package indexer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AustinSchoen/codebase-intel/internal/metrics"
	"github.com/fsnotify/fsnotify"
)

// DaemonConfig holds daemon-level configuration shared across codebases.
type DaemonConfig struct {
	ServerURL string
	ServerKey string
	NodeID    string
}

// Daemon runs N indexers in a single process, watching each codebase for
// changes and sharing one SSE connection with the MCP server. The original
// design (commit 7391b2f) was per-machine — one daemon, many codebases — but
// the daemon was initially implemented one-codebase-per-process. This
// restores the intended behavior; see issue #3.
type Daemon struct {
	cfg    DaemonConfig
	logger interface{ Printf(string, ...interface{}) }

	// clients maps codebase name -> *Indexer. Codebase names must be unique;
	// the constructor of the daemon (cmd/indexer/main.go) enforces this.
	clients map[string]*Client
}

// NewDaemon creates a daemon that serves the given indexers. The map key is
// expected to match each indexer's cfg.Codebase.Name; the caller guarantees
// no duplicates.
func NewDaemon(clients map[string]*Client, cfg DaemonConfig) *Daemon {
	// Pick any indexer's logger — they all use the package default.
	var logger interface{ Printf(string, ...interface{}) }
	for _, c := range clients {
		logger = c.logger
		break
	}
	return &Daemon{
		cfg:     cfg,
		logger:  logger,
		clients: clients,
	}
}

// codebases returns the sorted list of codebase names served by this daemon.
// Sorted so registration log lines and the SSE URL are deterministic.
func (d *Daemon) codebases() []string {
	names := make([]string, 0, len(d.clients))
	for name := range d.clients {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Run starts the daemon: runs an initial index for each codebase, spawns one
// file watcher per codebase, and opens a single SSE connection that carries
// commands for every codebase this daemon serves.
func (d *Daemon) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		d.logger.Printf("received signal %v, shutting down", sig)
		cancel()
	}()

	// Run initial index for each codebase. Sequential to limit concurrent
	// load on Voyage/Qdrant; multi-codebase indexing is rare enough that
	// going parallel isn't worth the rate-limit risk.
	for _, name := range d.codebases() {
		client := d.clients[name]
		d.logger.Printf("running initial index for codebase: %s", name)
		if err := client.FullIndex(ctx); err != nil {
			d.logger.Printf("warning: initial index for %s failed: %v", name, err)
		}
	}

	// Start one file watcher per codebase.
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	for _, name := range d.codebases() {
		cw := newCodebaseWatcher(d, name, d.clients[name])
		go cw.run(watchCtx)
	}

	// Connect to MCP server SSE stream and listen for commands.
	d.logger.Printf("connecting to MCP server at %s (codebases: %v)", d.cfg.ServerURL, d.codebases())
	return d.connectAndListen(ctx)
}

// connectAndListen connects to the MCP server SSE endpoint and listens for
// commands. Reconnects automatically on disconnection.
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

// sseConnect opens a single SSE connection registering all codebases this
// daemon serves. The codebase query parameter is repeated once per codebase;
// the server reads them as a slice via r.URL.Query()["codebase"].
func (d *Daemon) sseConnect(ctx context.Context) error {
	q := url.Values{}
	q.Set("node_id", d.cfg.NodeID)
	for _, name := range d.codebases() {
		q.Add("codebase", name)
	}
	endpoint := strings.TrimRight(d.cfg.ServerURL, "/") + "/mcp/indexer?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
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

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
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
		d.dispatchReindex(ctx, event.Codebase, event.Full, event.RequestID)
	default:
		d.logger.Printf("unknown SSE event type: %s", event.Type)
	}
}

// dispatchReindex routes a reindex command to the indexer for the named
// codebase. Returns false if no such codebase is served by this daemon.
// Each per-codebase reindex runs in its own goroutine; the reindex itself
// serializes via the indexer's reindexMu so two concurrent commands for the
// same codebase don't race.
func (d *Daemon) dispatchReindex(ctx context.Context, codebase string, full bool, requestID string) bool {
	client, ok := d.clients[codebase]
	if !ok {
		d.logger.Printf("warning: reindex requested for unknown codebase %q (daemon serves %v)",
			codebase, d.codebases())
		return false
	}
	go d.handleReindex(ctx, client, full, requestID)
	return true
}

// handleReindex performs a reindex via the given client and reports progress.
// Serialized per client via client.reindexMu so a watcher-triggered reindex and
// an MCP-triggered one for the same codebase don't race.
func (d *Daemon) handleReindex(ctx context.Context, client *Client, full bool, requestID string) {
	client.reindexMu.Lock()
	defer client.reindexMu.Unlock()

	codebase := client.cfg.Codebase.Name

	prevIncremental := client.cfg.Indexing.Incremental
	if full {
		client.cfg.Indexing.Incremental = false
	}

	d.reportStatus(codebase, requestID, "started", 0, 0, 0, 0, 0, "")

	startTime := time.Now()
	err := client.FullIndex(ctx)
	durationMs := time.Since(startTime).Milliseconds()

	client.cfg.Indexing.Incremental = prevIncremental

	if err != nil {
		d.logger.Printf("reindex error for %s: %v", codebase, err)
		d.reportStatus(codebase, requestID, "error", 0, 0, 0, 0, durationMs, err.Error())
	} else {
		d.logger.Printf("reindex of %s complete in %dms", codebase, durationMs)
		d.reportStatus(codebase, requestID, "complete", 0, 0, 0, 0, durationMs, "")
	}
}

// reportStatus sends a progress update for one codebase to the MCP server.
func (d *Daemon) reportStatus(codebase, requestID, status string, filesTotal, filesProcessed, filesIndexed, filesSkipped int, durationMs int64, errMsg string) {
	report := map[string]interface{}{
		"request_id":      requestID,
		"node_id":         d.cfg.NodeID,
		"codebase":        codebase,
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

	endpoint := strings.TrimRight(d.cfg.ServerURL, "/") + "/mcp/indexer/status"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
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

// codebaseWatcher owns the fsnotify watcher and pending-event state for a
// single codebase. Previously this state lived on Daemon, which forced the
// daemon to be single-codebase. Splitting it lets a daemon process serve
// many codebases independently — each with its own debounce timer, batch
// buffer, and per-codebase reindex serialization (the indexer's reindexMu).
type codebaseWatcher struct {
	d        *Daemon
	codebase string
	client   *Client

	mu            sync.Mutex
	pendingFiles  map[string]fsnotify.Op
	debounceTimer *time.Timer
}

func newCodebaseWatcher(d *Daemon, codebase string, client *Client) *codebaseWatcher {
	return &codebaseWatcher{
		d:            d,
		codebase:     codebase,
		client:       client,
		pendingFiles: make(map[string]fsnotify.Op),
	}
}

func (cw *codebaseWatcher) run(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		cw.d.logger.Printf("failed to create file watcher for %s: %v", cw.codebase, err)
		return
	}
	defer watcher.Close()

	sw := &smartWatcher{client: cw.client, watcher: watcher, pending: make(map[string]fsnotify.Op), windowStart: time.Now()}
	if err := sw.addDirectories(); err != nil {
		cw.d.logger.Printf("failed to add watch directories for %s: %v", cw.codebase, err)
		return
	}

	cw.d.logger.Printf("file watcher started for %s (%s)", cw.codebase, cw.client.cfg.Codebase.Path)

	const debounceDelay = 3 * time.Second

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

			metrics.RecordFileWatchEvent(cw.codebase)

			cw.mu.Lock()
			cw.pendingFiles[event.Name] |= event.Op
			if cw.debounceTimer != nil {
				cw.debounceTimer.Stop()
			}
			cw.debounceTimer = time.AfterFunc(debounceDelay, func() {
				cw.processBatch(ctx)
			})
			cw.mu.Unlock()

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			cw.d.logger.Printf("file watcher error for %s: %v", cw.codebase, err)
		}
	}
}

// processBatch processes accumulated file watch events for this codebase.
func (cw *codebaseWatcher) processBatch(ctx context.Context) {
	cw.mu.Lock()
	if len(cw.pendingFiles) == 0 {
		cw.mu.Unlock()
		return
	}
	batch := cw.pendingFiles
	cw.pendingFiles = make(map[string]fsnotify.Op)
	cw.mu.Unlock()

	// Serialize with MCP-triggered reindexes for the same indexer.
	cw.client.reindexMu.Lock()
	defer cw.client.reindexMu.Unlock()

	cw.d.logger.Printf("processing %d file change events for %s", len(batch), cw.codebase)

	requestID := fmt.Sprintf("watch-%d", time.Now().UnixMilli())
	cw.d.reportStatus(cw.codebase, requestID, "started", len(batch), 0, 0, 0, 0, "")

	startTime := time.Now()
	var indexed int

	for path, op := range batch {
		if op&(fsnotify.Remove|fsnotify.Rename) != 0 {
			if err := cw.client.DeleteFile(ctx, path); err != nil {
				cw.d.logger.Printf("error deleting %s on server: %v", path, err)
			}
			continue
		}

		if op&(fsnotify.Write|fsnotify.Create) != 0 {
			lang := cw.client.DetectLanguage(path)
			if lang != "" {
				if err := cw.client.IndexSingleFile(ctx, path); err != nil {
					cw.d.logger.Printf("error uploading %s: %v", path, err)
				} else {
					indexed++
				}
			}
		}
	}

	durationMs := time.Since(startTime).Milliseconds()
	cw.d.logger.Printf("watch batch for %s complete: %d uploaded in %dms",
		cw.codebase, indexed, durationMs)
	cw.d.reportStatus(cw.codebase, requestID, "complete", len(batch), indexed, indexed, 0, durationMs, "")
}
