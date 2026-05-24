package indexer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/AustinSchoen/codebase-intel/internal/config"
)

// newTestClient builds a *Client pointing at the given test server URL with
// a discard logger. Codebase fields default to a sensible all-languages
// setup; tests override what they need.
func newTestClient(t *testing.T, cfg config.CodebaseConfig, serverURL string) *Client {
	t.Helper()
	c := NewClient(&config.Config{
		Codebase: cfg,
	}, serverURL, "test-token", log.New(io.Discard, "", 0))
	return c
}

// TestNormalizeLanguage covers the alias mapping. Users put either canonical
// names ("typescript") or aliases ("ts") in their codebase config; the
// daemon normalizes both to the canonical form before matching.
func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{
		"py":         "python",
		"ts":         "typescript",
		"tsx":        "typescript",
		"js":         "javascript",
		"jsx":        "javascript",
		"rs":         "rust",
		"kt":         "kotlin",
		"kts":        "kotlin",
		"go":         "go",         // pass-through
		"python":     "python",     // pass-through canonical
		"typescript": "typescript", // pass-through
		"unknown":    "unknown",    // unrecognized passes through unchanged
		"":           "",
	}
	for input, want := range cases {
		if got := normalizeLanguage(input); got != want {
			t.Errorf("normalizeLanguage(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestClient_DetectLanguage_RespectsCodebaseFilter covers the per-codebase
// language allowlist. The server-side DetectLanguage in internal/pipeline
// always returns a tag; the client-side version additionally filters by the
// codebase config so users can opt out of indexing, say, Python files in a
// Go repo.
func TestClient_DetectLanguage_RespectsCodebaseFilter(t *testing.T) {
	cases := []struct {
		name      string
		languages []string
		path      string
		want      string
	}{
		// Empty allowlist = accept everything (default behavior).
		{"empty allowlist", nil, "main.go", "go"},
		{"empty allowlist py", nil, "x.py", "python"},

		// Canonical names match canonical extension.
		{"canonical match", []string{"go"}, "main.go", "go"},
		{"canonical match py", []string{"python"}, "x.py", "python"},

		// Alias names (e.g. "ts") match too — normalize handles it.
		{"alias match ts", []string{"ts"}, "a.ts", "typescript"},
		{"alias match tsx", []string{"ts"}, "a.tsx", "typescript"},
		{"alias match py", []string{"py"}, "x.py", "python"},
		{"alias match rs", []string{"rs"}, "lib.rs", "rust"},

		// Wrong language is filtered out.
		{"go-only excludes py", []string{"go"}, "x.py", ""},
		{"go-only excludes ts", []string{"go"}, "a.ts", ""},

		// Unrecognized extension is always filtered.
		{"unsupported extension", []string{"go"}, "README.md", ""},

		// Multiple languages in allowlist.
		{"multi-lang allows both", []string{"go", "python"}, "x.py", "python"},
		{"multi-lang allows both go", []string{"go", "python"}, "main.go", "go"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, config.CodebaseConfig{
				Name:      "test",
				Path:      "/tmp/test",
				Languages: tc.languages,
			}, "http://localhost")
			got := c.DetectLanguage(tc.path)
			if got != tc.want {
				t.Errorf("DetectLanguage(%q, languages=%v) = %q, want %q",
					tc.path, tc.languages, got, tc.want)
			}
		})
	}
}

// TestClient_Excluded covers the exclude-pattern matching the walker uses
// to prune subtrees and skip individual files. Patterns come from user
// config — regressions here can silently shadow large parts of a codebase.
func TestClient_Excluded(t *testing.T) {
	c := newTestClient(t, config.CodebaseConfig{
		ExcludePatterns: []string{
			"vendor/**",       // top-level only
			"**/node_modules", // basename match anywhere
			"**/dist",
			"**/*.min.js",
		},
	}, "http://localhost")

	cases := []struct {
		name    string
		relPath string
		isDir   bool
		want    bool
	}{
		// `**/foo` patterns are checked via the basename-trim path, so
		// they match a basename anywhere in the tree.
		{"node_modules at root", "node_modules", true, true},
		{"node_modules deep", "pkg/a/node_modules", true, true},
		{"dist at root", "dist", true, true},
		{"dist deep", "pkg/b/dist", true, true},

		// Files matching `**/*.min.js` glob basename
		{"min.js at root", "bundle.min.js", false, true},
		{"min.js nested", "pkg/c/d/foo.min.js", false, true},

		// Non-matching paths
		{"normal file", "main.go", false, false},
		{"normal dir", "pkg/sub", true, false},

		// vendor/** has a literal match at root only (filepath.Match doesn't
		// expand `**`); test the actual current behavior.
		{"vendor literal match", "vendor/foo", false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.excluded(tc.relPath, tc.isDir)
			if got != tc.want {
				t.Errorf("excluded(%q, isDir=%v) = %v, want %v", tc.relPath, tc.isDir, got, tc.want)
			}
		})
	}
}

// TestClient_WalkCodebase exercises the file-walking logic end-to-end
// against a temporary directory tree. Verifies that:
//   - Files matching a permitted language are included
//   - Files in excluded directories are skipped (subtree is pruned)
//   - Unsupported extensions are silently dropped
func TestClient_WalkCodebase(t *testing.T) {
	root := t.TempDir()

	// Build a small fixture tree:
	//   root/
	//   ├── main.go            ← included
	//   ├── README.md          ← excluded (unsupported ext)
	//   ├── pkg/
	//   │   ├── foo.go         ← included
	//   │   └── foo_test.go    ← excluded by pattern
	//   ├── vendor/
	//   │   └── x.go           ← pruned (excluded dir)
	//   └── node_modules/
	//       └── y.go           ← pruned (excluded dir, ** semantics)
	files := map[string]string{
		"main.go":               "package main",
		"README.md":              "# readme",
		"pkg/foo.go":             "package pkg",
		"pkg/foo_test.go":        "package pkg",
		"vendor/x.go":            "package x",
		"node_modules/y.go":      "package y",
		"node_modules/nested/z.go": "package z",
	}
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	c := newTestClient(t, config.CodebaseConfig{
		Name:      "fixture",
		Path:      root,
		Languages: []string{"go"},
		ExcludePatterns: []string{
			"vendor/**",
			"**/node_modules",
			"**/*_test.go",
		},
	}, "http://localhost")

	got, err := c.walkCodebase()
	if err != nil {
		t.Fatalf("walkCodebase: %v", err)
	}

	// Normalize to relative paths sorted for comparison stability.
	rels := make([]string, 0, len(got))
	for _, p := range got {
		rel, _ := filepath.Rel(root, p)
		rels = append(rels, filepath.ToSlash(rel))
	}
	sort.Strings(rels)

	want := []string{"main.go", "pkg/foo.go"}
	if len(rels) != len(want) {
		t.Fatalf("walkCodebase returned %d files (%v), want %d (%v)", len(rels), rels, len(want), want)
	}
	for i := range want {
		if rels[i] != want[i] {
			t.Errorf("walked[%d] = %q, want %q", i, rels[i], want[i])
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// HTTP-layer tests using httptest
// ─────────────────────────────────────────────────────────────────────────

// TestClient_PostFiles_HappyPath verifies that postFiles serializes the
// request correctly, attaches the bearer token, and decodes the response.
// This is the wire-format contract between the daemon and the server.
func TestClient_PostFiles_HappyPath(t *testing.T) {
	var seenReq indexFilesRequest
	var seenAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/mcp/indexer/files" {
			t.Errorf("server saw path %q, want /mcp/indexer/files", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("server saw method %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&seenReq); err != nil {
			t.Fatalf("server failed to decode: %v", err)
		}
		// Echo back a plausible response.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(indexFilesResponse{
			Indexed:   2,
			Unchanged: 0,
			Failed:    0,
			Finalized: true,
		})
	}))
	defer srv.Close()

	c := newTestClient(t, config.CodebaseConfig{
		Name: "demo",
		Path: "/home/user/demo",
	}, srv.URL)

	const payload = "package main"
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))
	files := []fileUpload{
		{Filepath: "main.go", Content: encoded},
		{Filepath: "lib.go", Content: encoded},
	}

	got, err := c.postFiles(context.Background(), files, true, true, "req-1")
	if err != nil {
		t.Fatalf("postFiles: %v", err)
	}
	if got.Indexed != 2 || !got.Finalized {
		t.Errorf("got %+v, want Indexed=2 Finalized=true", got)
	}

	// Server received the correct request shape and auth header.
	if seenAuth != "Bearer test-token" {
		t.Errorf("server saw Authorization=%q, want Bearer test-token", seenAuth)
	}
	if seenReq.Codebase != "demo" || seenReq.RequestID != "req-1" {
		t.Errorf("server saw codebase=%q request_id=%q", seenReq.Codebase, seenReq.RequestID)
	}
	if !seenReq.Incremental || !seenReq.Final {
		t.Errorf("flags lost: incremental=%v final=%v", seenReq.Incremental, seenReq.Final)
	}
	if len(seenReq.Files) != 2 {
		t.Fatalf("server saw %d files, want 2", len(seenReq.Files))
	}

	// Base64 survives round-trip without corruption — the wire format
	// has to handle binary content embedded in JSON.
	decoded, err := base64.StdEncoding.DecodeString(seenReq.Files[0].Content)
	if err != nil {
		t.Fatalf("base64 round-trip: %v", err)
	}
	if string(decoded) != payload {
		t.Errorf("decoded content = %q, want %q", string(decoded), payload)
	}
}

// TestClient_PostFiles_NonOK_IncludesBodyInError verifies that a server
// error (non-2xx) propagates the response body into the returned error so
// operators can diagnose the failure from logs.
func TestClient_PostFiles_NonOK_IncludesBodyInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"codebase missing"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, config.CodebaseConfig{Name: "x", Path: "/tmp/x"}, srv.URL)
	_, err := c.postFiles(context.Background(), nil, false, true, "req-1")
	if err == nil {
		t.Fatal("expected error on 400 response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error %q should mention HTTP status 400", err.Error())
	}
	if !strings.Contains(err.Error(), "codebase missing") {
		t.Errorf("error %q should include response body for diagnostics", err.Error())
	}
}

// TestClient_PostFiles_ServerError_SurfacesAsError covers the case where a
// 200 response carries an `error` field (e.g., relationship finalize failed
// on the server side). The error is reported up so the daemon can log it.
func TestClient_PostFiles_ServerError_SurfacesAsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(indexFilesResponse{
			Indexed:   5,
			Finalized: true,
			Error:     "finalize relationships: db gone",
		})
	}))
	defer srv.Close()

	c := newTestClient(t, config.CodebaseConfig{Name: "x", Path: "/tmp/x"}, srv.URL)
	got, err := c.postFiles(context.Background(), nil, false, true, "req-1")
	if err == nil {
		t.Fatal("expected error when response carries an error field")
	}
	// The response is still returned — caller may want the counts even
	// when finalize failed.
	if got.Indexed != 5 {
		t.Errorf("response not returned alongside error: %+v", got)
	}
	if !strings.Contains(err.Error(), "finalize relationships") {
		t.Errorf("error %q should include the server-reported message", err.Error())
	}
}

// TestClient_PostGC verifies the GC POST shape.
func TestClient_PostGC(t *testing.T) {
	var seen gcRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp/indexer/gc" {
			t.Errorf("path = %q, want /mcp/indexer/gc", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&seen)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t, config.CodebaseConfig{Name: "demo", Path: "/tmp/demo"}, srv.URL)
	keep := []string{"a.go", "pkg/b.go"}
	if err := c.postGC(context.Background(), keep); err != nil {
		t.Fatalf("postGC: %v", err)
	}
	if seen.Codebase != "demo" || len(seen.KeepFiles) != 2 {
		t.Errorf("server saw codebase=%q keep=%v", seen.Codebase, seen.KeepFiles)
	}
}
