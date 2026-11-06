// pdf_test.go builds the PDFs in testdata/ from Go generators and checks CountPages against each.
// The generators write the same syntax as three.pdf, for example pagesNode returns
//
//	<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 >>
//
// and pdfWriter records each object's offset for the xref data. Each generator covers one way
// ISO 32000-1 §7.5 lets a file store its index: classic writes a classic xref table (§7.5.4),
// incrementalPDF an appended update (§7.5.6), objstmPDF an object stream and xref stream (§7.5.7,
// §7.5.8), hybridPDF a table plus xref stream (§7.5.8.4).

package pdf

import (
	"bytes"
	"compress/zlib"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

var update bool

func init() {
	flag.BoolVar(&update, "update", false, "rewrite testdata/ from the generators in pdf_test.go")
}

// fixtures are the generated files in testdata/. `go test -run TestCountPages -update` rewrites
// them; otherwise TestCountPages fails if a file differs from its generator.
var fixtures = map[string]func() []byte{
	"classic.pdf":     func() []byte { return classic(3, "<< /Size 6 /Root 1 0 R >>").Bytes() },
	"objstm.pdf":      objstmPDF,
	"hybrid.pdf":      hybridPDF,
	"incremental.pdf": incrementalPDF,
	"badcount.pdf":    func() []byte { return classic(4, "<< /Size 6 /Root 1 0 R >>").Bytes() },
}

func TestCountPages(t *testing.T) {
	for name, gen := range fixtures {
		path := filepath.Join("testdata", name)
		if update {
			if err := os.MkdirAll("testdata", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, gen(), 0o644); err != nil {
				t.Fatal(err)
			}
		} else if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, gen()) {
			t.Errorf("%s is stale or missing (%v); run go test -run TestCountPages -update",
				path, err)
		}
	}
	for _, c := range []struct {
		path string
		want int
		err  string
	}{
		{"../../three.pdf", 3, ""},
		{"testdata/classic.pdf", 3, ""},
		{"testdata/objstm.pdf", 3, ""},
		{"testdata/hybrid.pdf", 3, ""},
		{"testdata/incremental.pdf", 2, ""},
		{"testdata/badcount.pdf", 3, "count mismatch: /Count=4 walked=3"},
	} {
		b, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatal(err)
		}
		n, err := CountPages(b)
		if got := fmt.Sprint(err); n != c.want || (c.err == "" && err != nil) || (c.err != "" &&
			got != c.err) {
			t.Errorf("%s: got %d, %v; want %d, %q", c.path, n, err, c.want, c.err)
		}
	}
}

// TestCountPagesRefuses checks each refusal is an error, not a count.
func TestCountPagesRefuses(t *testing.T) {
	good := classic(3, "<< /Size 6 /Root 1 0 R >>").Bytes()
	for name, c := range map[string]struct {
		in  []byte
		err string
	}{
		"encrypted": {classic(3, "<< /Size 6 /Root 1 0 R /Encrypt << /Filter /Standard >> >>").Bytes(),
			"encrypted"},
		"nostartxref": {good[:bytes.LastIndex(good, []byte("startxref"))], "no startxref"},
		"empty":       {nil, "no startxref"},
		"noroot":      {classic(3, "<< /Size 6 /Root 9 0 R >>").Bytes(), "unresolvable /Root"},
		"nopages":     {classic(3, "<< /Size 6 /Root 3 0 R >>").Bytes(), "unresolvable /Pages"},
	} {
		if n, err := CountPages(c.in); n != 0 || fmt.Sprint(err) != c.err {
			t.Errorf("%s: got %d, %v; want 0, %q", name, n, err, c.err)
		}
	}
}

// TestLexer covers token forms the fixtures don't: comments between tokens, no whitespace around
// delimiters, escapes in names and strings.
func TestLexer(t *testing.T) {
	o, err := parseObject(&lexer{b: []byte("<</Type%c\n/Page/K[1 0 R(a\\)\\101\\\nb)<4 1 6>/A#42]>>")})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprint(Dict{"Type": Name("Page"),
		"K": Array{Ref{1, 0}, String("a)Ab"), String("A`"), Name("AB")}})
	if got := fmt.Sprint(o); got != want {
		t.Errorf("got %s; want %s", got, want)
	}
}

// TestTIFFPredictor covers /Predictor 2, which the fixtures don't use.
func TestTIFFPredictor(t *testing.T) {
	raw := []byte{10, 20, 13, 25, 16, 30, 1, 2, 3, 4, 5, 6} // 2 rows × 3 pixels × 2 colors
	enc := append([]byte{}, raw...)
	for r := 0; r < len(enc); r += 6 {
		for i := r + 5; i >= r+2; i-- {
			enc[i] -= enc[i-2]
		}
	}
	got, err := unpredict(enc, Dict{"Predictor": Number(2), "Colors": Number(2),
		"Columns": Number(3)})
	if err != nil || !bytes.Equal(got, raw) {
		t.Errorf("got %v, %v; want %v", got, err, raw)
	}
}

const (
	catalog = "<< /Type /Catalog /Pages 2 0 R >>"
	page    = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>"
)

func pagesNode(kids string, count int) string {
	return fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, count)
}

// pdfWriter writes PDF bytes directly, recording offsets for the cross-reference data.
type pdfWriter struct {
	bytes.Buffer
	off      map[int]int
	lastXref int
}

func newPDF(version string) *pdfWriter {
	w := pdfWriter{off: map[int]int{}}
	w.WriteString("%PDF-" + version + "\n%\xe2\xe3\xcf\xd3\n")
	return &w
}

func (w *pdfWriter) obj(n int, body string) {
	w.off[n] = w.Len()
	fmt.Fprintf(w, "%d 0 obj\n%s\nendobj\n", n, body)
}

func (w *pdfWriter) stream(n int, dict string, data []byte) {
	w.off[n] = w.Len()
	fmt.Fprintf(w, "%d 0 obj\n<< %s /Length %d >>\nstream\n", n, dict, len(data))
	w.Write(data)
	w.WriteString("\nendstream\nendobj\n")
}

// xrow is one classic xref entry; off < 0 marks it free.
type xrow struct{ num, off, gen int }

// table writes a classic xref section of rows (ascending), its trailer and startxref.
func (w *pdfWriter) table(rows []xrow, trailer string) {
	w.lastXref = w.Len()
	w.WriteString("xref\n")
	for i := 0; i < len(rows); {
		j := i + 1
		for j < len(rows) && rows[j].num == rows[j-1].num+1 {
			j++
		}
		fmt.Fprintf(w, "%d %d\n", rows[i].num, j-i)
		for _, r := range rows[i:j] {
			if r.off < 0 {
				fmt.Fprintf(w, "%010d %05d f \n", 0, r.gen)
			} else {
				fmt.Fprintf(w, "%010d %05d n \n", r.off, r.gen)
			}
		}
		i = j
	}
	fmt.Fprintf(w, "trailer\n%s\nstartxref\n%d\n%%%%EOF\n", trailer, w.lastXref)
}

// classic is fixture (a): catalog 1, pages 2, pages 3–5, classic xref. count sets /Count.
func classic(count int, trailer string) *pdfWriter {
	w := newPDF("1.4")
	w.obj(1, catalog)
	w.obj(2, pagesNode("3 0 R 4 0 R 5 0 R", count))
	rows := []xrow{{0, -1, 65535}, {1, w.off[1], 0}, {2, w.off[2], 0}}
	for n := 3; n <= 5; n++ {
		w.obj(n, page)
		rows = append(rows, xrow{n, w.off[n], 0})
	}
	w.table(rows, trailer)
	return w
}

// incrementalPDF is fixture (c): (a) plus an update that drops page 5 from /Kids and frees it.
// Object 5's stale /Type /Page body stays in the file.
func incrementalPDF() []byte {
	w := classic(3, "<< /Size 6 /Root 1 0 R >>")
	prev := w.lastXref
	w.obj(2, pagesNode("3 0 R 4 0 R", 2))
	w.table([]xrow{{2, w.off[2], 0}, {5, -1, 1}},
		fmt.Sprintf("<< /Size 6 /Root 1 0 R /Prev %d >>", prev))
	return w.Bytes()
}

// objStm packs objects into an object stream body; it returns the data and /First.
func objStm(nums []int, bodies []string) ([]byte, int) {
	var hdr, body bytes.Buffer
	for i, n := range nums {
		fmt.Fprintf(&hdr, "%d %d ", n, body.Len())
		body.WriteString(bodies[i] + "\n")
	}
	first := hdr.Len()
	hdr.Write(body.Bytes())
	return hdr.Bytes(), first
}

// xrefRows encodes xref stream rows with the given /W widths.
func xrefRows(w [3]int, rows [][3]int) []byte {
	var out []byte
	for _, r := range rows {
		for k := range 3 {
			for s := w[k] - 1; s >= 0; s-- {
				out = append(out, byte(r[k]>>(8*s)))
			}
		}
	}
	return out
}

func deflate(b []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

// pngRows applies PNG filter type (row index mod 5) to each cols-byte row, so one stream exercises
// every type that /Predictor 10–15 decoding must reverse. One byte per pixel.
func pngRows(raw []byte, cols int) []byte {
	var out []byte
	prev := make([]byte, cols)
	for i := 0; i*cols < len(raw); i++ {
		row := raw[i*cols : (i+1)*cols]
		ft := byte(i % 5)
		out = append(out, ft)
		for j, x := range row {
			var left, ul byte
			if j > 0 {
				left, ul = row[j-1], prev[j-1]
			}
			p := [5]byte{0, left, prev[j], byte((int(left) + int(prev[j])) / 2),
				paeth(left, prev[j], ul)}[ft]
			out = append(out, x-p)
		}
		prev = row
	}
	return out
}

// objstmPDF is fixture (b): (a)'s objects 1–5 in object stream 6, indexed by xref stream 7 with
// FlateDecode and /Predictor 12.
func objstmPDF() []byte {
	w := newPDF("1.5")
	data, first := objStm([]int{1, 2, 3, 4, 5}, []string{catalog, pagesNode("3 0 R 4 0 R 5 0 R", 3),
		page, page, page})
	w.stream(6, fmt.Sprintf("/Type /ObjStm /N 5 /First %d /Filter /FlateDecode", first),
		deflate(data))
	xs := w.Len()
	rows := [][3]int{{0, 0, 65535}}
	for i := range 5 {
		rows = append(rows, [3]int{2, 6, i})
	}
	rows = append(rows, [3]int{1, w.off[6], 0}, [3]int{1, xs, 0})
	enc := deflate(pngRows(xrefRows([3]int{1, 2, 2}, rows), 5))
	w.stream(7, "/Type /XRef /Size 8 /W [1 2 2] /Root 1 0 R /Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 5 >>", enc)
	fmt.Fprintf(w, "startxref\n%d\n%%%%EOF\n", xs)
	return w.Bytes()
}

// hybridPDF is a hybrid-reference file (§7.5.8.4): catalog and page tree uncompressed in a classic
// table that lists pages 3–5 as free; they live in object stream 6, found through /XRefStm 7.
func hybridPDF() []byte {
	w := newPDF("1.5")
	w.obj(1, catalog)
	w.obj(2, pagesNode("3 0 R 4 0 R 5 0 R", 3))
	data, first := objStm([]int{3, 4, 5}, []string{page, page, page})
	w.stream(6, fmt.Sprintf("/Type /ObjStm /N 3 /First %d /Filter /FlateDecode", first), deflate(data))
	xs := w.Len()
	enc := deflate(xrefRows([3]int{1, 1, 1}, [][3]int{{2, 6, 0}, {2, 6, 1}, {2, 6, 2}}))
	w.stream(7, "/Type /XRef /Size 8 /W [1 1 1] /Index [3 3] /Filter /FlateDecode", enc)
	w.table([]xrow{{0, -1, 65535}, {1, w.off[1], 0}, {2, w.off[2], 0}, {3, -1, 0}, {4, -1, 0},
		{5, -1, 0}, {6, w.off[6], 0}, {7, xs, 0}},
		fmt.Sprintf("<< /Size 8 /Root 1 0 R /XRefStm %d >>", xs))
	return w.Bytes()
}
