package mcp

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/AustinSchoen/codebase-intel/internal/pipeline"
)

// fileUpload is a single file in a /mcp/indexer/files batch.
//
// Content is base64-encoded so binary-looking source (rare but possible) and
// embedded NUL bytes survive JSON encoding intact. Daemons that already have
// a UTF-8 string can use stdlib base64 to encode without copying.
type fileUpload struct {
	Filepath string `json:"filepath"`
	Content  string `json:"content"` // base64
}

// indexFilesRequest is the body of POST /mcp/indexer/files.
type indexFilesRequest struct {
	Codebase    string       `json:"codebase"`
	RequestID   string       `json:"request_id"`
	Incremental bool         `json:"incremental"`
	Final       bool         `json:"final"` // is this the last batch in the request?
	DisplayName string       `json:"display_name,omitempty"`
	RootPath    string       `json:"root_path,omitempty"` // informational, recorded in the codebases row
	Files       []fileUpload `json:"files"`
}

type indexFilesResponse struct {
	Indexed   int    `json:"indexed"`   // files newly written
	Unchanged int    `json:"unchanged"` // files skipped via content-hash dedup
	Failed    int    `json:"failed"`    // files where IndexFile errored
	Finalized bool   `json:"finalized"` // true if relationship resolution ran on this request
	Error     string `json:"error,omitempty"`
}

// decodeIndexerRequest performs the boilerplate shared by every /mcp/indexer/*
// POST handler: bearer-token auth, method check, pipeline-readiness check,
// JSON body decode. On any failure it writes the appropriate HTTP error and
// returns false; the caller should bail.
//
// The caller still validates request-specific fields (e.g. that Codebase is
// non-empty), because those vary per endpoint.
func (t *HTTPTransport) decodeIndexerRequest(w http.ResponseWriter, r *http.Request, req interface{}) bool {
	if !t.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if t.server.pipeline == nil {
		writeHTTPError(w, http.StatusServiceUnavailable, "indexing pipeline not initialized")
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		writeHTTPError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// handleIndexerFiles accepts a batch of file uploads from an indexer daemon
// and runs them through the pipeline. Raw relationships are buffered per
// request_id so that cross-file rels emitted in earlier batches can resolve
// against symbols added in later batches. When the daemon signals final=true
// the buffer drains and gets stored.
func (t *HTTPTransport) handleIndexerFiles(w http.ResponseWriter, r *http.Request) {
	var req indexFilesRequest
	if !t.decodeIndexerRequest(w, r, &req) {
		return
	}
	if req.Codebase == "" || req.RequestID == "" {
		writeHTTPError(w, http.StatusBadRequest, "codebase and request_id are required")
		return
	}

	// Ensure the codebase row exists so symbols/relationships can FK to it.
	if err := t.server.pipeline.EnsureCodebase(r.Context(), req.Codebase, req.DisplayName, req.RootPath); err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "ensuring codebase: "+err.Error())
		return
	}

	buf := t.relBufferFor(req.RequestID)

	resp := indexFilesResponse{}
	ctx := r.Context()
	for _, f := range req.Files {
		content, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			t.server.logger.Printf("indexer-files: bad base64 in %s/%s: %v", req.Codebase, f.Filepath, err)
			resp.Failed++
			continue
		}
		changed, rels, err := t.server.pipeline.IndexFile(ctx, pipeline.FileInput{
			Codebase: req.Codebase,
			Filepath: f.Filepath,
			Content:  content,
		}, req.Incremental)
		if err != nil {
			t.server.logger.Printf("indexer-files: pipeline error %s/%s: %v", req.Codebase, f.Filepath, err)
			resp.Failed++
			continue
		}
		if changed {
			resp.Indexed++
			buf.Append(rels)
		} else {
			resp.Unchanged++
		}
	}

	if req.Final {
		raw := buf.Drain()
		t.dropRelBuffer(req.RequestID)
		if err := t.server.pipeline.FinalizeRelationships(ctx, req.Codebase, raw); err != nil {
			t.server.logger.Printf("indexer-files: finalize relationships failed for %s: %v", req.Codebase, err)
			resp.Error = "finalize relationships: " + err.Error()
		}
		resp.Finalized = true
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// gcRequest is the body of POST /mcp/indexer/gc.
type gcRequest struct {
	Codebase  string   `json:"codebase"`
	KeepFiles []string `json:"keep_files"` // paths still present on the indexer host
}

// handleIndexerGC removes file_state rows (and cascades) for files no longer
// present on the indexer host. Daemons walk their tree, send the current list,
// and the server drops anything else.
func (t *HTTPTransport) handleIndexerGC(w http.ResponseWriter, r *http.Request) {
	var req gcRequest
	if !t.decodeIndexerRequest(w, r, &req) {
		return
	}
	if req.Codebase == "" {
		writeHTTPError(w, http.StatusBadRequest, "codebase is required")
		return
	}

	if err := t.server.pipeline.GC(r.Context(), req.Codebase, req.KeepFiles); err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "gc: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// deleteRequest is the body of POST /mcp/indexer/delete.
type deleteRequest struct {
	Codebase string   `json:"codebase"`
	Files    []string `json:"files"`
}

// handleIndexerDelete removes a single file (or a few) — used by watcher
// events for file deletions. For bulk reconciliation, use /mcp/indexer/gc.
func (t *HTTPTransport) handleIndexerDelete(w http.ResponseWriter, r *http.Request) {
	var req deleteRequest
	if !t.decodeIndexerRequest(w, r, &req) {
		return
	}
	if req.Codebase == "" {
		writeHTTPError(w, http.StatusBadRequest, "codebase is required")
		return
	}

	ctx := r.Context()
	failed := 0
	for _, f := range req.Files {
		if err := t.server.pipeline.DeleteFile(ctx, req.Codebase, f); err != nil {
			t.server.logger.Printf("indexer-delete: failed for %s/%s: %v", req.Codebase, f, err)
			failed++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted": len(req.Files) - failed,
		"failed":  failed,
	})
}

// relBufferFor returns the per-request_id relationship buffer, creating one
// if this is the first batch in the request.
func (t *HTTPTransport) relBufferFor(requestID string) *pipeline.RelBuffer {
	t.server.indexerRelBuffersMu.Lock()
	defer t.server.indexerRelBuffersMu.Unlock()
	if buf, ok := t.server.indexerRelBuffers[requestID]; ok {
		return buf
	}
	buf := pipeline.NewRelBuffer()
	t.server.indexerRelBuffers[requestID] = buf
	return buf
}

func (t *HTTPTransport) dropRelBuffer(requestID string) {
	t.server.indexerRelBuffersMu.Lock()
	defer t.server.indexerRelBuffersMu.Unlock()
	delete(t.server.indexerRelBuffers, requestID)
}

