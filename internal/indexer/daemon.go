package indexer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AustinSchoen/codebase-intel/internal/metrics"
	"github.com/fsnotify/fsnotify"
)

// DaemonConfig holds daemon-specific configuration.
type DaemonConfig struct {
	ServerURL string
	ServerKey string
	NodeID    string
}

// Daemon runs the indexer in daemon mode with file watching and MCP server registration.
type Daemon struct {
	idx    *Indexer
	cfg    DaemonConfig
	logger interface{ Printf(string, ...interface{}) }

	mu            sync.Mutex
	pendingFiles  map[string]fsnotify.Op
	debounceTimer *time.Timer
	reindexMu     sync.Mutex // serializes reindex operations
}

// NewDaemon creates a new daemon wrapping an existing indexer.
func NewDaemon(idx *Indexer, cfg DaemonConfig) *Daemon {
	return &Daemon{
		idx:          idx,
		cfg:          cfg,
		logger:       idx.logger,
		pendingFiles: make(map[string]fsnotify.Op),
	}
}

// Run starts the daemon: connects to server, watches files, handles commands.
func (d *Daemon) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		d.logger.Printf("received signal %v, shutting down", sig)
		cancel()
	}()

	// Run initial index
	d.logger.Printf("running initial index for codebase: %s", d.idx.cfg.Codebase.Name)
	if err := d.idx.fullIndex(ctx); err != nil {
		d.logger.Printf("warning: initial index failed: %v", err)
	}

	// Start file watcher in background
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	go d.runFileWatcher(watchCtx)

	// Connect to MCP server SSE stream and listen for commands
	d.logger.Printf("connecting to MCP server at %s", d.cfg.ServerURL)
	return d.connectAndListen(ctx)
}

// connectAndListen connects to the MCP server SSE endpoint and listens for commands.
// Reconnects automatically on disconnection.
func (d *Daemon) connectAndListen(ctx context.Context) error {
	backoff := time.Second
	maxBackoff := 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := d.sseConnect(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err != nil {
			d.logger.Printf("SSE connection error: %v, reconnecting in %v", err, backoff)
		} else {
			d.logger.Printf("SSE connection closed, reconnecting in %v", backoff)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// sseConnect opens a single SSE connection to the MCP server.
func (d *Daemon) sseConnect(ctx context.Context) error {
	url := strings.TrimRight(d.cfg.ServerURL, "/") + "/mcp/indexer" +
		"?node_id=" + d.cfg.NodeID +
		"&codebase=" + d.idx.cfg.Codebase.Name

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if d.cfg.ServerKey != "" {
		req.Header.Set("Authorization", "Bearer "+d.cfg.ServerKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	d.logger.Printf("connected to MCP server as node %s", d.cfg.NodeID)

	// Read SSE events
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()

		// SSE data lines start with "data: "
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		d.handleSSEEvent(ctx, []byte(data))
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading SSE: %w", err)
	}

	return nil
}

// handleSSEEvent processes a single SSE event from the MCP server.
func (d *Daemon) handleSSEEvent(ctx context.Context, data []byte) {
	var event struct {
		Type      string `json:"type"`
		Codebase  string `json:"codebase"`
		RequestID string `json:"request_id"`
		Full      bool   `json:"full"`
		NodeID    string `json:"node_id"`
	}

	if err := json.Unmarshal(data, &event); err != nil {
		d.logger.Printf("invalid SSE event: %v", err)
		return
	}

	switch event.Type {
	case "registered":
		d.logger.Printf("registration confirmed by server (node_id: %s)", event.NodeID)
	case "reindex":
		d.logger.Printf("received reindex command: codebase=%s full=%v request_id=%s",
			event.Codebase, event.Full, event.RequestID)
		go d.handleReindex(ctx, event.Codebase, event.Full, event.RequestID)
	default:
		d.logger.Printf("unknown SSE event type: %s", event.Type)
	}
}

// handleReindex performs a reindex and reports progress to the MCP server.
// Serialized by reindexMu to prevent concurrent reindex operations from
// racing on shared indexer config.
func (d *Daemon) handleReindex(ctx context.Context, codebase string, full bool, requestID string) {
	d.reindexMu.Lock()
	defer d.reindexMu.Unlock()

	prevIncremental := d.idx.cfg.Indexing.Incremental
	if full {
		d.idx.cfg.Indexing.Incremental = false
	}

	// Report started
	d.reportStatus(requestID, "started", 0, 0, 0, 0, 0, "")

	startTime := time.Now()
	err := d.idx.fullIndex(ctx)
	durationMs := time.Since(startTime).Milliseconds()

	// Restore original incremental mode
	d.idx.cfg.Indexing.Incremental = prevIncremental

	if err != nil {
		d.logger.Printf("reindex error: %v", err)
		d.reportStatus(requestID, "error", 0, 0, 0, 0, durationMs, err.Error())
	} else {
		d.logger.Printf("reindex complete in %dms", durationMs)
		d.reportStatus(requestID, "complete", 0, 0, 0, 0, durationMs, "")
	}
}

// reportStatus sends a progress update to the MCP server.
func (d *Daemon) reportStatus(requestID, status string, filesTotal, filesProcessed, filesIndexed, filesSkipped int, durationMs int64, errMsg string) {
	report := map[string]interface{}{
		"request_id":      requestID,
		"node_id":         d.cfg.NodeID,
		"codebase":        d.idx.cfg.Codebase.Name,
		"status":          status,
		"files_total":     filesTotal,
		"files_processed": filesProcessed,
		"files_indexed":   filesIndexed,
		"files_skipped":   filesSkipped,
		"duration_ms":     durationMs,
	}
	if errMsg != "" {
		report["error"] = errMsg
	}

	body, _ := json.Marshal(report)

	url := strings.TrimRight(d.cfg.ServerURL, "/") + "/mcp/indexer/status"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		d.logger.Printf("failed to create status request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if d.cfg.ServerKey != "" {
		req.Header.Set("Authorization", "Bearer "+d.cfg.ServerKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.logger.Printf("failed to report status: %v", err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		d.logger.Printf("status report returned %d", resp.StatusCode)
	}
}

// runFileWatcher watches the codebase directory for changes and triggers incremental reindex.
func (d *Daemon) runFileWatcher(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		d.logger.Printf("failed to create file watcher: %v", err)
		return
	}
	defer watcher.Close()

	// Add directories to watch
	sw := &smartWatcher{idx: d.idx, watcher: watcher, pending: make(map[string]fsnotify.Op), windowStart: time.Now()}
	if err := sw.addDirectories(); err != nil {
		d.logger.Printf("failed to add watch directories: %v", err)
		return
	}

	d.logger.Printf("file watcher started for %s", d.idx.cfg.Codebase.Path)

	debounceDelay := 3 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}

			metrics.RecordFileWatchEvent(d.idx.cfg.Codebase.Name)

			d.mu.Lock()
			d.pendingFiles[event.Name] |= event.Op

			// Reset debounce timer
			if d.debounceTimer != nil {
				d.debounceTimer.Stop()
			}
			d.debounceTimer = time.AfterFunc(debounceDelay, func() {
				d.processWatchBatch(ctx)
			})
			d.mu.Unlock()

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			d.logger.Printf("file watcher error: %v", err)
		}
	}
}

// processWatchBatch processes accumulated file watch events.
func (d *Daemon) processWatchBatch(ctx context.Context) {
	d.mu.Lock()
	if len(d.pendingFiles) == 0 {
		d.mu.Unlock()
		return
	}
	batch := d.pendingFiles
	d.pendingFiles = make(map[string]fsnotify.Op)
	d.mu.Unlock()

	d.logger.Printf("processing %d file change events", len(batch))

	requestID := fmt.Sprintf("watch-%d", time.Now().UnixMilli())
	d.reportStatus(requestID, "started", len(batch), 0, 0, 0, 0, "")

	startTime := time.Now()
	var indexed, skipped int

	for path, op := range batch {
		if op&(fsnotify.Remove|fsnotify.Rename) != 0 {
			relPath, _ := d.idx.relPath(path)
			if err := d.idx.store.DeleteFileData(ctx, d.idx.cfg.Codebase.Name, relPath); err != nil {
				d.logger.Printf("error cleaning up %s: %v", relPath, err)
			}
			if err := d.idx.qdrant.DeleteByFilter(ctx, d.idx.cfg.Codebase.Name, relPath); err != nil {
				d.logger.Printf("error deleting vectors for %s: %v", relPath, err)
			}
			continue
		}

		if op&(fsnotify.Write|fsnotify.Create) != 0 {
			lang := d.idx.detectLanguage(path)
			if lang != "" {
				changed, _, err := d.idx.indexFile(ctx, path)
				if err != nil {
					d.logger.Printf("error re-indexing %s: %v", path, err)
				} else if changed {
					indexed++
				} else {
					skipped++
				}
			}
		}
	}

	durationMs := time.Since(startTime).Milliseconds()
	d.logger.Printf("watch batch complete: %d indexed, %d skipped in %dms", indexed, skipped, durationMs)
	d.reportStatus(requestID, "complete", len(batch), indexed+skipped, indexed, skipped, durationMs, "")
}
