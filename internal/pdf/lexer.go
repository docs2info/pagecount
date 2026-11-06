// lexer.go splits PDF bytes into tokens, the smallest units of PDF syntax (ISO 32000-1 §7.2). The
// body of a PDF is text such as this, from three.pdf:
//
//	2 0 obj
//	<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 >>
//	endobj
//
// lexer.next returns it one token at a time: the number 2, the number 0, the keyword obj, the
// dictionary opener <<, the name Type, and so on; tokKind lists the kinds. Tokens are separated by
// whitespace (isSpace) or by delimiter characters such as ( ) < > [ ] / % (isDelim); % starts a
// comment, which skipSpace skips. lexer.name decodes names such as /Type, lexer.literal and
// lexer.hex decode strings. object.go assembles tokens into objects.

package pdf

import (
	"fmt"
	"strconv"
)

// tokKind classifies a PDF token (ISO 32000-1 §7.2).
type tokKind int

const (
	tokEOF        tokKind = iota
	tokName               // /Name, #xx escapes decoded, without the slash
	tokNumber             // integer or real
	tokString             // literal (…) or hex <…>, decoded
	tokKeyword            // run of regular characters that isn't a number: true, obj, R, stream, …
	tokArrayOpen          // [
	tokArrayClose         // ]
	tokDictOpen           // <<
	tokDictClose          // >>
)

type token struct {
	kind  tokKind
	s     string  // name, keyword or string bytes
	num   float64 // number value
	isInt bool    // number had no decimal point
	pos   int     // offset of the token's first byte
}

// lexer splits b into PDF tokens starting at pos. Comments are skipped as whitespace.
type lexer struct {
	b   []byte
	pos int
}

// isSpace reports whether c is PDF whitespace (§7.2.2, Table 1).
func isSpace(c byte) bool {
	switch c {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}

// isDelim reports whether c is a PDF delimiter (§7.2.2, Table 2).
func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func isRegular(c byte) bool { return !isSpace(c) && !isDelim(c) }

// skipSpace advances past whitespace and comments (§7.2.3).
func (l *lexer) skipSpace() {
	for l.pos < len(l.b) {
		c := l.b[l.pos]
		switch {
		case isSpace(c):
			l.pos++
		case c == '%':
			for l.pos < len(l.b) && l.b[l.pos] != '\n' && l.b[l.pos] != '\r' {
				l.pos++
			}
		default:
			return
		}
	}
}

// next returns the next token, or tokEOF at the end of input.
func (l *lexer) next() (token, error) {
	l.skipSpace()
	start := l.pos
	if start >= len(l.b) {
		return token{kind: tokEOF, pos: start}, nil
	}
	two := func(c byte) bool { return start+1 < len(l.b) && l.b[start+1] == c }
	switch c := l.b[start]; {
	case c == '/':
		l.pos++
		return l.name(start), nil
	case c == '(':
		l.pos++
		return l.literal(start)
	case c == '<' && two('<'):
		l.pos += 2
		return token{kind: tokDictOpen, pos: start}, nil
	case c == '<':
		l.pos++
		return l.hex(start)
	case c == '>' && two('>'):
		l.pos += 2
		return token{kind: tokDictClose, pos: start}, nil
	case c == '[':
		l.pos++
		return token{kind: tokArrayOpen, pos: start}, nil
	case c == ']':
		l.pos++
		return token{kind: tokArrayClose, pos: start}, nil
	case isDelim(c):
		return token{}, fmt.Errorf("offset %d: unexpected %q", start, c)
	}
	for l.pos < len(l.b) && isRegular(l.b[l.pos]) {
		l.pos++
	}
	w := l.b[start:l.pos]
	if n, isInt, ok := parseNumber(w); ok {
		return token{kind: tokNumber, num: n, isInt: isInt, pos: start}, nil
	}
	return token{kind: tokKeyword, s: string(w), pos: start}, nil
}

// parseNumber parses an integer or real (§7.3.3): optional sign, digits, at most one '.'.
func parseNumber(w []byte) (n float64, isInt, ok bool) {
	i, digits, dot := 0, 0, false
	if len(w) > 0 && (w[0] == '+' || w[0] == '-') {
		i++
	}
	for ; i < len(w); i++ {
		switch c := w[i]; {
		case c >= '0' && c <= '9':
			digits++
		case c == '.' && !dot:
			dot = true
		default:
			return 0, false, false
		}
	}
	if digits == 0 {
		return 0, false, false
	}
	f, err := strconv.ParseFloat(string(w), 64)
	if err != nil {
		return 0, false, false
	}
	return f, !dot, true
}

// name reads a name after its '/', decoding #xx escapes (§7.3.5).
func (l *lexer) name(start int) token {
	var out []byte
	for l.pos < len(l.b) && isRegular(l.b[l.pos]) {
		c := l.b[l.pos]
		l.pos++
		if c == '#' && l.pos+1 < len(l.b) {
			if hi, lo := unhex(l.b[l.pos]), unhex(l.b[l.pos+1]); hi >= 0 && lo >= 0 {
				c = byte(hi<<4 | lo)
				l.pos += 2
			}
		}
		out = append(out, c)
	}
	return token{kind: tokName, s: string(out), pos: start}
}

// literal reads a literal string after its '(' (§7.3.4.2): balanced parentheses, backslash escapes,
// line continuations, and any unescaped EOL normalised to '\n'.
func (l *lexer) literal(start int) (token, error) {
	var out []byte
	depth := 1
	for l.pos < len(l.b) {
		c := l.b[l.pos]
		l.pos++
		switch c {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return token{kind: tokString, s: string(out), pos: start}, nil
			}
		case '\r':
			if l.pos < len(l.b) && l.b[l.pos] == '\n' {
				l.pos++
			}
			c = '\n'
		case '\\':
			if l.pos >= len(l.b) {
				return token{}, fmt.Errorf("offset %d: unterminated string", start)
			}
			e := l.b[l.pos]
			l.pos++
			switch e {
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			case 'b':
				c = '\b'
			case 'f':
				c = '\f'
			case '\r':
				if l.pos < len(l.b) && l.b[l.pos] == '\n' {
					l.pos++
				}
				continue
			case '\n':
				continue
			case '0', '1', '2', '3', '4', '5', '6', '7':
				v := int(e - '0')
				for k := 0; k < 2 && l.pos < len(l.b) && l.b[l.pos] >= '0' && l.b[l.pos] <= '7'; k++ {
					v = v<<3 | int(l.b[l.pos]-'0')
					l.pos++
				}
				c = byte(v)
			default: // \( \) \\ map to themselves; an unknown escape drops the backslash
				c = e
			}
		}
		out = append(out, c)
	}
	return token{}, fmt.Errorf("offset %d: unterminated string", start)
}

// hex reads a hex string after its '<' (§7.3.4.3). An odd final digit is followed by an implied 0.
func (l *lexer) hex(start int) (token, error) {
	var out []byte
	hi := -1
	for l.pos < len(l.b) {
		c := l.b[l.pos]
		l.pos++
		switch v := unhex(c); {
		case c == '>':
			if hi >= 0 {
				out = append(out, byte(hi<<4))
			}
			return token{kind: tokString, s: string(out), pos: start}, nil
		case isSpace(c):
		case v < 0:
			return token{}, fmt.Errorf("offset %d: bad hex string byte %q", l.pos-1, c)
		case hi < 0:
			hi = v
		default:
			out = append(out, byte(hi<<4|v))
			hi = -1
		}
	}
	return token{}, fmt.Errorf("offset %d: unterminated hex string", start)
}

func unhex(c byte) int {
	switch {
	case c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	case 'A' <= c && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// expectInt reads a token that must be a non-negative integer.
func (l *lexer) expectInt() (int, error) {
	t, err := l.next()
	if err != nil {
		return 0, err
	}
	if t.kind != tokNumber || !t.isInt || (!(0 <= t.num && t.num <= maxInt)) {
		return 0, fmt.Errorf("offset %d: want non-negative integer", t.pos)
	}
	return int(t.num), nil
}

// expectKeyword reads a token that must be the keyword `kw`.
func (l *lexer) expectKeyword(kw string) error {
	t, err := l.next()
	if err != nil {
		return err
	}
	if t.kind != tokKeyword || t.s != kw {
		return fmt.Errorf("offset %d: want %q", t.pos, kw)
	}
	return nil
}
