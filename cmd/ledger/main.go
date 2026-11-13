// Command ledger rebuilds derived indexes of an offline current-version ledger.
package main

import (
	"flag"
	"fmt"
	"github.com/kksty/tianwu/internal/store"
	"os"
)

func main() {
	source := flag.String("source", "", "offline SQLite ledger path")
	reindex := flag.Bool("reindex", false, "rebuild derived search and relation indexes")
	flag.Parse()
	if *source == "" || !*reindex {
		fmt.Fprintln(os.Stderr, "--source and --reindex are required")
		os.Exit(2)
	}
	if _, err := os.Stat(*source); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s, err := store.Open(*source)
	if err == nil {
		err = s.RebuildIndexes()
		s.Close()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("derived indexes rebuilt:", *source)
}
