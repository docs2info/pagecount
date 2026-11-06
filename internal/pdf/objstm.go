// objstm.go reads object streams (ISO 32000-1 §7.5.7): one stream that holds several objects,
// compressed together. three.pdf has none; the testdata fixture objstm.pdf keeps its five
// objects in one:
//
//	6 0 obj
//	<< /Type /ObjStm /N 5 /First 26 /Filter /FlateDecode /Length 133 >>
//	stream
//	…compressed bytes…
//	endstream
//
// Decompressed, the data starts with /N pairs "object number, offset" and holds the objects
// themselves from byte /First on. decode undoes /FlateDecode (zlib) and its predictors
// (§7.4.4, unpredict); loadObjStm reads the pairs into an objStream; fromObjStm parses one object
// out of it. readXRefStream uses decode for xref streams too.

package pdf

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
)

// maxDecoded caps the bytes decoded from all streams of one document, so compressed bombs can't
// exhaust memory, whether in one stream or spread across many. A variable so tests can lower it.
var maxDecoded = 256 << 20

// objStream is a decoded /Type /ObjStm (§7.5.7).
type objStream struct {
	data  []byte
	first int   // /First: offset of the first object in data
	nums  []int // object number at each index
	offs  []int // object offset at each index, relative to first
}

// fromObjStm returns object num, stored at index idx of object stream stm.
func (d *document) fromObjStm(stm, idx, num int) (any, error) {
	s, ok := d.objstm[stm]
	if !ok {
		var err error
		if s, err = d.loadObjStm(stm); err != nil {
			return nil, fmt.Errorf("object stream %d: %w", stm, err)
		}
		d.objstm[stm] = s
	}
	if idx < 0 || idx >= len(s.nums) {
		return nil, fmt.Errorf("object stream %d has no index %d", stm, idx)
	}
	if s.nums[idx] != num {
		return nil, fmt.Errorf("object stream %d index %d holds object %d, want %d",
			stm, idx, s.nums[idx], num)
	}
	p := s.first + s.offs[idx]
	if p >= len(s.data) {
		return nil, fmt.Errorf("object stream %d: object %d offset outside stream", stm, num)
	}
	o, err := parseObject(&lexer{b: s.data, pos: p})
	if err != nil {
		return nil, fmt.Errorf("object %d in stream %d: %w", num, stm, err)
	}
	return o, nil
}

func (d *document) loadObjStm(stm int) (*objStream, error) {
	o, err := d.object(Ref{Num: stm})
	if err != nil {
		return nil, err
	}
	st, ok := o.(Stream)
	if !ok || st.Dict["Type"] != Name("ObjStm") {
		return nil, errors.New("not a /Type /ObjStm stream")
	}
	data, err := d.decode(st)
	if err != nil {
		return nil, err
	}
	nv, err := d.resolve(st.Dict["N"])
	if err != nil {
		return nil, err
	}
	fv, err := d.resolve(st.Dict["First"])
	if err != nil {
		return nil, err
	}
	n, ok1 := asInt(nv)
	first, ok2 := asInt(fv)
	if !ok1 || !ok2 || n < 0 || first < 0 || first > len(data) {
		return nil, errors.New("bad /N or /First")
	}
	s := &objStream{data: data, first: first}
	l := &lexer{b: data[:first]}
	for range n {
		num, err := l.expectInt()
		if err != nil {
			return nil, err
		}
		off, err := l.expectInt()
		if err != nil {
			return nil, err
		}
		s.nums, s.offs = append(s.nums, num), append(s.offs, off)
	}
	return s, nil
}

// decode decodes s and charges its output to the document's budget, which object streams draw on
// for as long as they stay cached.
func (d *document) decode(s Stream) ([]byte, error) {
	data, err := decodeStream(s, maxDecoded-d.decoded)
	if err != nil {
		return nil, err
	}
	d.decoded += len(data)
	return data, nil
}

// decodeStream applies the stream's filters, failing if any stage outputs more than limit bytes.
// Only /FlateDecode is supported, with the predictors of its /DecodeParms (§7.4.4).
func decodeStream(s Stream, limit int) ([]byte, error) {
	var filters, parms Array
	switch f := s.Dict["Filter"].(type) {
	case nil:
	case Name:
		filters = Array{f}
	case Array:
		filters = f
	default:
		return nil, errors.New("bad /Filter")
	}
	switch p := s.Dict["DecodeParms"].(type) {
	case Dict:
		parms = Array{p}
	case Array:
		parms = p
	}
	data := s.Raw
	for i, f := range filters {
		if f != Name("FlateDecode") {
			return nil, fmt.Errorf("unsupported filter %v", f)
		}
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("FlateDecode: %w", err)
		}
		out, err := io.ReadAll(io.LimitReader(zr, int64(limit)+1))
		// Producers often truncate the zlib trailer; keep what decoded and let parsing judge it.
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("FlateDecode: %w", err)
		}
		if len(out) > limit {
			return nil, fmt.Errorf("decoded streams exceed %d bytes", maxDecoded)
		}
		var p Dict
		if i < len(parms) {
			p, _ = parms[i].(Dict)
		}
		if data, err = unpredict(out, p); err != nil {
			return nil, err
		}
	}
	return data, nil
}

// unpredict reverses a /Predictor (§7.4.4.4, Table 8): 1 none, 2 TIFF (8 bits per component only),
// 10–15 PNG with a per-row filter type byte.
func unpredict(data []byte, p Dict) ([]byte, error) {
	param := func(k Name, def int) int {
		if n, ok := asInt(p[k]); ok {
			return n
		}
		return def
	}
	pred, colors, bpc, cols := param("Predictor", 1), param("Colors", 1),
		param("BitsPerComponent", 8), param("Columns", 1)
	if pred == 1 {
		return data, nil
	}
	if colors < 1 || colors > 32 || cols < 1 || cols > 1<<24 {
		return nil, errors.New("predictor: bad /Colors or /Columns")
	}
	switch bpc {
	case 1, 2, 4, 8, 16:
	default:
		return nil, errors.New("predictor: bad /BitsPerComponent")
	}
	bpp := (colors*bpc + 7) / 8 // bytes per pixel, for the left neighbour
	rowlen := (colors*bpc*cols + 7) / 8
	switch {
	case pred == 2:
		if bpc != 8 {
			return nil, fmt.Errorf("TIFF predictor: /BitsPerComponent %d unsupported", bpc)
		}
		for r := 0; r < len(data); r += rowlen {
			row := data[r:min(r+rowlen, len(data))]
			for i := bpp; i < len(row); i++ {
				row[i] += row[i-bpp]
			}
		}
		return data, nil
	case 10 <= pred && pred <= 15:
		if len(data)%(rowlen+1) != 0 {
			return nil, errors.New("PNG predictor: data is not whole rows")
		}
		out := make([]byte, 0, len(data)/(rowlen+1)*rowlen)
		prev := make([]byte, rowlen)
		for r := 0; r < len(data); r += rowlen + 1 {
			ft, row := data[r], data[r+1:r+1+rowlen]
			for i := range row {
				var left, upLeft byte
				if i >= bpp {
					left, upLeft = row[i-bpp], prev[i-bpp]
				}
				switch ft {
				case 0:
				case 1:
					row[i] += left
				case 2:
					row[i] += prev[i]
				case 3:
					row[i] += byte((int(left) + int(prev[i])) / 2)
				case 4:
					row[i] += paeth(left, prev[i], upLeft)
				default:
					return nil, fmt.Errorf("PNG predictor: bad filter type %d", ft)
				}
			}
			out = append(out, row...)
			prev = row
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported /Predictor %d", pred)
}

// paeth is the PNG Paeth predictor: whichever of left, up, upper-left is nearest left+up-upLeft.
func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
