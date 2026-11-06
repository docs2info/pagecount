// adversarial_test.go gives CountPages hostile and edge-case PDFs. Most are three.pdf's structure
// with one part changed by variant. 04-cycle, for example, replaces three.pdf's
//
//	<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 >>
//
// with /Kids [6 0 R 5 0 R], where object 6's /Kids lead back to object 2: a cycle in the page tree
// (ISO 32000-1 §7.7.3). Each entry in adversarial states the count CountPages must return or the
// error it must give.

package pdf

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// adversarial lists hostile or edge-case PDFs (written to testdata/adversarial/ under -update).
// Each is a variant of the 3-page classic-xref base (catalog 1, /Pages 2, pages 3–5) unless its
// generator says otherwise. want is the count CountPages must return; err, if set, a substring its
// error must hold.
var adversarial = []struct {
	name string
	gen  func() []byte
	want int
	err  string
}{
	{"01-two-roots", twoRoots, 1, ""},
	{"02-update-swaps-catalog", updateSwapsCatalog, 1, ""},
	// RULE: a page is an object, so a /Page listed twice in /Kids counts once.
	{"03-shared-leaf", variant(func(o map[int]string) {
		o[2] = pagesNode("3 0 R 3 0 R 4 0 R 5 0 R", 3)
	}), 3, ""},
	{"04-cycle", variant(func(o map[int]string) {
		o[2] = pagesNode("6 0 R 5 0 R", 3)
		o[6] = pagesNode("3 0 R 4 0 R 2 0 R", 2)
	}), 0, "cycle"},
	{"05-deep-tree", deepTree, 1, ""},
	// RULE: /Type alone decides what a node is; a node without /Type /Page or /Pages is an error,
	// whatever other keys it has.
	{"06-untyped-leaf", variant(func(o map[int]string) {
		o[5] = "<< /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 6 0 R >>"
		o[6] = "<< /Length 7 >>\nstream\n0 0 m S\nendstream"
	}), 0, "page tree: node has no /Type"},
	// RULE: a /Type /Page node with /Kids is contradictory and an error, not 1 page or its kids.
	{"07-typed-intermediate", variant(func(o map[int]string) {
		o[2] = pagesNode("3 0 R 4 0 R", 3)
		o[4] = "<< /Type /Page /Parent 2 0 R /Kids [5 0 R] /Count 1 >>"
	}), 0, "/Page node has /Kids"},
	{"08-count-high", variant(func(o map[int]string) { o[2] = pagesNode("3 0 R 4 0 R 5 0 R", 7) }),
		3, "count mismatch: /Count=7 walked=3"},
	{"09-count-zero", variant(func(o map[int]string) { o[2] = pagesNode("3 0 R 4 0 R 5 0 R", 0) }),
		3, "count mismatch"},
	{"10-count-negative", variant(func(o map[int]string) { o[2] = pagesNode("3 0 R 4 0 R 5 0 R", -1) }),
		3, "count mismatch"},
	{"11-count-huge", variant(func(o map[int]string) {
		o[2] = "<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 2147483648 >>"
	}), 3, "count mismatch"},
	// 1e9 is not PDF number syntax (§7.3.3), so /Pages fails to parse.
	{"11-count-huge-1e9", variant(func(o map[int]string) {
		o[2] = "<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 1e9 >>"
	}), 0, `unexpected keyword "1e9"`},
	{"12-indirect-everything", variant(func(o map[int]string) {
		o[2] = "<< /Type 6 0 R /Kids 7 0 R /Count 8 0 R >>"
		for n := 3; n <= 5; n++ {
			o[n] = "<< /Type 9 0 R /Parent 2 0 R /MediaBox [0 0 595 842] >>"
		}
		o[6], o[7], o[8], o[9] = "/Pages", "[3 0 R 4 0 R 5 0 R]", "3", "/Page"
	}), 3, ""},
	{"13-xref-wrong-offset", xrefWrongOffset, 0, "object number mismatch"},
	{"14-xref-wrong-gen", xrefWrongGen, 0, "object generation mismatch"},
	{"15-hybrid-xrefstm", hybridPDF, 3, ""},
	{"16-xref-index-gaps", xrefIndexGaps, 3, ""},
	{"17-double-definition", doubleDefinition, 3, ""},
	{"18-objstm-then-direct", objstmThenDirect, 3, ""},
	{"19-predictor-12", func() []byte { return predictorPDF(12) }, 3, ""},
	{"20-predictor-2", func() []byte { return predictorPDF(2) }, 3, ""},
	{"21-objstm-bad-n", func() []byte { return badObjStm(5, 0) }, 0, "object stream 6"},
	{"22-objstm-bad-first", func() []byte { return badObjStm(3, 10) }, 0, "bad /N or /First"},
	{"23-startxref-off-by-3", startxrefOffBy3, 0, "neither xref table nor xref stream"},
	{"24-two-eof", twoEOF, 0, "no object header"},
	{"25-page-in-string", variant(func(o map[int]string) {
		o[6] = "(3 0 obj << /Type /Page >> endobj)"
	}), 3, ""},
	{"26-page-in-stream", variant(func(o map[int]string) {
		o[3] = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 6 0 R >>"
		o[6] = "<< /Length 32 >>\nstream\n3 0 obj << /Type /Page >> endobj\nendstream"
	}), 3, ""},
	{"27-free-list-kid", freeListKid, 0, "free object referenced"},
	{"28-encrypted-plain", func() []byte {
		return classicPDF(baseObjs(), " /Encrypt << /Filter /Standard /V 1 /R 2 >>")
	}, 0, "encrypted"},
	{"29-linearized-lie", linearizedLie, 3, ""},
	{"30-name-hex", func() []byte {
		o := map[int]string{
			1: "<< /T#79pe /Catalog /Pag#65s 2 0 R >>",
			2: "<< /T#79pe /Pag#65s /K#69ds [3 0 R 4 0 R 5 0 R] /Count 3 >>",
		}
		for n := 3; n <= 5; n++ {
			o[n] = "<< /T#79pe /Pag#65 /Parent 2 0 R >>"
		}
		return classicPDF(o, "")
	}, 3, ""},
	{"31-comment-in-dict", variant(func(o map[int]string) {
		o[2] = "<< /Type% c\n/Pages /Kids%\n[3 0 R 4 0 R 5 0 R] /Count 3 >>"
		for n := 3; n <= 5; n++ {
			o[n] = "<< /Type% c\n/Page /Parent 2 0 R >>"
		}
	}), 3, ""},
	{"32-null-whitespace", nullWhitespace, 3, ""},
	{"33-cr-only-eol", func() []byte { return styledPDF("\r", " \r") }, 3, ""},
	// RULE: xref tables are read as tokens, so 19-byte entries (no space before the EOL) are
	// tolerated; every offset is still checked against the object header it points at.
	{"34-xref-19-byte", func() []byte { return styledPDF("\n", "\n") }, 3, ""},
	{"35-kids-not-array", variant(func(o map[int]string) {
		o[2] = "<< /Type /Pages /Kids << /A 3 0 R >> /Count 3 >>"
	}), 0, "/Kids is not an array"},
	{"36-kid-not-dict", variant(func(o map[int]string) { o[2] = pagesNode("3 0 R 4 0 R 42", 3) }),
		0, "kid is not a dictionary"},
	{"37-root-not-catalog", func() []byte { return classicPDF(baseObjs(), "", 2) }, 0, "unresolvable /Pages"},
	// RULE: the catalog's /Pages must be a /Type /Pages node; a lone /Page there is an error.
	{"38-pages-ref-to-leaf", variant(func(o map[int]string) {
		o[1] = "<< /Type /Catalog /Pages 3 0 R >>"
	}), 0, "root /Type is /Page, want /Pages"},
	{"39-count-real", variant(func(o map[int]string) {
		o[2] = "<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3.0 >>"
	}), 0, "/Count is not an integer"},
	{"40-count-overflow", variant(func(o map[int]string) {
		o[2] = "<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 99999999999999999999 >>"
	}), 0, "/Count out of range"},
	{"41-count-missing", variant(func(o map[int]string) {
		o[2] = "<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] >>"
	}), 0, "/Pages has no /Count"},
}

// TestAdversarial runs CountPages on each generator's output. -update also writes the files to
// testdata/adversarial/ (gitignored) for inspection with other tools.
func TestAdversarial(t *testing.T) {
	dir := filepath.Join("testdata", "adversarial")
	if update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range adversarial {
		t.Run(c.name, func(t *testing.T) {
			b := c.gen()
			if update {
				if err := os.WriteFile(filepath.Join(dir, c.name+".pdf"), b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			n, err := CountPages(b)
			switch {
			case c.err == "" && err != nil:
				t.Errorf("got %d, %v; want %d", n, err, c.want)
			case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
				t.Errorf("got %d, %v; want error containing %q", n, err, c.err)
			case n != c.want:
				t.Errorf("got %d, %v; want %d", n, err, c.want)
			}
		})
	}
}

// baseObjs returns the 3-page base: catalog 1, /Pages 2, pages 3–5.
func baseObjs() map[int]string {
	return map[int]string{1: catalog, 2: pagesNode("3 0 R 4 0 R 5 0 R", 3),
		3: page, 4: page, 5: page}
}

// variant returns a generator for the base with edit applied to its object bodies.
func variant(edit func(map[int]string)) func() []byte {
	return func() []byte {
		o := baseObjs()
		edit(o)
		return classicPDF(o, "")
	}
}

// classicPDF writes objs in ascending order with a classic xref table. The trailer's /Root is
// root (default 1); extra is appended inside the trailer dictionary.
func classicPDF(objs map[int]string, extra string, root ...int) []byte {
	return classicW(objs, extra, root...).Bytes()
}

func classicW(objs map[int]string, extra string, root ...int) *pdfWriter {
	r := 1
	if len(root) > 0 {
		r = root[0]
	}
	nums := slices.Sorted(func(yield func(int) bool) {
		for n := range objs {
			if !yield(n) {
				return
			}
		}
	})
	w := newPDF("1.4")
	for _, n := range nums {
		w.obj(n, objs[n])
	}
	w.table(w.rowsFor(nums...), fmt.Sprintf("<< /Size %d /Root %d 0 R%s >>",
		nums[len(nums)-1]+1, r, extra))
	return w
}

// rowsFor returns the xref rows for objects nums (generation 0) after the free head of object 0.
func (w *pdfWriter) rowsFor(nums ...int) []xrow {
	rows := []xrow{{0, -1, 65535}}
	for _, n := range nums {
		rows = append(rows, xrow{n, w.off[n], 0})
	}
	return rows
}

// setStartxref rewrites the final startxref offset of b to off.
func setStartxref(b []byte, off int) []byte {
	i := bytes.LastIndex(b, []byte("startxref\n")) + len("startxref\n")
	j := i + bytes.IndexByte(b[i:], '\n')
	return slices.Concat(b[:i], []byte(strconv.Itoa(off)), b[j:])
}

// xrefStream writes object num as a FlateDecode xref stream with /W [1 4 2] holding rows, then the
// startxref that points at it. dict adds /Index, /Root, /Prev and so on.
func (w *pdfWriter) xrefStream(num int, dict string, rows [][3]int) {
	xs := w.Len()
	w.stream(num, "/Type /XRef /W [1 4 2] /Filter /FlateDecode "+dict,
		deflate(xrefRows([3]int{1, 4, 2}, rows)))
	fmt.Fprintf(w, "startxref\n%d\n%%%%EOF\n", xs)
}

// twoRoots: /Root → a 1-page tree; a second catalog and 5-page tree sit unreferenced in the file.
func twoRoots() []byte {
	o := baseObjs()
	o[2] = pagesNode("3 0 R", 1)
	o[6] = pagesNode("7 0 R 8 0 R 9 0 R 10 0 R 11 0 R", 5)
	for n := 7; n <= 11; n++ {
		o[n] = "<< /Type /Page /Parent 6 0 R >>"
	}
	o[12] = "<< /Type /Catalog /Pages 6 0 R >>"
	return classicPDF(o, "")
}

// updateSwapsCatalog: the 3-page base, then an update with a new catalog 6 and 1-page /Pages 7.
func updateSwapsCatalog() []byte {
	w := classicW(baseObjs(), "")
	prev := w.lastXref
	w.obj(6, "<< /Type /Catalog /Pages 7 0 R >>")
	w.obj(7, pagesNode("3 0 R", 1))
	w.table([]xrow{{6, w.off[6], 0}, {7, w.off[7], 0}},
		fmt.Sprintf("<< /Size 8 /Root 6 0 R /Prev %d >>", prev))
	return w.Bytes()
}

// deepTree: 10000 single-child /Pages nodes (2–10001) above one leaf (10002).
func deepTree() []byte {
	const leaf = 10002
	o := map[int]string{1: catalog, leaf: "<< /Type /Page /Parent 10001 0 R >>"}
	for n := 2; n < leaf; n++ {
		o[n] = pagesNode(fmt.Sprintf("%d 0 R", n+1), 1)
	}
	return classicPDF(o, "")
}

// xrefWrongOffset: the entry for object 4 holds object 9's offset.
func xrefWrongOffset() []byte {
	o := baseObjs()
	o[9] = page
	w := newPDF("1.4")
	for _, n := range []int{1, 2, 3, 4, 5, 9} {
		w.obj(n, o[n])
	}
	w.off[4] = w.off[9]
	w.table(w.rowsFor(1, 2, 3, 4, 5, 9), "<< /Size 10 /Root 1 0 R >>")
	return w.Bytes()
}

// xrefWrongGen: the xref says 4 0; the file has "4 1 obj" at that offset.
func xrefWrongGen() []byte {
	w := newPDF("1.4")
	o := baseObjs()
	for n := 1; n <= 5; n++ {
		if n == 4 {
			w.off[4] = w.Len()
			fmt.Fprintf(w, "4 1 obj\n%s\nendobj\n", page)
			continue
		}
		w.obj(n, o[n])
	}
	w.table(w.rowsFor(1, 2, 3, 4, 5), "<< /Size 6 /Root 1 0 R >>")
	return w.Bytes()
}

// xrefIndexGaps: a classic section with the catalog, then an xref stream with /Index
// [0 1 10 3 20 2] and /Prev to it, holding /Pages 10 and pages 11, 12, 20.
func xrefIndexGaps() []byte {
	w := newPDF("1.5")
	w.obj(1, "<< /Type /Catalog /Pages 10 0 R >>")
	w.table(w.rowsFor(1), "<< /Size 2 /Root 1 0 R >>")
	prev := w.lastXref
	w.obj(10, "<< /Type /Pages /Kids [11 0 R 12 0 R 20 0 R] /Count 3 >>")
	for _, n := range []int{11, 12, 20} {
		w.obj(n, "<< /Type /Page /Parent 10 0 R >>")
	}
	xs := w.Len()
	rows := [][3]int{{0, 0, 65535}, {1, w.off[10], 0}, {1, w.off[11], 0}, {1, w.off[12], 0},
		{1, w.off[20], 0}, {1, xs, 0}}
	w.xrefStream(21, fmt.Sprintf("/Size 22 /Index [0 1 10 3 20 2] /Root 1 0 R /Prev %d", prev), rows)
	return w.Bytes()
}

// doubleDefinition: "4 0 obj" (an annotation) and "4 1 obj" (a page) both present; the xref and
// /Kids name generation 1.
func doubleDefinition() []byte {
	o := baseObjs()
	o[2] = pagesNode("3 0 R 4 1 R 5 0 R", 3)
	w := newPDF("1.4")
	for n := 1; n <= 5; n++ {
		if n == 4 {
			w.WriteString("4 0 obj\n<< /Type /Annot /Subtype /Text >>\nendobj\n")
			w.off[4] = w.Len()
			fmt.Fprintf(w, "4 1 obj\n%s\nendobj\n", page)
			continue
		}
		w.obj(n, o[n])
	}
	rows := w.rowsFor(1, 2, 3, 4, 5)
	rows[4].gen = 1
	w.table(rows, "<< /Size 6 /Root 1 0 R >>")
	return w.Bytes()
}

// objstmThenDirect: section 1 is an xref stream with object 5 (an annotation) in object stream 6;
// a classic update redefines 5 0 obj directly as a /Page.
func objstmThenDirect() []byte {
	w := newPDF("1.5")
	o := baseObjs()
	for n := 1; n <= 4; n++ {
		w.obj(n, o[n])
	}
	data, first := objStm([]int{5}, []string{"<< /Type /Annot /Subtype /Text >>"})
	w.stream(6, fmt.Sprintf("/Type /ObjStm /N 1 /First %d /Filter /FlateDecode", first),
		deflate(data))
	xs := w.Len()
	rows := [][3]int{{0, 0, 65535}}
	for n := 1; n <= 4; n++ {
		rows = append(rows, [3]int{1, w.off[n], 0})
	}
	rows = append(rows, [3]int{2, 6, 0}, [3]int{1, w.off[6], 0}, [3]int{1, xs, 0})
	w.xrefStream(7, "/Size 8 /Root 1 0 R", rows)
	w.obj(5, page)
	w.table([]xrow{{5, w.off[5], 0}}, fmt.Sprintf("<< /Size 8 /Root 1 0 R /Prev %d >>", xs))
	return w.Bytes()
}

// predictorPDF: the base indexed by an xref stream with /W [1 2 2] under /Predictor pred
// (12: PNG, every filter type; 2: TIFF).
func predictorPDF(pred int) []byte {
	w := newPDF("1.5")
	o := baseObjs()
	for n := 1; n <= 5; n++ {
		w.obj(n, o[n])
	}
	xs := w.Len()
	rows := [][3]int{{0, 0, 65535}}
	for n := 1; n <= 5; n++ {
		rows = append(rows, [3]int{1, w.off[n], 0})
	}
	rows = append(rows, [3]int{1, xs, 0})
	raw := xrefRows([3]int{1, 2, 2}, rows)
	var enc []byte
	if pred == 2 {
		enc = slices.Clone(raw)
		for r := 0; r < len(enc); r += 5 {
			for i := r + 4; i > r; i-- {
				enc[i] -= enc[i-1]
			}
		}
	} else {
		enc = pngRows(raw, 5)
	}
	w.stream(6,
		fmt.Sprintf("/Type /XRef /Size 7 /W [1 2 2] /Root 1 0 R /Filter /FlateDecode /DecodeParms << /Predictor %d /Columns 5 >>", pred),
		deflate(enc))
	fmt.Fprintf(w, "startxref\n%d\n%%%%EOF\n", xs)
	return w.Bytes()
}

// badObjStm: pages 3–5 in object stream 6 whose header holds 3 pairs, declared with /N n and
// /First moved firstDelta bytes past the true value.
func badObjStm(n, firstDelta int) []byte {
	w := newPDF("1.5")
	o := baseObjs()
	w.obj(1, o[1])
	w.obj(2, o[2])
	data, first := objStm([]int{3, 4, 5}, []string{page, page, page})
	if firstDelta > 0 {
		first = len(data) + firstDelta
	}
	w.stream(6, fmt.Sprintf("/Type /ObjStm /N %d /First %d /Filter /FlateDecode", n, first),
		deflate(data))
	xs := w.Len()
	rows := [][3]int{{0, 0, 65535}, {1, w.off[1], 0}, {1, w.off[2], 0}, {2, 6, 0}, {2, 6, 1},
		{2, 6, 2}, {1, w.off[6], 0}, {1, xs, 0}}
	w.xrefStream(7, "/Size 8 /Root 1 0 R", rows)
	return w.Bytes()
}

// startxrefOffBy3: the base with startxref 3 bytes before its xref table.
func startxrefOffBy3() []byte {
	w := classicW(baseObjs(), "")
	return setStartxref(w.Bytes(), w.lastXref-3)
}

// twoEOF: the base, then an update ending in its own %%EOF whose startxref points at the
// second entry inside the first section's xref table.
func twoEOF() []byte {
	w := classicW(baseObjs(), "")
	first := w.lastXref
	w.obj(2, pagesNode("3 0 R 4 0 R 5 0 R", 3))
	w.table([]xrow{{2, w.off[2], 0}}, fmt.Sprintf("<< /Size 6 /Root 1 0 R /Prev %d >>", first))
	return setStartxref(w.Bytes(), first+len("xref\n0 6\n")+20)
}

// freeListKid: the xref marks object 5 free; /Kids still references 5 0 R.
func freeListKid() []byte {
	w := newPDF("1.4")
	o := baseObjs()
	for n := 1; n <= 5; n++ {
		w.obj(n, o[n])
	}
	rows := w.rowsFor(1, 2, 3, 4)
	rows = append(rows, xrow{5, -1, 1})
	w.table(rows, "<< /Size 6 /Root 1 0 R >>")
	return w.Bytes()
}

// linearizedLie: a first-object /Linearized dictionary claiming /N 10 pages.
func linearizedLie() []byte {
	w := newPDF("1.4")
	w.obj(6, "<< /Linearized 1 /L 1000 /H [0 0] /O 3 /E 0 /N 10 /T 0 >>")
	o := baseObjs()
	for n := 1; n <= 5; n++ {
		w.obj(n, o[n])
	}
	w.table(w.rowsFor(1, 2, 3, 4, 5, 6), "<< /Size 7 /Root 1 0 R >>")
	return w.Bytes()
}

// nullWhitespace: every space in object headers and bodies is NUL (§7.2.2, Table 1).
func nullWhitespace() []byte {
	w := newPDF("1.4")
	o := baseObjs()
	for n := 1; n <= 5; n++ {
		w.off[n] = w.Len()
		w.WriteString(strings.ReplaceAll(fmt.Sprintf("%d 0 obj %s endobj\n", n, o[n]), " ", "\x00"))
	}
	w.table(w.rowsFor(1, 2, 3, 4, 5), "<< /Size 6 /Root 1 0 R >>")
	return w.Bytes()
}

// styledPDF: the base with eol ending every line and entryEnd ending each xref entry after its
// n/f keyword (" \n" is the spec's 20-byte form).
func styledPDF(eol, entryEnd string) []byte {
	var w bytes.Buffer
	w.WriteString("%PDF-1.4" + eol)
	o := baseObjs()
	off := map[int]int{}
	for n := 1; n <= 5; n++ {
		off[n] = w.Len()
		fmt.Fprintf(&w, "%d 0 obj%s%s%sendobj%s", n, eol, o[n], eol, eol)
	}
	xref := w.Len()
	fmt.Fprintf(&w, "xref%s0 6%s%010d %05d f%s", eol, eol, 0, 65535, entryEnd)
	for n := 1; n <= 5; n++ {
		fmt.Fprintf(&w, "%010d %05d n%s", off[n], 0, entryEnd)
	}
	fmt.Fprintf(&w, "trailer%s<< /Size 6 /Root 1 0 R >>%sstartxref%s%d%s%%%%EOF%s",
		eol, eol, eol, xref, eol, eol)
	return w.Bytes()
}
