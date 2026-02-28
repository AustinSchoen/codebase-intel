package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/AustinSchoen/codebase-intel/internal/config"
	"github.com/AustinSchoen/codebase-intel/internal/mcp"
	"github.com/AustinSchoen/codebase-intel/internal/metrics"
)

func main() {
	configFile := flag.String("config", "", "Path to config.yaml")
	transport := flag.String("transport", "stdio", "Transport type: stdio or http")
	addr := flag.String("addr", ":8090", "Listen address for HTTP transport")
	flag.Parse()

	cfgPath := *configFile
	if cfgPath == "" {
		cfgPath = os.Getenv("CODEBASE_INTEL_CONFIG")
		if cfgPath == "" {
			cfgPath = "config.yaml"
		}
	}

	// Server mode: use relaxed validation (no codebase config required)
	cfg, err := config.LoadFromFileForServer(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Start Prometheus metrics server in background if enabled
	if cfg.Metrics.Enabled {
		go func() {
			fmt.Fprintf(os.Stderr, "[metrics] serving on :%d/metrics\n", cfg.Metrics.Port)
			if err := metrics.StartHTTPServer(cfg.Metrics.Port); err != nil {
				fmt.Fprintf(os.Stderr, "[metrics] server error: %v\n", err)
			}
		}()
	}

	server := mcp.NewServer(cfg)

	switch *transport {
	case "stdio":
		if err := server.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
			os.Exit(1)
		}
	case "http":
		if err := server.RunHTTP(*addr); err != nil {
			fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown transport: %s (use 'stdio' or 'http')\n", *transport)
		os.Exit(1)
	}
}
