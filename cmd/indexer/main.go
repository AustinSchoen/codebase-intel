package main

import (
	"fmt"
	"os"

	"github.com/AustinSchoen/codebase-intel/internal/config"
	"github.com/AustinSchoen/codebase-intel/internal/indexer"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	idx, err := indexer.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create indexer: %v\n", err)
		os.Exit(1)
	}

	if err := idx.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Indexer error: %v\n", err)
		os.Exit(1)
	}
}
