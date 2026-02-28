package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

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
	server   *Server
	apiKey   string // optional bearer token
	sessions sync.Map
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

	t := &HTTPTransport{server: s}

	// Resolve optional API key for bearer auth
	if s.cfg.Server.APIKeyEnv != "" {
		t.apiKey = os.Getenv(s.cfg.Server.APIKeyEnv)
		if t.apiKey == "" {
			s.logger.Printf("warning: server.api_key_env set to %q but env var is empty, auth disabled", s.cfg.Server.APIKeyEnv)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", t.handleHealth)
	mux.HandleFunc("/mcp", t.handleMCP)

	s.logger.Printf("MCP HTTP server listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

// handleHealth returns a simple health check response.
func (t *HTTPTransport) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
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

// updateIndexMetrics queries backends and sets Prometheus index size gauges.
func (s *Server) updateIndexMetrics(ctx context.Context) {
	if s.store != nil {
		symbols, files, relationships, err := s.store.GetIndexCounts(ctx, s.cfg.Codebase.Name)
		if err != nil {
			s.logger.Printf("warning: failed to get index counts: %v", err)
		} else {
			metrics.SetIndexSize("symbols", float64(symbols))
			metrics.SetIndexSize("files", float64(files))
			metrics.SetIndexSize("relationships", float64(relationships))
		}
	}

	if s.qdrant != nil {
		count, err := s.qdrant.CountPoints(ctx, s.cfg.Codebase.Name)
		if err != nil {
			s.logger.Printf("warning: failed to get qdrant count: %v", err)
		} else {
			metrics.SetIndexSize("chunks", float64(count))
		}
	}
}
