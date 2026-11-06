# internal/pdf

Package `pdf` reads the file structure of a PDF (the header, the body of numbered objects, the
cross-reference table and the trailer) far enough to find the page tree and count its pages. It
decompresses only what that requires: object streams and xref streams, with `/FlateDecode`. It
does not read content streams, fonts or images, and does not render.

## Types and data flow

| File | Identifier | What it is |
| --- | --- | --- |
| `lexer.go` | `lexer` | Splits bytes into `token`s: numbers, names, strings, keywords, `[ ] << >>`. |
| `object.go` | `parseObject` | Turns tokens into objects: `Dict`, `Array`, `Name`, `Number`, `Ref`, `Stream` and so on. |
| `xref.go` | `document` | The file opened through its xref data and trailer; `document.object` loads any object by number. |
| `objstm.go` | `objStream` | A decoded object stream, from which `document.object` takes compressed objects. |
| `pages.go` | `CountPages` | The entry point: opens a `document`, follows `/Root` → `/Pages`, walks `/Kids`, counts `/Page`. |

`CountPages` → `openDocument` (reads xref sections and trailer) → `document.resolve` (follows
`Ref`s through `document.object`, which calls `parseIndirect` or `fromObjStm`) → `walk`.

## How to read the §7.5.5 references

Section numbers in comments are sections of ISO 32000-1:2008 (PDF 1.7), the PDF standard. Adobe
hosts a free copy:
<https://opensource.adobe.com/dc-acrobat-sdk-docs/pdfstandards/PDF32000_2008.pdf>.

| § | Implemented by | Title |
| --- | --- | --- |
| 7.2 | `lexer`, `tokKind` | Lexical Conventions |
| 7.2.2 | `isSpace`, `isDelim` | Character Set |
| 7.2.3 | `lexer.skipSpace` | Comments |
| 7.3 | `parseObject`, the types in `object.go` | Objects |
| 7.3.3 | `parseNumber`, `Number`, `Real` | Numeric Objects |
| 7.3.4.2 | `lexer.literal` | Literal Strings |
| 7.3.4.3 | `lexer.hex` | Hexadecimal Strings |
| 7.3.5 | `lexer.name` | Name Objects |
| 7.3.7 | `parseObjectDepth` | Dictionary Objects |
| 7.3.8.1 | `parseIndirect` | Stream Objects: General |
| 7.3.10 | `parseIndirect`, `document.resolve` | Indirect Objects |
| 7.4.4 | `decodeStream` | LZWDecode and FlateDecode Filters |
| 7.4.4.4 | `unpredict` | LZW and Flate Predictor Functions |
| 7.5 | `openDocument` | File Structure |
| 7.5.4 | `document.readTable`, `xrefEntry` | Cross-Reference Table |
| 7.5.5 | `findStartxref`, `document.readTable` | File Trailer |
| 7.5.6 | `openDocument` (`/Prev` chain) | Incremental Updates |
| 7.5.7 | `objStream`, `document.loadObjStm` | Object Streams |
| 7.5.8 | `document.readXRefStream` | Cross-Reference Streams |
| 7.5.8.3 | `document.readXRefStream`, `xrefEntry` | Cross-Reference Stream Data |
| 7.5.8.4 | `document.readTable` (`/XRefStm`) | Compatibility with Applications That Do Not Support Compressed Reference Streams |
| 7.7.2 | `CountPages` | Document Catalog |
| 7.7.3 | `document.walk`, `document.node` | Page Tree |

## Dependencies

The three principles in the root README hold here: this package imports nothing outside the Go
standard library.
