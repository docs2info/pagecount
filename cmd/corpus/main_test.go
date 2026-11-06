package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParsePages(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"Title: x\nPages:          12\nEncrypted: no\n", 12},
		{"Syntax Error: junk\nPages: 3\n", 3},
		{"Title: x\n", -1},
		{"Pages: many\n", -1},
		{"", -1},
	} {
		if got := parsePages([]byte(c.in)); got != c.want {
			t.Errorf("parsePages(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSummarize(t *testing.T) {
	rows := []row{
		{file: "a.pdf", helloCount: 3, pdfinfoCount: 3},
		{file: "b.pdf", helloCount: -1, helloErr: "encrypted", pdfinfoCount: 2},
		{file: "c.pdf", helloCount: -1, helloErr: "encrypted", pdfinfoCount: -1},
		{file: "d.pdf", helloCount: 4, helloErr: "count mismatch", pdfinfoCount: 5},
		{file: "e.pdf", helloCount: 2, pdfinfoCount: 7},
		{file: "f.pdf", helloCount: 2, pdfinfoCount: -1},
	}
	var b strings.Builder
	summarize(&b, rows)
	want := "files\t6\nagree\t1\nrefused\t3\n  2\tencrypted\n  1\tcount mismatch\ndisagree\t2\n" +
		"  file\thello_count\thello_error\tpdfinfo_count\n  e.pdf\t2\t\t7\n  f.pdf\t2\t\t\n"
	if b.String() != want {
		t.Errorf("summary:\n%s\nwant:\n%s", b.String(), want)
	}
}

// TestRun runs the walk over the parser's fixtures; it needs pdfinfo on PATH.
func TestRun(t *testing.T) {
	pdfinfo, err := exec.LookPath("pdfinfo")
	if err != nil {
		t.Skip("pdfinfo not on PATH")
	}
	dir := t.TempDir()
	three, err := os.ReadFile("../../three.pdf")
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"sub/three.PDF": three, "junk.pdf": []byte("not a pdf"),
		"skip.txt": three} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := run(dir, pdfinfo, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if r := rows[0]; r.file != "junk.pdf" || !r.refused() || r.pdfinfoCount != -1 {
		t.Errorf("junk.pdf row = %+v", r)
	}
	if r := rows[1]; r.file != filepath.Join("sub", "three.PDF") || !r.agree() || r.helloCount != 3 {
		t.Errorf("three.PDF row = %+v", r)
	}
	out := filepath.Join(dir, "corpus.tsv")
	if err := writeTSV(out, rows); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) != 3 ||
		lines[0] != "file\thello_count\thello_error\tpdfinfo_count" {
		t.Errorf("tsv:\n%s", b)
	}
}
