// hello: prints the page count of a PDF. Reads one file, sends nothing anywhere.
package main

import (
	"fmt"
	"os"

	"github.com/docs2info/pagecount/internal/pdf"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: hello FILE.pdf")
		os.Exit(2)
	}
	b, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	n, err := pdf.CountPages(b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: refused: %v\n", os.Args[1], err)
		os.Exit(2)
	}
	fmt.Printf("%s: %d pages\n", os.Args[1], n)
}
