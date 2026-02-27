package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AustinSchoen/codebase-intel/internal/config"
	"github.com/AustinSchoen/codebase-intel/internal/indexer"
)

func main() {
	var (
		path       = flag.String("path", "", "Path to codebase to index")
		configFile = flag.String("config", "", "Path to config.yaml")
		reindex    = flag.Bool("reindex", false, "Force full re-index (ignore content hashes)")
		migrate    = flag.Bool("migrate", false, "Run database migrations and exit")
	)
	flag.Parse()

	// Load config
	var cfg *config.Config
	var err error
	if *configFile != "" {
		cfg, err = config.LoadFromFile(*configFile)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Override codebase path if provided
	if *path != "" {
		cfg.Codebase.Path = *path
	}

	// Disable incremental mode for reindex
	if *reindex {
		cfg.Indexing.Incremental = false
	}

	idx, err := indexer.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create indexer: %v\n", err)
		os.Exit(1)
	}

	// Run migrations if requested
	if *migrate {
		migrationsDir := findMigrationsDir()
		if err := idx.RunMigrations(context.Background(), migrationsDir); err != nil {
			fmt.Fprintf(os.Stderr, "Migration failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Migrations complete.")
		return
	}

	// Run indexer
	if err := idx.RunOnce(); err != nil {
		fmt.Fprintf(os.Stderr, "Indexer error: %v\n", err)
		os.Exit(1)
	}
}

// findMigrationsDir locates the migrations directory relative to the repo root.
func findMigrationsDir() string {
	// Use git rev-parse to find repo root
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err == nil {
		root := strings.TrimSpace(string(out))
		dir := filepath.Join(root, "migrations")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	// Fallback to relative path
	return "migrations"
}
