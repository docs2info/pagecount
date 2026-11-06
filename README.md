# hello

Prints the page count of a PDF, or exits with code 2 (`refused`) if it cannot be sure.

- **Documents stay with you.** It reads one file and sends nothing anywhere.
- **Nothing to operate.** One static binary, no server, no account.
- **Nothing is metered.** Currently open source; never a charge per document or per month.

Counting PDF pages looks simple enough to be reliably handled by existing libraries, and that's
probably true. It's the "hello world" of document verification: we don't take a library's word for
anything, we check.

```console
% go run . three.pdf
three.pdf: 3 pages
```

```sh
go install github.com/docs2info/pagecount@latest
```

```sh
go test ./...
```

## Does it count correctly?

The goal is that the page count `hello` prints matches the count from a reference tool, `pdfinfo`
from Poppler.

`cmd/corpus` runs hello's parser and `pdfinfo` on every PDF in a directory and writes a TSV with
one row per file: `file`, `hello_count`, `hello_error`, `pdfinfo_count`.

```sh
go run ./cmd/corpus -o corpus.tsv DIR
```

On 200 PDFs from the RECAP court-filing corpus (1622 pages, largest file 55 pages), all 200 counts
agree and none were refused. These files are ordinary well-formed PDFs; the test says nothing about
damaged or hostile files.

`go test ./...` also checks 42 malformed and hostile PDFs (`TestAdversarial`) and verifies that the
binary links no network packages (`TestNoNet`).

## How it counts and when it refuses

A page is a dictionary reached from the trailer `/Root` → `/Pages`, following `/Kids`, whose
`/Type` is `/Page`; `hello` counts those leaves. It reads the cross-reference data (classic tables,
xref streams, object streams, incremental updates) rather than scanning bytes. It refuses, with
exit code 2, rather than guess: encrypted files, no `startxref`, an unresolvable `/Root` or
`/Pages`, a malformed page tree, or a root `/Count` that is missing, not an integer, out of range,
or disagrees with the walked tree.

- A page object listed more than once in the tree counts once; a `/Kids` entry that leads back to
  its own ancestor is a cycle and an error. Viewers such as Acrobat and pdf.js render a leaf listed
  twice in `/Kids` twice, so their page count is one higher per repeat.
- `/Type` alone decides what a node is: a node with neither `/Type /Page` nor `/Type /Pages` is an
  error, whatever other keys it has.
- A `/Type /Page` node that also has `/Kids` is an error.
- The catalog's `/Pages` must be a `/Type /Pages` node; a lone `/Page` there is an error.
- That root `/Pages` node must carry an integer `/Count`: one that is missing, not an integer, or
  beyond ±2^53 is an error, and one that disagrees with the walked tree is a count mismatch.
- Cross-reference entries are read as tokens, so 19-byte entries are accepted; every offset is still
  checked against the object number and generation it points at.
