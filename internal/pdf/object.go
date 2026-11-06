// object.go builds PDF objects from tokens (ISO 32000-1 §7.3). A PDF file is mostly a list of
// numbered objects, each wrapped in "number generation obj … endobj". From three.pdf:
//
//	3 0 obj
//	<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>
//	endobj
//
// Object 3, generation 0, is a dictionary (Dict) from names (Name) to values: here a Name, a
// reference to object 2 (Ref, written "2 0 R"), and an Array of Number. References are how objects
// point at each other. parseObject reads one object; parseIndirect reads the "3 0 obj" wrapper at
// a byte offset and, when the dictionary is followed by the keyword stream, the binary data that
// comes with it (Stream).

package pdf

import (
	"errors"
	"fmt"
	"math"
)

// PDF objects (ISO 32000-1 §7.3). Booleans are bool and null is nil; every other object is one of
// these types.
type (
	Name   string  // without the leading slash
	Number float64 // integer (§7.3.3)
	Real   float64 // number written with a '.'; kept apart so callers can demand an integer
	String string
	Array  []any
	Dict   map[Name]any
	Ref    struct{ Num, Gen int }
	Stream struct {
		Dict Dict
		Raw  []byte // undecoded data; aliases the file bytes
	}
)

const (
	maxDepth = 256     // nesting limit for arrays and dictionaries
	maxInt   = 1 << 53 // largest integer a Number holds exactly
)

// asInt returns o as an int if it is an integral Number or Real.
func asInt(o any) (int, bool) {
	var f float64
	switch n := o.(type) {
	case Number:
		f = float64(n)
	case Real:
		f = float64(n)
	default:
		return 0, false
	}
	if f != math.Trunc(f) || !(-maxInt <= f && f <= maxInt) {
		return 0, false
	}
	return int(f), true
}

// parseObject reads one direct object from l. "num gen R" is returned as a Ref.
func parseObject(l *lexer) (any, error) { return parseObjectDepth(l, 0) }

func parseObjectDepth(l *lexer, depth int) (any, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("offset %d: objects nested deeper than %d", l.pos, maxDepth)
	}
	t, err := l.next()
	if err != nil {
		return nil, err
	}
	switch t.kind {
	case tokName:
		return Name(t.s), nil
	case tokString:
		return String(t.s), nil
	case tokNumber:
		if t.isInt && t.num >= 0 && t.num <= maxInt {
			save := l.pos
			if g, err := l.next(); err == nil && g.kind == tokNumber &&
				g.isInt && g.num >= 0 && g.num <= maxInt {
				if r, err := l.next(); err == nil && r.kind == tokKeyword && r.s == "R" {
					return Ref{int(t.num), int(g.num)}, nil
				}
			}
			l.pos = save
		}
		if !t.isInt {
			return Real(t.num), nil
		}
		return Number(t.num), nil
	case tokArrayOpen:
		var a Array
		for {
			save := l.pos
			t, err := l.next()
			if err != nil {
				return nil, err
			}
			if t.kind == tokArrayClose {
				return a, nil
			}
			l.pos = save
			v, err := parseObjectDepth(l, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
	case tokDictOpen:
		d := Dict{}
		for {
			k, err := l.next()
			if err != nil {
				return nil, err
			}
			if k.kind == tokDictClose {
				return d, nil
			}
			if k.kind != tokName {
				return nil, fmt.Errorf("offset %d: dictionary key is not a name", k.pos)
			}
			v, err := parseObjectDepth(l, depth+1)
			if err != nil {
				return nil, err
			}
			if v != nil { // a null value is the same as an absent key (§7.3.7)
				d[Name(k.s)] = v
			}
		}
	case tokKeyword:
		switch t.s {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			return nil, nil
		}
		return nil, fmt.Errorf("offset %d: unexpected keyword %q", t.pos, t.s)
	case tokEOF:
		return nil, errors.New("unexpected end of data")
	}
	return nil, fmt.Errorf("offset %d: unexpected token", t.pos)
}

// parseIndirect parses the indirect object "num gen obj …" at off in b (§7.3.10). If want.Num ≥ 0
// the header must match want. A dictionary followed by "stream" becomes a Stream whose /Length is
// resolved through length.
func parseIndirect(b []byte, off int, want Ref, length func(any) (int, error)) (any, error) {
	if !(0 <= off && off < len(b)) {
		return nil, fmt.Errorf("object offset %d outside file", off)
	}
	l := lexer{b: b, pos: off}
	num, err1 := l.expectInt()
	gen, err2 := l.expectInt()
	if err1 != nil || err2 != nil || l.expectKeyword("obj") != nil {
		return nil, fmt.Errorf("no object header at offset %d", off)
	}
	if want.Num >= 0 && num != want.Num {
		return nil, fmt.Errorf("object number mismatch: offset %d holds %d %d, want %d %d",
			off, num, gen, want.Num, want.Gen)
	}
	if want.Num >= 0 && gen != want.Gen {
		return nil, fmt.Errorf("object generation mismatch: offset %d holds %d %d, want %d %d",
			off, num, gen, want.Num, want.Gen)
	}
	o, err := parseObject(&l)
	if err != nil {
		return nil, err
	}
	d, ok := o.(Dict)
	if !ok {
		return o, nil
	}
	if t, err := l.next(); err != nil || t.kind != tokKeyword || t.s != "stream" {
		return d, nil
	}
	// Data starts after the EOL that follows "stream": CRLF or LF (§7.3.8.1); a lone CR is tolerated.
	p := l.pos
	if p < len(b) && b[p] == '\r' {
		p++
	}
	if p < len(b) && b[p] == '\n' {
		p++
	}
	n, err := length(d["Length"])
	if err != nil {
		return nil, fmt.Errorf("stream /Length: %w", err)
	}
	if n > len(b)-p {
		return nil, fmt.Errorf("stream /Length %d runs past end of file", n)
	}
	return Stream{Dict: d, Raw: b[p : p+n]}, nil
}
