// Package indexer is the thin client side of the indexing system. Under the
// thin-client architecture (issue #18), this package no longer parses, chunks,
// embeds, or talks to Qdrant/Postgres — that's all server-side now. What
// lives here:
//
//   - Client: walks the codebase, reads files, ships content to the server
//     over HTTP, and keeps an SSE connection open for reindex commands.
//   - Daemon: holds N Clients (one per codebase), shares one SSE connection
//     for all of them, runs file watchers.
//
// The split point is /mcp/indexer/files on the server. Content goes up; the
// server runs the pipeline (parse, chunk, embed, store) using the credentials
// it owns. The indexer host only needs the server URL and a bearer token.
package indexer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AustinSchoen/codebase-intel/internal/config"
	"github.com/AustinSchoen/codebase-intel/internal/httpretry"
)

// uploadBatchMaxFiles bounds how many files go in a single /mcp/indexer/files
// POST. Picked to keep per-request RAM modest while amortizing HTTP overhead.
const uploadBatchMaxFiles = 64

// uploadBatchMaxBytes bounds the cumulative file content per POST. Stops one
// huge file from blowing the request body limit.
const uploadBatchMaxBytes = 4 * 1024 * 1024 // 4 MiB

// Client is the per-codebase indexer client. It knows where files live on
// disk, what languages to include, and where to ship the content.
type Client struct {
	cfg         *config.Config
	serverURL   string
	serverToken string

	httpClient *http.Client
	logger     *log.Logger

	// reindexMu serializes reindex operations for this codebase so a watcher-
	// triggered batch and an MCP-triggered reindex don't race.
	reindexMu sync.Mutex
}

// NewClient constructs a thin indexer client. cfg holds the codebase block
// (path, name, languages, excludes) and any per-codebase indexing knobs that
// remain client-side (currently just incremental — chunking parameters moved
// server-side with the pipeline).
func NewClient(cfg *config.Config, serverURL, serverToken string, logger *log.Logger) *Client {
	if logger == nil {
		logger = log.New(os.Stderr, "[indexer] ", log.LstdFlags)
	}
	return &Client{
		cfg:         cfg,
		serverURL:   strings.TrimRight(serverURL, "/"),
		serverToken: serverToken,
		// File-upload batches drive server-side parse + chunk + embed (Voyage
		// round trip) + insert, which can take a couple of minutes for a
		// 64-file batch of a moderately busy codebase. The retry helper
		// already handles transient blips, so this is for "the server is
		// genuinely working" rather than "the server is unreachable."
		httpClient: &http.Client{Timeout: 10 * time.Minute},
		logger:      logger,
	}
}

// Codebase returns the codebase name this client serves.
func (c *Client) Codebase() string { return c.cfg.Codebase.Name }

// RunOnce performs a single full pass: walk the tree, upload everything, GC
// orphans, finalize. Used in one-shot indexer mode (the existing
// `./scripts/index.sh` path).
func (c *Client) RunOnce() error {
	c.reindexMu.Lock()
	defer c.reindexMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return c.fullIndex(ctx)
}

// Reindex is RunOnce with incremental forced off. Triggered by the MCP
// reindex tool or the --reindex CLI flag.
func (c *Client) Reindex() error {
	c.reindexMu.Lock()
	defer c.reindexMu.Unlock()
	prev := c.cfg.Indexing.Incremental
	c.cfg.Indexing.Incremental = false
	defer func() { c.cfg.Indexing.Incremental = prev }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return c.fullIndex(ctx)
}

// FullIndex is the public entry point used by the daemon when handling
// reindex SSE events. The daemon takes the reindexMu itself (it has its own
// serialization model across watcher events) so this method doesn't.
func (c *Client) FullIndex(ctx context.Context) error {
	return c.fullIndex(ctx)
}

// fullIndex walks the codebase, batches up file uploads, and posts them to
// the server. The last batch carries final=true so the server runs the
// cross-file relationship resolution pass.
func (c *Client) fullIndex(ctx context.Context) error {
	files, err := c.walkCodebase()
	if err != nil {
		return fmt.Errorf("walking codebase: %w", err)
	}
	c.logger.Printf("found %d files to index in %s", len(files), c.cfg.Codebase.Name)

	requestID := generateRequestID()
	incremental := c.cfg.Indexing.Incremental
	start := time.Now()

	// Garbage collect orphans before uploading new content. If we did it
	// after, a file deleted on disk between this walk and the next would be
	// orphaned in the index until the next pass.
	relPaths := make([]string, 0, len(files))
	for _, f := range files {
		rel, _ := filepath.Rel(c.cfg.Codebase.Path, f)
		relPaths = append(relPaths, rel)
	}
	if err := c.postGC(ctx, relPaths); err != nil {
		c.logger.Printf("warning: GC failed for %s: %v", c.cfg.Codebase.Name, err)
	}

	var (
		batch        []fileUpload
		batchBytes   int
		indexed      int
		unchanged    int
		failed       int
		filesPosted  int
	)
	flush := func(final bool) error {
		if len(batch) == 0 && !final {
			return nil
		}
		resp, err := c.postFiles(ctx, batch, incremental, final, requestID)
		if err != nil {
			return err
		}
		indexed += resp.Indexed
		unchanged += resp.Unchanged
		failed += resp.Failed
		filesPosted += len(batch)
		batch = batch[:0]
		batchBytes = 0
		if total := len(files); total > 0 && filesPosted > 0 {
			c.logger.Printf("progress: %d/%d files (%d%%)", filesPosted, total, filesPosted*100/total)
		}
		return nil
	}

	for i, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			c.logger.Printf("warning: reading %s: %v", path, err)
			failed++
			continue
		}
		rel, _ := filepath.Rel(c.cfg.Codebase.Path, path)
		batch = append(batch, fileUpload{
			Filepath: filepath.ToSlash(rel),
			Content:  base64.StdEncoding.EncodeToString(content),
		})
		batchBytes += len(content)

		atEnd := i == len(files)-1
		if len(batch) >= uploadBatchMaxFiles || batchBytes >= uploadBatchMaxBytes || atEnd {
			if err := flush(atEnd); err != nil {
				return fmt.Errorf("uploading batch: %w", err)
			}
		}
	}

	// If we never sent a final batch (codebase is empty), send an empty one
	// so the server runs FinalizeRelationships on the existing buffer.
	if filesPosted == 0 {
		if err := flush(true); err != nil {
			return fmt.Errorf("sending final empty batch: %w", err)
		}
	}

	dur := time.Since(start)
	rate := float64(len(files)) / dur.Seconds()
	c.logger.Printf("indexing complete: %d files in %s (%.1f files/sec) — %d indexed, %d unchanged, %d failed",
		len(files), dur.Round(time.Millisecond), rate, indexed, unchanged, failed)
	return nil
}

// IndexSingleFile uploads a single file to the server. Used by the watcher
// when a single file change arrives. Sets final=true so the server flushes
// the relationship buffer for this one-file "request."
func (c *Client) IndexSingleFile(ctx context.Context, path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	rel, _ := filepath.Rel(c.cfg.Codebase.Path, path)
	batch := []fileUpload{{
		Filepath: filepath.ToSlash(rel),
		Content:  base64.StdEncoding.EncodeToString(content),
	}}
	_, err = c.postFiles(ctx, batch, c.cfg.Indexing.Incremental, true, generateRequestID())
	return err
}

// DeleteFile tells the server a file has been removed on the indexer host.
func (c *Client) DeleteFile(ctx context.Context, path string) error {
	rel, _ := filepath.Rel(c.cfg.Codebase.Path, path)
	return c.postDelete(ctx, []string{filepath.ToSlash(rel)})
}

// DetectLanguage maps a file extension to a language tag (the daemon uses
// this to decide whether to ship a file at all; the server uses its own copy
// to dispatch to the right parser).
func (c *Client) DetectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	allowed := func(lang string) string {
		if len(c.cfg.Codebase.Languages) == 0 {
			return lang
		}
		for _, want := range c.cfg.Codebase.Languages {
			if want == lang || normalizeLanguage(want) == lang {
				return lang
			}
		}
		return ""
	}
	switch ext {
	case ".go":
		return allowed("go")
	case ".py":
		return allowed("python")
	case ".ts", ".tsx":
		return allowed("typescript")
	case ".js", ".jsx":
		return allowed("javascript")
	case ".rs":
		return allowed("rust")
	case ".c", ".h":
		return allowed("c")
	case ".cpp", ".cc", ".hpp":
		return allowed("cpp")
	case ".kt", ".kts":
		return allowed("kotlin")
	case ".swift":
		return allowed("swift")
	case ".dart":
		return allowed("dart")
	}
	return ""
}

// RelPath returns the file path relative to the codebase root.
func (c *Client) RelPath(path string) (string, error) {
	return filepath.Rel(c.cfg.Codebase.Path, path)
}

// walkCodebase enumerates files under cfg.Codebase.Path that match an
// allowed language and aren't excluded by cfg.Codebase.ExcludePatterns.
func (c *Client) walkCodebase() ([]string, error) {
	var files []string
	err := filepath.WalkDir(c.cfg.Codebase.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		relPath, _ := filepath.Rel(c.cfg.Codebase.Path, path)
		if d.IsDir() {
			if c.excluded(relPath, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if c.DetectLanguage(path) == "" {
			return nil
		}
		if c.excluded(relPath, false) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// excluded matches the relative path against the codebase's exclude
// patterns. isDir distinguishes directory-level matches (where we can prune
// the walk) from file-level matches.
func (c *Client) excluded(relPath string, isDir bool) bool {
	for _, pattern := range c.cfg.Codebase.ExcludePatterns {
		if matched, _ := filepath.Match(pattern, relPath); matched {
			return true
		}
		// `**/foo` patterns: also check trimmed against basename so the
		// walker can prune mid-tree dirs.
		trimmed := strings.TrimPrefix(pattern, "**/")
		if matched, _ := filepath.Match(trimmed, filepath.Base(relPath)); matched {
			return true
		}
	}
	return false
}

// normalizeLanguage maps alias language tags users may put in their config
// (e.g. "ts") to the canonical tag (e.g. "typescript") used by the parser.
func normalizeLanguage(s string) string {
	switch s {
	case "py":
		return "python"
	case "ts", "tsx":
		return "typescript"
	case "js", "jsx":
		return "javascript"
	case "rs":
		return "rust"
	case "kt", "kts":
		return "kotlin"
	}
	return s
}

// ─────────────────────────────────────────────────────────────────────────
// HTTP client surface — postFiles, postGC, postDelete
// ─────────────────────────────────────────────────────────────────────────

type fileUpload struct {
	Filepath string `json:"filepath"`
	Content  string `json:"content"` // base64
}

type indexFilesRequest struct {
	Codebase    string       `json:"codebase"`
	RequestID   string       `json:"request_id"`
	Incremental bool         `json:"incremental"`
	Final       bool         `json:"final"`
	DisplayName string       `json:"display_name,omitempty"`
	RootPath    string       `json:"root_path,omitempty"`
	Files       []fileUpload `json:"files"`
}

type indexFilesResponse struct {
	Indexed   int    `json:"indexed"`
	Unchanged int    `json:"unchanged"`
	Failed    int    `json:"failed"`
	Finalized bool   `json:"finalized"`
	Error     string `json:"error,omitempty"`
}

func (c *Client) postFiles(ctx context.Context, files []fileUpload, incremental, final bool, requestID string) (indexFilesResponse, error) {
	req := indexFilesRequest{
		Codebase:    c.cfg.Codebase.Name,
		RequestID:   requestID,
		Incremental: incremental,
		Final:       final,
		DisplayName: c.cfg.Codebase.Name,
		RootPath:    c.cfg.Codebase.Path,
		Files:       files,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return indexFilesResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL+"/mcp/indexer/files", bytes.NewReader(body))
	if err != nil {
		return indexFilesResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.serverToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.serverToken)
	}
	resp, err := httpretry.Do(ctx, c.httpClient, httpReq, httpretry.Policy{})
	if err != nil {
		return indexFilesResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return indexFilesResponse{}, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(b))
	}
	var out indexFilesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return indexFilesResponse{}, fmt.Errorf("decoding response: %w", err)
	}
	if out.Error != "" {
		return out, errors.New(out.Error)
	}
	return out, nil
}

type gcRequest struct {
	Codebase  string   `json:"codebase"`
	KeepFiles []string `json:"keep_files"`
}

func (c *Client) postGC(ctx context.Context, keepFiles []string) error {
	for i := range keepFiles {
		keepFiles[i] = filepath.ToSlash(keepFiles[i])
	}
	body, err := json.Marshal(gcRequest{Codebase: c.cfg.Codebase.Name, KeepFiles: keepFiles})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL+"/mcp/indexer/gc", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.serverToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.serverToken)
	}
	resp, err := httpretry.Do(ctx, c.httpClient, req, httpretry.Policy{})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

type deleteRequest struct {
	Codebase string   `json:"codebase"`
	Files    []string `json:"files"`
}

func (c *Client) postDelete(ctx context.Context, files []string) error {
	body, err := json.Marshal(deleteRequest{Codebase: c.cfg.Codebase.Name, Files: files})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL+"/mcp/indexer/delete", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.serverToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.serverToken)
	}
	resp, err := httpretry.Do(ctx, c.httpClient, req, httpretry.Policy{})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// generateRequestID returns a short opaque identifier used to group file
// uploads into a single "indexing pass" for relationship resolution.
func generateRequestID() string {
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}
