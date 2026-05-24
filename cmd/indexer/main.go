package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
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

	// Migrations are now server-side (#18): the MCP server runs them on
	// startup. The -migrate flag is preserved as a no-op with a hint so
	// existing setup scripts don't break.
	if *migrate {
		fmt.Fprintln(os.Stderr, "Note: migrations are now run automatically by the MCP server on startup. -migrate is a no-op.")
		return
	}

	// Indexer is a thin HTTP client to the server (#18). Both daemon and
	// one-shot modes need the server URL + bearer token.
	if *serverURL == "" {
		fmt.Fprintln(os.Stderr, "Error: -server-url is required. Use http://localhost:8090 if the server is on this host.")
		os.Exit(1)
	}
	// Fall back to $MCP_API_KEY if -server-key wasn't passed explicitly. This
	// is what setup-indexer.sh sets in the service file's environment, so the
	// daemon command line doesn't need to carry the token directly.
	if *serverKey == "" {
		*serverKey = os.Getenv("MCP_API_KEY")
	}
	if *serverKey == "" {
		fmt.Fprintln(os.Stderr, "warning: -server-key and MCP_API_KEY both empty; requests will be sent without an Authorization header")
	}

	// Build one Client per config. Reject duplicate codebase names — the
	// server's node map keys on codebase, so duplicates would shadow.
	clients := make(map[string]*indexer.Client, len(configs))
	for _, cfg := range configs {
		name := cfg.Codebase.Name
		if _, dup := clients[name]; dup {
			fmt.Fprintf(os.Stderr, "Error: duplicate codebase name %q across -config flags\n", name)
			os.Exit(1)
		}
		clients[name] = indexer.NewClient(cfg, *serverURL, *serverKey, nil)
	}

	// Daemon mode: serve all codebases from one process, one SSE connection.
	if *daemon {
		nid := *nodeID
		if nid == "" {
			hostname, err := os.Hostname()
			if err != nil {
				nid = "unknown"
			} else {
				nid = hostname
			}
		}

		d := indexer.NewDaemon(clients, indexer.DaemonConfig{
			ServerURL: *serverURL,
			ServerKey: *serverKey,
			NodeID:    nid,
		})

		codebaseNames := make([]string, 0, len(clients))
		for name := range clients {
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

	// One-shot mode: walk each codebase sequentially, upload to server, exit.
	for name, c := range clients {
		if len(clients) > 1 {
			fmt.Fprintf(os.Stderr, "Indexing codebase: %s\n", name)
		}
		if err := c.RunOnce(); err != nil {
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
