package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AustinSchoen/codebase-intel/internal/discovery"
	"github.com/AustinSchoen/codebase-intel/internal/metrics"
)

// session represents a single MCP client session over HTTP.
type session struct {
	id      string
	created time.Time

	mu         sync.Mutex
	sseClients []chan []byte // SSE notification channels
}

// HTTPTransport serves the MCP protocol over Streamable HTTP (2025-03-26 spec).
type HTTPTransport struct {
	server     *Server
	apiKey     string // optional bearer token
	sessions   sync.Map
	indexerMgr *IndexerManager
}

// RunHTTP starts the MCP server with HTTP/SSE transport.
func (s *Server) RunHTTP(addr string) error {
	ctx := context.Background()

	if err := s.initBackends(ctx); err != nil {
		return fmt.Errorf("init backends: %w", err)
	}
	if s.store != nil {
		defer s.store.Close()
	}

	// Populate index size metrics on startup and periodically
	s.updateIndexMetrics(ctx)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.updateIndexMetrics(ctx)
		}
	}()

	indexerMgr := NewIndexerManager(s.logger)
	t := &HTTPTransport{server: s, indexerMgr: indexerMgr}

	// Make indexer manager available to MCP tools
	s.indexerMgr = indexerMgr

	// Resolve optional API key for bearer auth
	if s.cfg.Server.APIKeyEnv != "" {
		t.apiKey = os.Getenv(s.cfg.Server.APIKeyEnv)
		if t.apiKey == "" {
			s.logger.Printf("warning: server.api_key_env set to %q but env var is empty, auth disabled", s.cfg.Server.APIKeyEnv)
		}
	}

	// Publish ourselves on mDNS so indexer hosts on the same LAN can find
	// us without manual URL/token entry. Disabled via server.discovery.advertise
	// in config; token advertising disabled via server.discovery.advertise_token.
	if s.cfg.Server.Discovery.AdvertiseEnabled() {
		if adv, err := startDiscoveryAdvertise(addr, t.apiKey, s.cfg.Server.Discovery.TokenAdvertiseEnabled(), s.logger); err != nil {
			s.logger.Printf("warning: mDNS advertise failed (continuing without discovery): %v", err)
		} else if adv != nil {
			defer adv.Shutdown()
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", t.handleHealth)
	mux.HandleFunc("/ready", t.handleReady)
	mux.HandleFunc("/mcp", t.handleMCP)
	mux.HandleFunc("/mcp/indexer", t.handleIndexer)
	mux.HandleFunc("/mcp/indexer/status", t.handleIndexerStatus)
	mux.HandleFunc("/mcp/indexer/nodes", t.handleIndexerNodes)
	// Thin-client indexer endpoints (issue #18). Daemons POST file content;
	// server runs the pipeline.
	mux.HandleFunc("/mcp/indexer/files", t.handleIndexerFiles)
	mux.HandleFunc("/mcp/indexer/gc", t.handleIndexerGC)
	mux.HandleFunc("/mcp/indexer/delete", t.handleIndexerDelete)

	// Dashboard and API routes (no auth required)
	t.registerDashboardRoutes(mux)

	s.logger.Printf("MCP HTTP server listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

// handleHealth is a liveness probe: 200 as long as the HTTP server is up.
// It does not check backend connectivity — use /ready for that.
func (t *HTTPTransport) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// handleReady is a readiness probe: 200 only if the Postgres metadata store
// and the Qdrant vector store are both reachable right now. Returns 503 with
// a per-backend status object otherwise. Use this for orchestrator readiness
// gates and load-balancer health checks; use /health for liveness.
func (t *HTTPTransport) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	type backendStatus struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	result := struct {
		Ready    bool                     `json:"ready"`
		Backends map[string]backendStatus `json:"backends"`
	}{
		Backends: map[string]backendStatus{},
	}

	postgresOK := false
	if t.server.store != nil {
		if err := t.server.store.Ping(ctx); err != nil {
			result.Backends["postgres"] = backendStatus{OK: false, Error: err.Error()}
		} else {
			result.Backends["postgres"] = backendStatus{OK: true}
			postgresOK = true
		}
	} else {
		result.Backends["postgres"] = backendStatus{OK: false, Error: "store not initialized"}
	}

	qdrantOK := false
	if t.server.qdrant != nil {
		if err := t.server.qdrant.Healthz(ctx); err != nil {
			result.Backends["qdrant"] = backendStatus{OK: false, Error: err.Error()}
		} else {
			result.Backends["qdrant"] = backendStatus{OK: true}
			qdrantOK = true
		}
	} else {
		result.Backends["qdrant"] = backendStatus{OK: false, Error: "qdrant client not initialized"}
	}

	result.Ready = postgresOK && qdrantOK

	w.Header().Set("Content-Type", "application/json")
	if !result.Ready {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(result)
}

// handleMCP dispatches to POST (JSON-RPC) or GET (SSE) handlers.
func (t *HTTPTransport) handleMCP(w http.ResponseWriter, r *http.Request) {
	// Auth check for all /mcp requests
	if !t.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodPost:
		t.handlePost(w, r)
	case http.MethodGet:
		t.handleSSE(w, r)
	case http.MethodDelete:
		t.handleDeleteSession(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// checkAuth validates the bearer token if auth is configured.
func (t *HTTPTransport) checkAuth(r *http.Request) bool {
	if t.apiKey == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	return auth == "Bearer "+t.apiKey
}

// handlePost processes a JSON-RPC request and returns a JSON-RPC response.
func (t *HTTPTransport) handlePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MB limit
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var req jsonrpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := &jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      nil,
			Error:   &rpcError{Code: -32700, Message: "Parse error", Data: err.Error()},
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Session management
	sessionID := r.Header.Get("Mcp-Session-Id")

	if req.Method == "initialize" {
		// Create a new session
		sess := t.createSession()
		ctx := context.Background()
		resp := t.server.handleRequest(ctx, &req)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", sess.id)
		if resp != nil {
			json.NewEncoder(w).Encode(resp)
		}
		return
	}

	// For non-initialize requests, require a valid session
	if sessionID == "" {
		writeHTTPError(w, http.StatusBadRequest, "missing Mcp-Session-Id header")
		return
	}

	sess := t.getSession(sessionID)
	if sess == nil {
		writeHTTPError(w, http.StatusNotFound, "unknown session")
		return
	}

	// Notifications don't get a response
	if req.Method == "notifications/initialized" {
		resp := t.server.handleRequest(context.Background(), &req)
		w.Header().Set("Mcp-Session-Id", sess.id)
		if resp != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		} else {
			w.WriteHeader(http.StatusAccepted)
		}
		return
	}

	ctx := context.Background()
	resp := t.server.handleRequest(ctx, &req)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Mcp-Session-Id", sess.id)
	if resp != nil {
		json.NewEncoder(w).Encode(resp)
	}
}

// handleSSE opens an SSE stream for server-initiated notifications.
func (t *HTTPTransport) handleSSE(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		writeHTTPError(w, http.StatusBadRequest, "missing Mcp-Session-Id header")
		return
	}

	sess := t.getSession(sessionID)
	if sess == nil {
		writeHTTPError(w, http.StatusNotFound, "unknown session")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeHTTPError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Mcp-Session-Id", sess.id)

	// Register this SSE client
	ch := make(chan []byte, 64)
	sess.mu.Lock()
	sess.sseClients = append(sess.sseClients, ch)
	sess.mu.Unlock()

	defer func() {
		sess.mu.Lock()
		for i, c := range sess.sseClients {
			if c == ch {
				sess.sseClients = append(sess.sseClients[:i], sess.sseClients[i+1:]...)
				break
			}
		}
		sess.mu.Unlock()
		close(ch)
	}()

	// Send initial keepalive
	fmt.Fprintf(w, ": keepalive\n\n")
	flusher.Flush()

	// Heartbeat ticker
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// handleDeleteSession terminates a session.
func (t *HTTPTransport) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		writeHTTPError(w, http.StatusBadRequest, "missing Mcp-Session-Id header")
		return
	}

	if _, loaded := t.sessions.LoadAndDelete(sessionID); !loaded {
		writeHTTPError(w, http.StatusNotFound, "unknown session")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (t *HTTPTransport) createSession() *session {
	id := generateSessionID()
	sess := &session{
		id:      id,
		created: time.Now(),
	}
	t.sessions.Store(id, sess)
	return sess
}

func (t *HTTPTransport) getSession(id string) *session {
	v, ok := t.sessions.Load(id)
	if !ok {
		return nil
	}
	return v.(*session)
}

func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use timestamp (should never happen)
		return fmt.Sprintf("sess-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func writeHTTPError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// sendNotification sends a JSON-RPC notification to all SSE clients of a session.
func (t *HTTPTransport) sendNotification(sessionID string, method string, params interface{}) {
	sess := t.getSession(sessionID)
	if sess == nil {
		return
	}

	notification := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		notification["params"] = params
	}

	data, err := json.Marshal(notification)
	if err != nil {
		return
	}

	// Escape newlines in data for SSE format
	escaped := strings.ReplaceAll(string(data), "\n", "")

	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, ch := range sess.sseClients {
		select {
		case ch <- []byte(escaped):
		default:
			// Drop if channel is full (slow client)
		}
	}
}

// handleIndexer is the SSE endpoint for indexer daemons to connect and receive commands.
func (t *HTTPTransport) handleIndexer(w http.ResponseWriter, r *http.Request) {
	if !t.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeHTTPError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Parse registration from query parameters. `codebase` may be repeated
	// (e.g. `?codebase=a&codebase=b`) so a single daemon process can serve
	// multiple codebases on one host. Single-codebase clients still work
	// unchanged since one `?codebase=foo` produces a 1-element slice.
	q := r.URL.Query()
	nodeID := q.Get("node_id")
	codebases := q["codebase"]
	if nodeID == "" || len(codebases) == 0 {
		writeHTTPError(w, http.StatusBadRequest, "node_id and at least one codebase query parameter required")
		return
	}

	// Create SSE channel for this indexer
	sseChan := make(chan []byte, 64)
	t.indexerMgr.RegisterNode(nodeID, codebases, sseChan)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send initial registration acknowledgement
	ack, _ := json.Marshal(map[string]interface{}{
		"type":    "registered",
		"node_id": nodeID,
	})
	fmt.Fprintf(w, "data: %s\n\n", ack)
	flusher.Flush()

	// Heartbeat ticker
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	defer t.indexerMgr.DeregisterNode(nodeID)

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sseChan:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// handleIndexerStatus receives progress updates from indexer daemons.
func (t *HTTPTransport) handleIndexerStatus(w http.ResponseWriter, r *http.Request) {
	if !t.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var report struct {
		RequestID      string `json:"request_id"`
		NodeID         string `json:"node_id"`
		Codebase       string `json:"codebase"`
		Status         string `json:"status"`
		FilesTotal     int    `json:"files_total"`
		FilesProcessed int    `json:"files_processed"`
		FilesIndexed   int    `json:"files_indexed"`
		FilesSkipped   int    `json:"files_skipped"`
		DurationMs     int64  `json:"duration_ms"`
		Error          string `json:"error"`
	}

	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&report); err != nil {
		writeHTTPError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	defer r.Body.Close()

	if report.RequestID == "" || report.NodeID == "" {
		writeHTTPError(w, http.StatusBadRequest, "request_id and node_id required")
		return
	}

	t.indexerMgr.UpdateStatus(
		report.RequestID, report.NodeID, report.Codebase, report.Status,
		report.FilesTotal, report.FilesProcessed, report.FilesIndexed, report.FilesSkipped,
		report.DurationMs, report.Error,
	)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// handleIndexerNodes returns a list of connected indexer nodes and their status.
func (t *HTTPTransport) handleIndexerNodes(w http.ResponseWriter, r *http.Request) {
	if !t.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	nodes := t.indexerMgr.GetNodes()

	type nodeInfo struct {
		NodeID      string   `json:"node_id"`
		Codebases   []string `json:"codebases"`
		Status      string   `json:"status"`
		ConnectedAt string   `json:"connected_at"`
		LastSeen    string   `json:"last_seen"`
	}

	var out []nodeInfo
	for _, n := range nodes {
		out = append(out, nodeInfo{
			NodeID:      n.NodeID,
			Codebases:   n.Codebases,
			Status:      n.Status,
			ConnectedAt: n.ConnectedAt.Format(time.RFC3339),
			LastSeen:    n.LastSeen.Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"nodes": out})
}

// updateIndexMetrics queries backends and sets Prometheus index size gauges for all codebases.
func (s *Server) updateIndexMetrics(ctx context.Context) {
	if s.store == nil {
		return
	}

	codebases, err := s.store.ListCodebases(ctx)
	if err != nil {
		s.logger.Printf("warning: failed to list codebases for metrics: %v", err)
		return
	}

	for _, cb := range codebases {
		symbols, files, relationships, err := s.store.GetIndexCounts(ctx, cb.ID)
		if err != nil {
			s.logger.Printf("warning: failed to get index counts for %s: %v", cb.ID, err)
			continue
		}
		metrics.SetIndexSize("symbols", cb.ID, float64(symbols))
		metrics.SetIndexSize("files", cb.ID, float64(files))
		metrics.SetIndexSize("relationships", cb.ID, float64(relationships))

		if s.qdrant != nil {
			count, err := s.qdrant.CountPoints(ctx, cb.ID)
			if err != nil {
				s.logger.Printf("warning: failed to get qdrant count for %s: %v", cb.ID, err)
			} else {
				metrics.SetIndexSize("chunks", cb.ID, float64(count))
			}
		}
	}
}

// startDiscoveryAdvertise extracts the port from a listen address and starts
// publishing this server on mDNS. The bearer token is included in the TXT
// record when advertiseToken is true (allowing indexer hosts to auto-
// configure auth) or omitted when false (the operator must wire up the
// token manually). Returns nil, nil if discovery wasn't started but no error
// path was hit (e.g., addr couldn't be parsed and the caller already logged).
func startDiscoveryAdvertise(addr, token string, advertiseToken bool, logger *log.Logger) (discovery.Advertiser, error) {
	port, err := portFromAddr(addr)
	if err != nil {
		return nil, fmt.Errorf("extracting port from %q: %w", addr, err)
	}

	opts := discovery.AdvertiseOptions{Port: port}
	if advertiseToken {
		opts.Token = token
	}
	adv, err := discovery.Advertise(opts)
	if err != nil {
		return nil, err
	}

	if advertiseToken && token != "" {
		logger.Printf("mDNS: advertising on port %d with bearer token (set server.discovery.advertise_token: false to omit)", port)
	} else {
		logger.Printf("mDNS: advertising on port %d (no token in TXT record)", port)
	}
	return adv, nil
}

// portFromAddr parses the port out of a listen address. Handles ":8090",
// "0.0.0.0:8090", "192.168.1.10:8090", and "[::1]:8090".
func portFromAddr(addr string) (int, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(portStr)
}
