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
		daemon     = flag.Bool("daemon", false, "Run in daemon mode (watch files + register with MCP server)")
		serverURL  = flag.String("server-url", "", "MCP server URL for daemon mode (e.g., https://codebase-intel.example.com)")
		serverKey  = flag.String("server-key", "", "Bearer token for MCP server authentication")
		nodeID     = flag.String("node-id", "", "Node identifier for daemon registration (defaults to hostname)")
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

	// Daemon mode
	if *daemon {
		if *serverURL == "" {
			fmt.Fprintf(os.Stderr, "Error: -server-url is required in daemon mode\n")
			os.Exit(1)
		}

		nid := *nodeID
		if nid == "" {
			hostname, err := os.Hostname()
			if err != nil {
				nid = "unknown"
			} else {
				nid = hostname
			}
		}

		d := indexer.NewDaemon(idx, indexer.DaemonConfig{
			ServerURL: *serverURL,
			ServerKey: *serverKey,
			NodeID:    nid,
		})

		fmt.Fprintf(os.Stderr, "Starting daemon mode: node=%s codebase=%s server=%s\n",
			nid, cfg.Codebase.Name, *serverURL)

		if err := d.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Daemon error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Run indexer (one-shot mode)
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
