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
	flag.Parse()

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
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
