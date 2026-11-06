# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`hello` prints the page count of a PDF: `go run . three.pdf` → `three.pdf: 3 pages`. The parser is
`internal/pdf` (`CountPages`); `main.go` wraps it. `cmd/corpus` compares it with `pdfinfo` over a
directory. Stdlib only, no dependencies in `go.mod`. `three.pdf` is the sample input
used in the README.

## Commands

```sh
go build ./...
go test ./...
go test -run TestNoNet .
go test -run TestCountPages -update ./internal/pdf   # regenerate testdata/
go run ./cmd/corpus -o corpus.tsv DIR   # needs pdfinfo
go run . three.pdf
```

## Product promises enforced in code

The README's claims are constraints, not marketing copy:

- **"Sends nothing anywhere."** `TestNoNet` (`nonet_test.go`) runs `go list -deps .` and fails if
  any package starting with `net` is linked, directly or transitively. Any new dependency, stdlib
  or third-party, must keep this test green. It needs `go` on `PATH`.
- **"One static binary, no server."** Don't add CGo, config files, or runtime services.

## How page counting works

A page is a dictionary reached from the trailer `/Root` → `/Pages`, following `/Kids`, whose `/Type`
is `/Page`. `CountPages` (`internal/pdf/pages.go`) counts those leaves; it never scans for byte patterns.
`lexer.go` tokenises (§7.2), `object.go` parses objects, `xref.go` reads classic tables, xref
streams, hybrid `/XRefStm` and `/Prev` chains (newest section wins per object), `objstm.go` decodes
`/ObjStm` (FlateDecode, predictors 2 and 10–15). Errors, never guesses: encrypted, no `startxref`,
unresolvable `/Root`/`/Pages`, a broken xref entry, a malformed page tree (cycle, untyped node,
`/Page` with `/Kids`, non-array `/Kids`, free or missing kid), a root `/Pages` with no `/Count`, a
non-integer or out-of-range (beyond ±2^53) `/Count`, or `count mismatch` (walked count returned
with the error). Fixtures in `internal/pdf/testdata/` come from generators in `pdf_test.go`; regenerate with
`go test -run 'TestCountPages|TestAdversarial' -update ./internal/pdf`; the hostile cases live in
`adversarial_test.go`, which tests generator output directly; `-update` also writes them to the
gitignored `testdata/adversarial/`.

## Exit codes

`2` for wrong argument count (usage on stderr) or any `CountPages` error (`refused: …` on stderr), `1` for
read errors, `0` otherwise.

## Do NOT commit this file

This a public repo.


## Git workflow
Keep `main`'s history readable: many small commits on a working branch, one squashed commit per PR.

1. Work on one long-lived feature branch (currently `pre-product`). Commit small and often there.
2. When a longer set of changes looks good, squash it onto a fresh branch off `main`:
   ```
   git switch -c <topic> main
   git merge --squash pre-product
   git commit
   ```
   The message describes the whole change set, not the steps that produced it.
3. I push `<topic>` and open the PR. Never push.
4. After the PR merges, `git merge main` into the feature branch so the next squash carries only new
   work.

