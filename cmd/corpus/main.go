// corpus compares pagecount's page count with poppler's pdfinfo across a directory of PDFs. It
// writes one TSV row per file and prints a summary: agree, refused (grouped by error), disagree.
//
//	go run ./cmd/corpus [-o corpus.tsv] DIR
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docs2info/pagecount/internal/pdf"
)

// row is one file's result. pagecountCount and pdfinfoCount are -1 when absent. On a count
// mismatch CountPages returns the walked count with its error; that count is kept.
type row struct {
	file           string
	pagecountCount int
	pagecountErr   string
	pdfinfoCount   int
}

func main() {
	var out string
	var timeout time.Duration
	flag.StringVar(&out, "o", "corpus.tsv", "TSV output path")
	flag.DurationVar(&timeout, "timeout", time.Minute, "per-file pdfinfo timeout")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: corpus [-o corpus.tsv] DIR")
		os.Exit(2)
	}
	pdfinfo, err := exec.LookPath("pdfinfo")
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus: pdfinfo not found on PATH (install poppler-utils):", err)
		os.Exit(1)
	}
	rows, err := run(flag.Arg(0), pdfinfo, timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus:", err)
		os.Exit(1)
	}
	if err := writeTSV(out, rows); err != nil {
		fmt.Fprintln(os.Stderr, "corpus:", err)
		os.Exit(1)
	}
	summarize(os.Stdout, rows)
}

// run walks dir in lexical order and evaluates every *.pdf (case-insensitive).
func run(dir, pdfinfo string, timeout time.Duration) ([]row, error) {
	var rows []row
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() || !strings.EqualFold(filepath.Ext(path), ".pdf") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = path
		}
		r := row{file: rel, pagecountCount: -1, pdfinfoCount: -1}
		if b, err := os.ReadFile(path); err != nil {
			r.pagecountErr = err.Error()
		} else if n, err := pdf.CountPages(b); err != nil {
			r.pagecountErr = err.Error()
			if n > 0 {
				r.pagecountCount = n
			}
		} else {
			r.pagecountCount = n
		}
		r.pdfinfoCount = pdfinfoPages(pdfinfo, path, timeout)
		fmt.Fprintf(os.Stderr, "%d\t%s\n", len(rows)+1, rel)
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	return rows, nil
}

// pdfinfoPages returns pdfinfo's "Pages:" value, or -1 if pdfinfo fails, times out, or prints
// none. Output is parsed even on a non-zero exit: pdfinfo often reports pages despite warnings.
func pdfinfoPages(pdfinfo, path string, timeout time.Duration) int {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, _ := exec.CommandContext(ctx, pdfinfo, path).Output()
	return parsePages(out)
}

func parsePages(out []byte) int {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "Pages:"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n
			}
		}
	}
	return -1
}

func (r row) refused() bool  { return r.pagecountErr != "" }
func (r row) agree() bool    { return !r.refused() && r.pagecountCount == r.pdfinfoCount }
func (r row) disagree() bool { return !r.refused() && !r.agree() }

func (r row) tsv() string {
	return strings.Join([]string{clean(r.file), count(r.pagecountCount), clean(r.pagecountErr),
		count(r.pdfinfoCount)}, "\t")
}

func count(n int) string {
	if n < 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// clean keeps one row per line: tabs and newlines in paths or error text become spaces.
func clean(s string) string { return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s) }

func writeTSV(path string, rows []row) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, "file\tpagecount_count\tpagecount_error\tpdfinfo_count")
	for _, r := range rows {
		fmt.Fprintln(w, r.tsv())
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func summarize(w io.Writer, rows []row) {
	var agree int
	var disagree []row
	refused := map[string]int{}
	for _, r := range rows {
		switch {
		case r.refused():
			refused[r.pagecountErr]++
		case r.agree():
			agree++
		default:
			disagree = append(disagree, r)
		}
	}
	total := 0
	errs := make([]string, 0, len(refused))
	for e, n := range refused {
		errs = append(errs, e)
		total += n
	}
	// Most frequent first; ties alphabetical so output is stable.
	sort.Slice(errs, func(i, j int) bool {
		if refused[errs[i]] != refused[errs[j]] {
			return refused[errs[i]] > refused[errs[j]]
		}
		return errs[i] < errs[j]
	})
	fmt.Fprintf(w, "files\t%d\nagree\t%d\nrefused\t%d\n", len(rows), agree, total)
	for _, e := range errs {
		fmt.Fprintf(w, "  %d\t%s\n", refused[e], clean(e))
	}
	fmt.Fprintf(w, "disagree\t%d\n", len(disagree))
	if len(disagree) > 0 {
		fmt.Fprintln(w, "  file\tpagecount_count\tpagecount_error\tpdfinfo_count")
		for _, r := range disagree {
			fmt.Fprintln(w, "  "+r.tsv())
		}
	}
}
