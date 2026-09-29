// Command gen writes the zakwas.yaml JSON Schema to the file named by its
// argument; run it with `mise run schema`.
package main

import (
	"fmt"
	"os"

	"github.com/Automaat/zakwas/internal/schemagen"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gen <output file>")
		os.Exit(2)
	}
	out, err := schemagen.Generate()
	if err == nil {
		err = os.WriteFile(os.Args[1], out, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
