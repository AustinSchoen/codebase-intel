package mcp

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"sort"
	"time"
)

//go:embed dashboard
var dashboardFS embed.FS

// startTime records when the server started for uptime calculation.
var startTime = time.Now()

// registerDashboardRoutes adds dashboard and API routes to the mux.
func (t *HTTPTransport) registerDashboardRoutes(mux *http.ServeMux) {
	// API endpoints (no auth required)
	mux.HandleFunc("/api/codebases", t.handleAPICodebases)
	mux.HandleFunc("/api/indexers", t.handleAPIIndexers)
	mux.HandleFunc("/api/reindex-history", t.handleAPIReindexHistory)
	mux.HandleFunc("/api/stats", t.handleAPIStats)

	// Serve embedded dashboard files at root
	dashSub, err := fs.Sub(dashboardFS, "dashboard")
	if err != nil {
		t.server.logger.Printf("error: failed to create dashboard sub-fs: %v", err)
		return
	}
	fileServer := http.FileServer(http.FS(dashSub))
	mux.Handle("/dashboard/", http.StripPrefix("/dashboard/", fileServer))
	// Serve index.html at root
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/dashboard/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
}

func (t *HTTPTransport) handleAPICodebases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Check if a specific codebase is requested via query param
	name := r.URL.Query().Get("name")
	if name != "" {
		t.handleAPICodebaseDetail(w, r, ctx, name)
		return
	}

	if t.server.store == nil {
		writeJSON(w, map[string]interface{}{"codebases": []interface{}{}, "error": "database unavailable"})
		return
	}

	stats, err := t.server.store.GetAllCodebaseStats(ctx)
	if err != nil {
		writeJSON(w, map[string]interface{}{"codebases": []interface{}{}, "error": err.Error()})
		return
	}

	// Enrich with vector counts from Qdrant
	type codebaseResponse struct {
		ID                string  `json:"id"`
		DisplayName       string  `json:"display_name"`
		RootPath          string  `json:"root_path"`
		FileCount         int64   `json:"file_count"`
		SymbolCount       int64   `json:"symbol_count"`
		RelationshipCount int64   `json:"relationship_count"`
		ChunkCount        int64   `json:"chunk_count"`
		VectorCount       int64   `json:"vector_count"`
		LastIndexedAt     *string `json:"last_indexed_at"`
		IndexAgeSecs      float64 `json:"index_age_secs"`
	}

	var results []codebaseResponse
	for _, cs := range stats {
		cr := codebaseResponse{
			ID:                cs.ID,
			DisplayName:       cs.DisplayName,
			RootPath:          cs.RootPath,
			FileCount:         cs.FileCount,
			SymbolCount:       cs.SymbolCount,
			RelationshipCount: cs.RelationshipCount,
			ChunkCount:        cs.ChunkCount,
		}
		if cs.LastIndexedAt != nil {
			ts := cs.LastIndexedAt.Format(time.RFC3339)
			cr.LastIndexedAt = &ts
			cr.IndexAgeSecs = time.Since(*cs.LastIndexedAt).Seconds()
		}
		if t.server.qdrant != nil {
			count, err := t.server.qdrant.CountPoints(ctx, cs.ID)
			if err == nil {
				cr.VectorCount = count
			}
		}
		results = append(results, cr)
	}

	writeJSON(w, map[string]interface{}{"codebases": results})
}

func (t *HTTPTransport) handleAPICodebaseDetail(w http.ResponseWriter, r *http.Request, ctx context.Context, name string) {
	if t.server.store == nil {
		writeJSON(w, map[string]interface{}{"error": "database unavailable"})
		return
	}

	cs, err := t.server.store.GetCodebaseStats(ctx, name)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}

	result := map[string]interface{}{
		"id":                 cs.ID,
		"display_name":       cs.DisplayName,
		"root_path":          cs.RootPath,
		"file_count":         cs.FileCount,
		"symbol_count":       cs.SymbolCount,
		"relationship_count": cs.RelationshipCount,
		"chunk_count":        cs.ChunkCount,
	}

	if cs.LastIndexedAt != nil {
		result["last_indexed_at"] = cs.LastIndexedAt.Format(time.RFC3339)
		result["index_age_secs"] = time.Since(*cs.LastIndexedAt).Seconds()
	}

	if t.server.qdrant != nil {
		count, err := t.server.qdrant.CountPoints(ctx, cs.ID)
		if err == nil {
			result["vector_count"] = count
		}
	}

	writeJSON(w, result)
}

func (t *HTTPTransport) handleAPIIndexers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if t.indexerMgr == nil {
		writeJSON(w, map[string]interface{}{"indexers": []interface{}{}})
		return
	}

	nodes := t.indexerMgr.GetNodes()

	type indexerResponse struct {
		NodeID      string   `json:"node_id"`
		Codebases   []string `json:"codebases"`
		Status      string   `json:"status"`
		ConnectedAt string   `json:"connected_at"`
		LastSeen    string   `json:"last_seen"`
		Online      bool     `json:"online"`
	}

	var results []indexerResponse
	now := time.Now()
	for _, n := range nodes {
		online := now.Sub(n.LastSeen) < 2*time.Minute
		results = append(results, indexerResponse{
			NodeID:      n.NodeID,
			Codebases:   n.Codebases,
			Status:      n.Status,
			ConnectedAt: n.ConnectedAt.Format(time.RFC3339),
			LastSeen:    n.LastSeen.Format(time.RFC3339),
			Online:      online,
		})
	}

	writeJSON(w, map[string]interface{}{"indexers": results})
}

func (t *HTTPTransport) handleAPIReindexHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if t.indexerMgr == nil {
		writeJSON(w, map[string]interface{}{"requests": []interface{}{}})
		return
	}

	all := t.indexerMgr.GetAllReindexStatuses()

	// Sort by started_at descending
	sort.Slice(all, func(i, j int) bool {
		return all[i].StartedAt.After(all[j].StartedAt)
	})

	// Limit to 20 most recent
	if len(all) > 20 {
		all = all[:20]
	}

	type reindexResponse struct {
		RequestID      string `json:"request_id"`
		Codebase       string `json:"codebase"`
		NodeID         string `json:"node_id"`
		Full           bool   `json:"full"`
		Status         string `json:"status"`
		FilesTotal     int    `json:"files_total"`
		FilesProcessed int    `json:"files_processed"`
		FilesIndexed   int    `json:"files_indexed"`
		FilesSkipped   int    `json:"files_skipped"`
		StartedAt      string `json:"started_at"`
		CompletedAt    string `json:"completed_at,omitempty"`
		DurationMs     int64  `json:"duration_ms"`
		Error          string `json:"error,omitempty"`
	}

	var results []reindexResponse
	for _, req := range all {
		rr := reindexResponse{
			RequestID:      req.RequestID,
			Codebase:       req.Codebase,
			NodeID:         req.NodeID,
			Full:           req.Full,
			Status:         req.Status,
			FilesTotal:     req.FilesTotal,
			FilesProcessed: req.FilesProcessed,
			FilesIndexed:   req.FilesIndexed,
			FilesSkipped:   req.FilesSkipped,
			StartedAt:      req.StartedAt.Format(time.RFC3339),
			DurationMs:     req.DurationMs,
			Error:          req.Error,
		}
		if !req.CompletedAt.IsZero() {
			rr.CompletedAt = req.CompletedAt.Format(time.RFC3339)
		}
		results = append(results, rr)
	}

	writeJSON(w, map[string]interface{}{"requests": results})
}

func (t *HTTPTransport) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	result := map[string]interface{}{
		"uptime_secs": time.Since(startTime).Seconds(),
		"started_at":  startTime.Format(time.RFC3339),
	}

	if t.server.store != nil {
		gs, err := t.server.store.GetGlobalStats(r.Context())
		if err == nil {
			result["total_files"] = gs.TotalFiles
			result["total_symbols"] = gs.TotalSymbols
			result["total_relationships"] = gs.TotalRelationships
			result["total_chunks"] = gs.TotalChunks
			result["codebase_count"] = gs.CodebaseCount
		}
	}

	// Total vector count across all codebases
	if t.server.store != nil && t.server.qdrant != nil {
		codebases, err := t.server.store.ListCodebases(r.Context())
		if err == nil {
			var totalVectors int64
			for _, cb := range codebases {
				count, err := t.server.qdrant.CountPoints(r.Context(), cb.ID)
				if err == nil {
					totalVectors += count
				}
			}
			result["total_vectors"] = totalVectors
		}
	}

	// Connected indexer count
	if t.indexerMgr != nil {
		nodes := t.indexerMgr.GetNodes()
		result["connected_indexers"] = len(nodes)

		// Count online indexers
		now := time.Now()
		online := 0
		for _, n := range nodes {
			if now.Sub(n.LastSeen) < 2*time.Minute {
				online++
			}
		}
		result["online_indexers"] = online
	}

	writeJSON(w, result)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	data, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
		return
	}
	w.Write(data)
}
