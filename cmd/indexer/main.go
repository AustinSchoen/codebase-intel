package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AustinSchoen/codebase-intel/internal/config"
	"github.com/AustinSchoen/codebase-intel/internal/discovery"
	"github.com/AustinSchoen/codebase-intel/internal/indexer"
)

// multiFlag collects repeated occurrences of a string flag (e.g.
// `-config a.yaml -config b.yaml`) into a slice while still behaving like an
// ordinary flag when supplied once.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func main() {
	var configFiles multiFlag

	var (
		path      = flag.String("path", "", "Path to codebase to index (overrides codebase.path in the config; only valid with a single -config)")
		configDir = flag.String("config-dir", "", "Directory containing per-codebase config.yaml files. All *.yaml in the directory are loaded. Composes with repeated -config flags.")
		reindex   = flag.Bool("reindex", false, "Force full re-index (ignore content hashes)")
		migrate   = flag.Bool("migrate", false, "Run database migrations and exit")
		daemon    = flag.Bool("daemon", false, "Run in daemon mode (watch files + register with MCP server)")
		serverURL = flag.String("server-url", "", "MCP server URL for daemon mode (e.g., https://codebase-intel.example.com)")
		serverKey = flag.String("server-key", "", "Bearer token for MCP server authentication")
		nodeID    = flag.String("node-id", "", "Node identifier for daemon registration (defaults to hostname)")
		discover  = flag.Bool("discover", false, "Scan the local network for a codebase-intel server via mDNS and print results as JSON, then exit. Used by setup-indexer.sh.")
	)
	flag.Var(&configFiles, "config", "Path to a codebase config.yaml. Repeat for multiple codebases in daemon mode.")
	flag.Parse()

	// -discover is a one-shot helper for setup scripts; runs before any
	// config loading because discovery has no codebase prerequisites.
	if *discover {
		runDiscovery()
		return
	}

	// Collect config paths from both -config flags and -config-dir.
	configPaths := append([]string{}, configFiles...)
	if *configDir != "" {
		dirPaths, err := loadConfigDir(*configDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read -config-dir %s: %v\n", *configDir, err)
			os.Exit(1)
		}
		configPaths = append(configPaths, dirPaths...)
	}

	// Load configs. If no -config or -config-dir is supplied, fall back to
	// the default search path (config.Load) for backward compatibility.
	var configs []*config.Config
	if len(configPaths) == 0 {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
			os.Exit(1)
		}
		configs = []*config.Config{cfg}
	} else {
		for _, p := range configPaths {
			cfg, err := config.LoadFromFile(p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to load config %s: %v\n", p, err)
				os.Exit(1)
			}
			configs = append(configs, cfg)
		}
	}

	// -path only makes sense with a single config.
	if *path != "" {
		if len(configs) != 1 {
			fmt.Fprintf(os.Stderr, "Error: -path can only be used with a single -config\n")
			os.Exit(1)
		}
		configs[0].Codebase.Path = *path
	}

	// Disable incremental mode for forced reindex.
	if *reindex {
		for _, cfg := range configs {
			cfg.Indexing.Incremental = false
		}
	}

	// Build one Indexer per config. Reject duplicate codebase names — the
	// daemon's indexer map and the server's node map both key on codebase
	// name, so duplicates would silently shadow each other.
	indexers := make(map[string]*indexer.Indexer, len(configs))
	for _, cfg := range configs {
		name := cfg.Codebase.Name
		if _, dup := indexers[name]; dup {
			fmt.Fprintf(os.Stderr, "Error: duplicate codebase name %q across -config flags\n", name)
			os.Exit(1)
		}
		idx, err := indexer.New(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create indexer for %s: %v\n", name, err)
			os.Exit(1)
		}
		indexers[name] = idx
	}

	// Migrations: schema is shared across codebases, so run against the first
	// indexer and exit.
	if *migrate {
		migrationsDir := findMigrationsDir()
		// Map iteration order is non-deterministic; pick whatever comes out.
		var any *indexer.Indexer
		for _, idx := range indexers {
			any = idx
			break
		}
		if err := any.RunMigrations(context.Background(), migrationsDir); err != nil {
			fmt.Fprintf(os.Stderr, "Migration failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Migrations complete.")
		return
	}

	// Daemon mode: serve all indexers from one process, one SSE connection.
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

		d := indexer.NewDaemon(indexers, indexer.DaemonConfig{
			ServerURL: *serverURL,
			ServerKey: *serverKey,
			NodeID:    nid,
		})

		codebaseNames := make([]string, 0, len(indexers))
		for name := range indexers {
			codebaseNames = append(codebaseNames, name)
		}
		fmt.Fprintf(os.Stderr, "Starting daemon mode: node=%s codebases=%v server=%s\n",
			nid, codebaseNames, *serverURL)

		if err := d.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Daemon error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// One-shot mode: index each codebase sequentially.
	for name, idx := range indexers {
		if len(indexers) > 1 {
			fmt.Fprintf(os.Stderr, "Indexing codebase: %s\n", name)
		}
		if err := idx.RunOnce(); err != nil {
			fmt.Fprintf(os.Stderr, "Indexer error for %s: %v\n", name, err)
			os.Exit(1)
		}
	}
}

// loadConfigDir returns the paths of every *.yaml file in dir, sorted so the
// loaded order is deterministic across machines.
func loadConfigDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	sort.Strings(paths)
	return paths, nil
}

// runDiscovery scans the LAN for codebase-intel servers via mDNS and prints
// the results as JSON for consumption by setup-indexer.sh. Exits 0 on success
// (even if no servers responded — empty array is a valid result).
func runDiscovery() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	services, err := discovery.Discover(ctx, 3*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "discovery error: %v\n", err)
		os.Exit(1)
	}
	out := struct {
		Services []discovery.Service `json:"services"`
	}{Services: services}
	if services == nil {
		out.Services = []discovery.Service{} // marshal as [] not null
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "encode error: %v\n", err)
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
