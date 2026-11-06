// xref.go reads the cross-reference table, the index at the end of a PDF that gives each object's
// byte offset, and the trailer after it (ISO 32000-1 §7.5.4, §7.5.5). The end of three.pdf:
//
//	xref
//	0 6
//	0000000000 65535 f
//	0000000009 00000 n
//	…
//	trailer
//	<< /Size 6 /Root 1 0 R >>
//	startxref
//	340
//	%%EOF
//
// A reader starts at the bottom: findStartxref finds startxref, whose number is the byte offset of
// the xref keyword. "0 6" says the table covers objects 0–5; each line after it is one xrefEntry,
// in order: object 1 is at offset 9, generation 0, n for in use; f marks a free entry. The trailer
// dictionary names the document catalog, /Root. Newer files store the table as a compressed xref
// stream instead (§7.5.8, readXRefStream), and files edited by appending carry earlier sections
// linked by /Prev (§7.5.6). openDocument follows that chain into a document, whose object method
// loads any object by number.

package pdf

import (
	"bytes"
	"errors"
	"fmt"
)

// xrefEntry locates one object (§7.5.4, §7.5.8.3).
type xrefEntry struct {
	typ int // 0 free, 1 uncompressed, 2 in an object stream
	a   int // typ 1: byte offset; typ 2: object stream number
	b   int // typ 1: generation; typ 2: index within the object stream
}

// document is a PDF opened through its cross-reference data. Objects are parsed on demand.
type document struct {
	b       []byte
	xref    map[int]xrefEntry
	trailer Dict
	cache   map[Ref]any // keyed by number and generation: a stale generation resolves to null
	objstm  map[int]*objStream
	loading map[int]bool // objects being parsed, to refuse self-referential /Length chains
	decoded int          // bytes decoded from streams so far, against maxDecoded
}

// openDocument reads the cross-reference chain ending at the file's startxref: classic tables, xref
// streams and hybrid /XRefStm, following /Prev. Newer sections win per object, so a free entry in
// an update hides the older object.
func openDocument(b []byte) (*document, error) {
	off, err := findStartxref(b)
	if err != nil {
		return nil, err
	}
	d := &document{
		b:       b,
		xref:    map[int]xrefEntry{},
		trailer: Dict{},
		cache:   map[Ref]any{},
		objstm:  map[int]*objStream{},
		loading: map[int]bool{},
	}
	seen := map[int]bool{}
	for {
		if seen[off] {
			return nil, fmt.Errorf("xref /Prev loop at offset %d", off)
		}
		seen[off] = true
		entries, trailer, err := d.readSection(off)
		if err != nil {
			return nil, fmt.Errorf("xref at offset %d: %w", off, err)
		}
		for n, e := range entries {
			if _, ok := d.xref[n]; !ok {
				d.xref[n] = e
			}
		}
		for k, v := range trailer {
			if _, ok := d.trailer[k]; !ok {
				d.trailer[k] = v
			}
		}
		prev, ok := asInt(trailer["Prev"])
		if !ok {
			break
		}
		off = prev
	}
	if _, ok := d.trailer["Encrypt"]; ok {
		return nil, errors.New("encrypted")
	}
	return d, nil
}

// findStartxref returns the offset after the last "startxref" keyword, which §7.5.5 places in the
// file's final 1024 bytes.
func findStartxref(b []byte) (int, error) {
	base := max(0, len(b)-1024)
	i := bytes.LastIndex(b[base:], []byte("startxref"))
	if i < 0 {
		return 0, errors.New("no startxref")
	}
	l := &lexer{b: b, pos: base + i}
	if err := l.expectKeyword("startxref"); err != nil {
		return 0, fmt.Errorf("no startxref: %w", err)
	}
	off, err := l.expectInt()
	if err != nil {
		return 0, fmt.Errorf("no startxref: %w", err)
	}
	return off, nil
}

// readSection reads the classic table or xref stream at off and returns its entries and trailer.
func (d *document) readSection(off int) (map[int]xrefEntry, Dict, error) {
	if off < 0 || off >= len(d.b) {
		return nil, nil, errors.New("offset outside file")
	}
	l := &lexer{b: d.b, pos: off}
	t, err := l.next()
	if err != nil {
		return nil, nil, err
	}
	switch {
	case t.kind == tokKeyword && t.s == "xref":
		return d.readTable(l)
	case t.kind == tokNumber:
		return d.readXRefStream(off)
	}
	return nil, nil, errors.New("neither xref table nor xref stream")
}

// readTable reads a classic xref table after its "xref" keyword (§7.5.4) and the trailer that
// follows. In a hybrid file the trailer's /XRefStm adds entries the table lists as free or omits
// (§7.5.8.4).
func (d *document) readTable(l *lexer) (map[int]xrefEntry, Dict, error) {
	entries := map[int]xrefEntry{}
	for {
		save := l.pos
		t, err := l.next()
		if err != nil {
			return nil, nil, err
		}
		if t.kind == tokKeyword && t.s == "trailer" {
			break
		}
		l.pos = save
		first, err := l.expectInt()
		if err != nil {
			return nil, nil, err
		}
		count, err := l.expectInt()
		if err != nil {
			return nil, nil, err
		}
		for i := range count {
			off, err := l.expectInt()
			if err != nil {
				return nil, nil, err
			}
			gen, err := l.expectInt()
			if err != nil {
				return nil, nil, err
			}
			k, err := l.next()
			if err != nil {
				return nil, nil, err
			}
			switch {
			case k.kind == tokKeyword && k.s == "n":
				entries[first+i] = xrefEntry{typ: 1, a: off, b: gen}
			case k.kind == tokKeyword && k.s == "f":
				entries[first+i] = xrefEntry{typ: 0}
			default:
				return nil, nil, fmt.Errorf("offset %d: xref entry type is not n or f", k.pos)
			}
		}
	}
	o, err := parseObject(l)
	if err != nil {
		return nil, nil, fmt.Errorf("trailer: %w", err)
	}
	trailer, ok := o.(Dict)
	if !ok {
		return nil, nil, errors.New("trailer is not a dictionary")
	}
	if xs, ok := asInt(trailer["XRefStm"]); ok {
		more, _, err := d.readXRefStream(xs)
		if err != nil {
			return nil, nil, fmt.Errorf("/XRefStm: %w", err)
		}
		for n, e := range more {
			if cur, ok := entries[n]; !ok || cur.typ == 0 {
				entries[n] = e
			}
		}
	}
	return entries, trailer, nil
}

// readXRefStream reads the xref stream object at off (§7.5.8). Its dictionary is the trailer.
func (d *document) readXRefStream(off int) (map[int]xrefEntry, Dict, error) {
	o, err := parseIndirect(d.b, off, Ref{Num: -1}, d.length)
	if err != nil {
		return nil, nil, err
	}
	s, ok := o.(Stream)
	if !ok || s.Dict["Type"] != Name("XRef") {
		return nil, nil, errors.New("not a /Type /XRef stream")
	}
	data, err := d.decode(s)
	if err != nil {
		return nil, nil, err
	}
	wa, _ := s.Dict["W"].(Array)
	if len(wa) != 3 {
		return nil, nil, errors.New("/W is not a 3-element array")
	}
	var w [3]int
	row := 0
	for i, v := range wa {
		n, ok := asInt(v)
		if !ok || n < 0 || n > 8 {
			return nil, nil, errors.New("/W field width not in 0..8")
		}
		w[i], row = n, row+n
	}
	if row == 0 {
		return nil, nil, errors.New("/W widths are all zero")
	}
	index, ok := s.Dict["Index"].(Array)
	if !ok {
		size, ok := asInt(s.Dict["Size"])
		if !ok {
			return nil, nil, errors.New("missing /Size")
		}
		index = Array{Number(0), Number(size)}
	}
	if len(index)%2 != 0 {
		return nil, nil, errors.New("/Index has odd length")
	}
	entries := map[int]xrefEntry{}
	p := 0
	for i := 0; i < len(index); i += 2 {
		first, ok1 := asInt(index[i])
		count, ok2 := asInt(index[i+1])
		if !ok1 || !ok2 || first < 0 || count < 0 {
			return nil, nil, errors.New("bad /Index entry")
		}
		if count > (len(data)-p)/row {
			return nil, nil, errors.New("xref stream data shorter than /Index says")
		}
		for j := range count {
			var f [3]int
			for k := range 3 {
				for range w[k] {
					f[k] = f[k]<<8 | int(data[p])
					p++
				}
			}
			if w[0] == 0 {
				f[0] = 1 // default type (Table 17)
			}
			switch f[0] {
			case 0:
				entries[first+j] = xrefEntry{typ: 0}
			case 1, 2:
				entries[first+j] = xrefEntry{typ: f[0], a: f[1], b: f[2]}
			} // other types are references to the null object: leave absent
		}
	}
	return entries, s.Dict, nil
}

// resolve follows references until o is a direct object. Missing or free objects resolve to nil
// (§7.3.10).
func (d *document) resolve(o any) (any, error) {
	for range 32 {
		r, ok := o.(Ref)
		if !ok {
			return o, nil
		}
		var err error
		if o, err = d.object(r); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("reference chain longer than 32")
}

// object loads the indirect object r.
func (d *document) object(r Ref) (any, error) {
	if o, ok := d.cache[r]; ok {
		return o, nil
	}
	e, ok := d.xref[r.Num]
	if !ok || e.typ == 0 {
		return nil, nil
	}
	if d.loading[r.Num] {
		return nil, fmt.Errorf("object %d depends on itself", r.Num)
	}
	d.loading[r.Num] = true
	defer delete(d.loading, r.Num)
	var o any
	var err error
	switch e.typ {
	case 1:
		if e.b != r.Gen {
			return nil, nil // the reference names a generation the xref no longer holds
		}
		if o, err = parseIndirect(d.b, e.a, r, d.length); err != nil {
			err = fmt.Errorf("object %d: %w", r.Num, err)
		}
	case 2:
		if r.Gen != 0 {
			return nil, nil
		}
		o, err = d.fromObjStm(e.a, e.b, r.Num)
	}
	if err != nil {
		return nil, err
	}
	d.cache[r] = o
	return o, nil
}

// length resolves a stream /Length to a non-negative integer.
func (d *document) length(o any) (int, error) {
	v, err := d.resolve(o)
	if err != nil {
		return 0, err
	}
	n, ok := asInt(v)
	if !ok || n < 0 {
		return 0, errors.New("not a non-negative integer")
	}
	return n, nil
}
