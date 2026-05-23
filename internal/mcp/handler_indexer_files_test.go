package mcp

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AustinSchoen/codebase-intel/internal/config"
)

// These tests cover the request-validation layer of the /mcp/indexer/*
// handlers without spinning up backends: bearer-token auth, HTTP method
// check, and the "pipeline not initialized" 503.
//
// What we CAN'T reach with a nil pipeline: the JSON-decode and per-field
// validation branches (e.g. "missing codebase"). Those run AFTER the
// pipeline-nil check, so any malformed request bounces with 503 first.
// Exercising those branches would require stubbing the pipeline with a
// non-nil value, which in turn needs an interface refactor (the handler
// calls concrete methods like pipeline.IndexFile). See PR description for
// the follow-up note.
//
// The boilerplate that IS reachable here is still high-value: it's the
// first line of defense against malformed daemon requests and runs on
// every upload from every connected indexer.

// newTestTransport builds an HTTPTransport whose Server has no pipeline.
// That intentionally exercises the 503 "pipeline not initialized" path on
// every endpoint — any request that gets past the auth/method/decode
// boilerplate hits this and returns ServiceUnavailable.
//
// When apiKey is non-empty, the transport requires `Authorization: Bearer
// <apiKey>`; when empty, auth is disabled (matches production behavior).
func newTestTransport(apiKey string) *HTTPTransport {
	srv := &Server{
		cfg:    &config.Config{},
		logger: log.New(io.Discard, "", 0),
	}
	return &HTTPTransport{server: srv, apiKey: apiKey}
}

// boilerplateCase exercises one handler with a particular request shape and
// asserts the response code + (optionally) a substring of the body.
type boilerplateCase struct {
	name        string
	method      string
	authHeader  string // value of Authorization header on the request
	body        string // raw request body
	wantStatus  int
	wantInBody  string // substring; empty means no check
	wantAuthSet bool   // when true, transport has apiKey="expected-token"
}

func (c boilerplateCase) run(t *testing.T, handler func(*HTTPTransport, http.ResponseWriter, *http.Request), endpoint string) {
	t.Helper()
	apiKey := ""
	if c.wantAuthSet {
		apiKey = "expected-token"
	}
	tx := newTestTransport(apiKey)
	req := httptest.NewRequest(c.method, endpoint, strings.NewReader(c.body))
	if c.authHeader != "" {
		req.Header.Set("Authorization", c.authHeader)
	}
	rr := httptest.NewRecorder()

	handler(tx, rr, req)

	if rr.Code != c.wantStatus {
		t.Errorf("status = %d, want %d (body: %s)", rr.Code, c.wantStatus, rr.Body.String())
	}
	if c.wantInBody != "" && !strings.Contains(rr.Body.String(), c.wantInBody) {
		t.Errorf("body %q should contain %q", rr.Body.String(), c.wantInBody)
	}
}

// TestHandleIndexerFiles_Boilerplate covers the request-validation layer of
// /mcp/indexer/files. Pipeline-nil is intentional in these tests — when the
// boilerplate accepts a request, it hits the 503 "pipeline not initialized"
// path; that's our positive signal that the boilerplate didn't reject it
// for the wrong reason.
func TestHandleIndexerFiles_Boilerplate(t *testing.T) {
	handler := func(tx *HTTPTransport, w http.ResponseWriter, r *http.Request) {
		tx.handleIndexerFiles(w, r)
	}
	endpoint := "/mcp/indexer/files"

	cases := []boilerplateCase{
		{
			name:        "401 when bearer is wrong",
			method:      http.MethodPost,
			authHeader:  "Bearer WRONG-TOKEN",
			body:        `{"codebase":"x","request_id":"r"}`,
			wantStatus:  http.StatusUnauthorized,
			wantAuthSet: true,
		},
		{
			name:        "401 when bearer is missing",
			method:      http.MethodPost,
			body:        `{"codebase":"x","request_id":"r"}`,
			wantStatus:  http.StatusUnauthorized,
			wantAuthSet: true,
		},
		{
			name:        "401 when auth header is malformed",
			method:      http.MethodPost,
			authHeader:  "expected-token", // missing "Bearer " prefix
			body:        `{"codebase":"x","request_id":"r"}`,
			wantStatus:  http.StatusUnauthorized,
			wantAuthSet: true,
		},
		{
			name:       "405 on GET",
			method:     http.MethodGet,
			body:       "",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "405 on PUT",
			method:     http.MethodPut,
			body:       `{"codebase":"x","request_id":"r"}`,
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "503 when pipeline is nil but request would otherwise pass auth/method",
			method:     http.MethodPost,
			body:       `{"codebase":"x","request_id":"r"}`,
			wantStatus: http.StatusServiceUnavailable,
			wantInBody: "pipeline not initialized",
		},
		{
			// The pipeline-nil check runs before the JSON decode, so
			// malformed JSON ALSO bounces with 503 here, not 400.
			// Documents the order-of-operations.
			name:       "503 hides malformed JSON when pipeline is nil",
			method:     http.MethodPost,
			body:       `{not json`,
			wantStatus: http.StatusServiceUnavailable,
			wantInBody: "pipeline not initialized",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.run(t, handler, endpoint)
		})
	}
}

// TestHandleIndexerGC_Boilerplate covers the same paths for /mcp/indexer/gc.
// /gc doesn't take a request_id, so the validation rules differ slightly.
func TestHandleIndexerGC_Boilerplate(t *testing.T) {
	handler := func(tx *HTTPTransport, w http.ResponseWriter, r *http.Request) {
		tx.handleIndexerGC(w, r)
	}
	endpoint := "/mcp/indexer/gc"

	cases := []boilerplateCase{
		{
			name:        "401 when bearer is wrong",
			method:      http.MethodPost,
			authHeader:  "Bearer WRONG",
			body:        `{"codebase":"x"}`,
			wantStatus:  http.StatusUnauthorized,
			wantAuthSet: true,
		},
		{
			name:       "405 on GET",
			method:     http.MethodGet,
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "503 when pipeline is nil but request would otherwise pass auth/method",
			method:     http.MethodPost,
			body:       `{"codebase":"x","keep_files":[]}`,
			wantStatus: http.StatusServiceUnavailable,
			wantInBody: "pipeline not initialized",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.run(t, handler, endpoint)
		})
	}
}

// TestHandleIndexerDelete_Boilerplate covers /mcp/indexer/delete. Same shape
// as /gc — needs codebase, no request_id.
func TestHandleIndexerDelete_Boilerplate(t *testing.T) {
	handler := func(tx *HTTPTransport, w http.ResponseWriter, r *http.Request) {
		tx.handleIndexerDelete(w, r)
	}
	endpoint := "/mcp/indexer/delete"

	cases := []boilerplateCase{
		{
			name:        "401 when bearer is wrong",
			method:      http.MethodPost,
			authHeader:  "Bearer WRONG",
			body:        `{"codebase":"x","files":["a.go"]}`,
			wantStatus:  http.StatusUnauthorized,
			wantAuthSet: true,
		},
		{
			name:       "405 on GET",
			method:     http.MethodGet,
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "503 when pipeline is nil but request would otherwise pass auth/method",
			method:     http.MethodPost,
			body:       `{"codebase":"x","files":[]}`,
			wantStatus: http.StatusServiceUnavailable,
			wantInBody: "pipeline not initialized",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.run(t, handler, endpoint)
		})
	}
}

// TestHandleIndexerFiles_AuthDisabledWhenNoAPIKey verifies that when the
// transport's apiKey is empty (auth disabled by configuration), requests
// without an Authorization header still pass the auth check and continue
// to the next validation step.
func TestHandleIndexerFiles_AuthDisabledWhenNoAPIKey(t *testing.T) {
	tx := newTestTransport("") // no API key → auth disabled
	req := httptest.NewRequest(http.MethodPost, "/mcp/indexer/files", strings.NewReader(`{"codebase":"x","request_id":"r"}`))
	rr := httptest.NewRecorder()

	tx.handleIndexerFiles(rr, req)

	// Should reach the 503 (pipeline nil) path, not bounce on auth.
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (with auth disabled, the request should pass through to the pipeline-nil check)", rr.Code)
	}
}

