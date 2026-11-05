package main

import (
	"fmt"
	"os"

	"github.com/pr0sp3ro/dirhaven/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dirhaven:", err)
		os.Exit(1)
	}
}
