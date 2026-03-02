package qdrant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ---------- helpers ----------

// requestLog captures an incoming HTTP request for later assertions.
type requestLog struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// recordingMux is an http.Handler that routes by "METHOD PATH", records all
// requests (including bodies), and lets each route handler also inspect the
// body through its closure.
type recordingMux struct {
	mu     sync.Mutex
	logs   []requestLog
	routes map[string]http.HandlerFunc
}

func newRecordingMux() *recordingMux {
	return &recordingMux{routes: make(map[string]http.HandlerFunc)}
}

func (m *recordingMux) handle(key string, h http.HandlerFunc) {
	m.routes[key] = h
}

func (m *recordingMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()

	m.mu.Lock()
	m.logs = append(m.logs, requestLog{
		Method: r.Method,
		Path:   r.URL.Path,
		Header: r.Header.Clone(),
		Body:   body,
	})
	m.mu.Unlock()

	key := r.Method + " " + r.URL.Path
	if h, ok := m.routes[key]; ok {
		// Give the handler an empty body -- the original is consumed.
		// Handlers that need the body should use the closure pattern instead.
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		h(w, r)
		return
	}
	http.NotFound(w, r)
}

func (m *recordingMux) getLogs() []requestLog {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]requestLog, len(m.logs))
	copy(cp, m.logs)
	return cp
}

func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ---------- tests ----------

func TestCollectionName(t *testing.T) {
	c := NewClient("http://localhost:6333", "ci", "")
	if got := c.CollectionName("myproject"); got != "ci_myproject" {
		t.Fatalf("expected ci_myproject, got %s", got)
	}

	c2 := NewClient("http://localhost:6333", "", "")
	if got := c2.CollectionName("myproject"); got != "_myproject" {
		t.Fatalf("expected _myproject, got %s", got)
	}
}

func TestNewClient(t *testing.T) {
	c := NewClient("http://example.com:6333", "prefix", "secret-key")
	if c.url != "http://example.com:6333" {
		t.Fatalf("url mismatch: %s", c.url)
	}
	if c.collectionPrefix != "prefix" {
		t.Fatalf("collectionPrefix mismatch: %s", c.collectionPrefix)
	}
	if c.apiKey != "secret-key" {
		t.Fatalf("apiKey mismatch: %s", c.apiKey)
	}
	if c.client == nil {
		t.Fatal("http client should not be nil")
	}
}

func TestEnsureCollection(t *testing.T) {
	t.Run("creates collection when not exists", func(t *testing.T) {
		mux := newRecordingMux()
		mux.handle("GET /collections/ci_proj", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		mux.handle("PUT /collections/ci_proj", func(w http.ResponseWriter, r *http.Request) {
			jsonOK(w, map[string]interface{}{"result": true})
		})
		mux.handle("PUT /collections/ci_proj/index", func(w http.ResponseWriter, r *http.Request) {
			jsonOK(w, map[string]interface{}{"result": true})
		})
		ts := httptest.NewServer(mux)
		defer ts.Close()

		c := NewClient(ts.URL, "ci", "")
		err := c.EnsureCollection(context.Background(), "proj", 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		logs := mux.getLogs()
		var getCount, putCollCount, putIdxCount int
		for _, l := range logs {
			switch {
			case l.Method == "GET" && l.Path == "/collections/ci_proj":
				getCount++
			case l.Method == "PUT" && l.Path == "/collections/ci_proj":
				putCollCount++
			case l.Method == "PUT" && l.Path == "/collections/ci_proj/index":
				putIdxCount++
			}
		}
		if getCount != 1 {
			t.Fatalf("expected 1 GET, got %d", getCount)
		}
		if putCollCount != 1 {
			t.Fatalf("expected 1 PUT for collection, got %d", putCollCount)
		}
		if putIdxCount != 5 {
			t.Fatalf("expected 5 PUT for indexes, got %d", putIdxCount)
		}
	})

	t.Run("skips creation when collection already exists", func(t *testing.T) {
		mux := newRecordingMux()
		mux.handle("GET /collections/ci_existing", func(w http.ResponseWriter, r *http.Request) {
			jsonOK(w, map[string]interface{}{"result": map[string]interface{}{"status": "green"}})
		})
		ts := httptest.NewServer(mux)
		defer ts.Close()

		c := NewClient(ts.URL, "ci", "")
		err := c.EnsureCollection(context.Background(), "existing", 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		logs := mux.getLogs()
		if len(logs) != 1 {
			t.Fatalf("expected 1 request (GET), got %d", len(logs))
		}
		if logs[0].Method != "GET" {
			t.Fatalf("expected GET, got %s", logs[0].Method)
		}
	})
}

func TestUpsert(t *testing.T) {
	mux := newRecordingMux()
	mux.handle("PUT /collections/ci_proj/points", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]interface{}{"result": map[string]interface{}{"status": "completed"}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := NewClient(ts.URL, "ci", "")
	points := []Point{
		{
			ID:      "p1",
			Vector:  map[string]interface{}{"dense": []float32{0.1, 0.2}},
			Payload: map[string]interface{}{"module": "main"},
		},
	}
	err := c.Upsert(context.Background(), "proj", points)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logs := mux.getLogs()
	if len(logs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(logs))
	}

	var capturedBody map[string]json.RawMessage
	if err := json.Unmarshal(logs[0].Body, &capturedBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if _, ok := capturedBody["points"]; !ok {
		t.Fatal("request body missing 'points' key")
	}
	var pts []map[string]interface{}
	json.Unmarshal(capturedBody["points"], &pts)
	if len(pts) != 1 {
		t.Fatalf("expected 1 point, got %d", len(pts))
	}
	if pts[0]["id"] != "p1" {
		t.Fatalf("expected point id p1, got %v", pts[0]["id"])
	}
}

func TestHybridSearch_DenseOnly(t *testing.T) {
	mux := newRecordingMux()
	mux.handle("POST /collections/ci_proj/points/search", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]interface{}{
			"result": []map[string]interface{}{
				{"id": "r1", "score": 0.95, "payload": map[string]interface{}{"module": "core"}},
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := NewClient(ts.URL, "ci", "")
	results, err := c.HybridSearch(context.Background(), "proj", SearchRequest{
		DenseVector: []float32{0.1, 0.2, 0.3},
		Limit:       5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "r1" {
		t.Fatalf("expected id r1, got %s", results[0].ID)
	}
	if results[0].Score != 0.95 {
		t.Fatalf("expected score 0.95, got %f", results[0].Score)
	}

	// Verify dense vector present in request
	logs := mux.getLogs()
	var capturedBody map[string]interface{}
	json.Unmarshal(logs[0].Body, &capturedBody)

	vec, ok := capturedBody["vector"].(map[string]interface{})
	if !ok {
		t.Fatal("expected vector object in request body")
	}
	if vec["name"] != "dense" {
		t.Fatalf("expected vector name 'dense', got %v", vec["name"])
	}

	// Verify no filter when ModuleFilter and KindFilter are empty
	if _, exists := capturedBody["filter"]; exists {
		t.Fatal("expected no filter in request when ModuleFilter and KindFilter are empty")
	}
}

func TestHybridSearch_WithFilters(t *testing.T) {
	mux := newRecordingMux()
	mux.handle("POST /collections/ci_proj/points/search", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]interface{}{"result": []interface{}{}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := NewClient(ts.URL, "ci", "")
	_, err := c.HybridSearch(context.Background(), "proj", SearchRequest{
		DenseVector:  []float32{0.1, 0.2},
		ModuleFilter: "parser",
		KindFilter:   "function",
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logs := mux.getLogs()
	var capturedBody map[string]interface{}
	json.Unmarshal(logs[0].Body, &capturedBody)

	filterRaw, exists := capturedBody["filter"]
	if !exists {
		t.Fatal("expected filter in request body")
	}
	filterMap, ok := filterRaw.(map[string]interface{})
	if !ok {
		t.Fatal("filter is not a map")
	}
	mustRaw, ok := filterMap["must"]
	if !ok {
		t.Fatal("filter missing 'must' key")
	}
	mustArr, ok := mustRaw.([]interface{})
	if !ok {
		t.Fatal("must is not an array")
	}
	if len(mustArr) != 2 {
		t.Fatalf("expected 2 must clauses, got %d", len(mustArr))
	}

	var foundModule, foundKind bool
	for _, clause := range mustArr {
		m := clause.(map[string]interface{})
		key := m["key"].(string)
		matchMap := m["match"].(map[string]interface{})
		switch key {
		case "module":
			if matchMap["value"] == "parser" {
				foundModule = true
			}
		case "kind":
			if matchMap["value"] == "function" {
				foundKind = true
			}
		}
	}
	if !foundModule {
		t.Fatal("module filter not found in must clauses")
	}
	if !foundKind {
		t.Fatal("kind filter not found in must clauses")
	}
}

func TestHybridSearch_HybridQuery(t *testing.T) {
	mux := newRecordingMux()
	mux.handle("POST /collections/ci_proj/points/query", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]interface{}{
			"result": []map[string]interface{}{
				{"id": "rrf1", "score": 0.88, "payload": map[string]interface{}{"module": "indexer"}},
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := NewClient(ts.URL, "ci", "")
	results, err := c.HybridSearch(context.Background(), "proj", SearchRequest{
		DenseVector: []float32{0.1, 0.2, 0.3},
		SparseVector: &SparseVector{
			Indices: []int{10, 42, 99},
			Values:  []float32{1.0, 0.5, 0.3},
		},
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "rrf1" {
		t.Fatalf("expected id rrf1, got %s", results[0].ID)
	}

	logs := mux.getLogs()
	var capturedBody map[string]interface{}
	json.Unmarshal(logs[0].Body, &capturedBody)

	// Verify prefetch is present (hybrid query uses prefetch)
	if _, ok := capturedBody["prefetch"]; !ok {
		t.Fatal("expected 'prefetch' in hybrid query body")
	}
	prefetchArr := capturedBody["prefetch"].([]interface{})
	if len(prefetchArr) != 2 {
		t.Fatalf("expected 2 prefetch entries, got %d", len(prefetchArr))
	}

	// Verify fusion query
	queryRaw := capturedBody["query"].(map[string]interface{})
	if queryRaw["fusion"] != "rrf" {
		t.Fatalf("expected fusion rrf, got %v", queryRaw["fusion"])
	}
}

func TestDeleteByFilter(t *testing.T) {
	mux := newRecordingMux()
	mux.handle("POST /collections/ci_proj/points/delete", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]interface{}{"result": true})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := NewClient(ts.URL, "ci", "")
	err := c.DeleteByFilter(context.Background(), "proj", "internal/parser/parser.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logs := mux.getLogs()
	if len(logs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(logs))
	}

	var capturedBody map[string]interface{}
	if err := json.Unmarshal(logs[0].Body, &capturedBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}

	filterMap := capturedBody["filter"].(map[string]interface{})
	mustArr := filterMap["must"].([]interface{})
	if len(mustArr) != 1 {
		t.Fatalf("expected 1 must clause, got %d", len(mustArr))
	}
	clause := mustArr[0].(map[string]interface{})
	if clause["key"] != "filepath" {
		t.Fatalf("expected key 'filepath', got %v", clause["key"])
	}
	matchMap := clause["match"].(map[string]interface{})
	if matchMap["value"] != "internal/parser/parser.go" {
		t.Fatalf("expected filepath value 'internal/parser/parser.go', got %v", matchMap["value"])
	}
}

func TestListCollections(t *testing.T) {
	mux := newRecordingMux()
	mux.handle("GET /collections", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]interface{}{
			"result": map[string]interface{}{
				"collections": []map[string]interface{}{
					{"name": "ci_proj1"},
					{"name": "ci_proj2"},
					{"name": "other_collection"},
				},
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := NewClient(ts.URL, "ci", "")
	names, err := c.ListCollections(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(names) != 3 {
		t.Fatalf("expected 3 collections, got %d", len(names))
	}
	expected := []string{"ci_proj1", "ci_proj2", "other_collection"}
	for i, name := range names {
		if name != expected[i] {
			t.Fatalf("expected %s at index %d, got %s", expected[i], i, name)
		}
	}
}

func TestCountPoints(t *testing.T) {
	mux := newRecordingMux()
	mux.handle("POST /collections/ci_proj/points/count", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]interface{}{
			"result": map[string]interface{}{
				"count": 42,
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// CountPoints uses http.DefaultClient.Do(req) instead of c.client.Do(req),
	// so we just need to point the url to the test server - DefaultClient will
	// reach it since it is a real HTTP server on localhost.
	c := NewClient(ts.URL, "ci", "")
	count, err := c.CountPoints(context.Background(), "proj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 42 {
		t.Fatalf("expected count 42, got %d", count)
	}
}

func TestAPIKeyHeader(t *testing.T) {
	t.Run("api key is sent when set", func(t *testing.T) {
		mux := newRecordingMux()
		mux.handle("GET /collections", func(w http.ResponseWriter, r *http.Request) {
			jsonOK(w, map[string]interface{}{
				"result": map[string]interface{}{
					"collections": []interface{}{},
				},
			})
		})
		ts := httptest.NewServer(mux)
		defer ts.Close()

		c := NewClient(ts.URL, "ci", "my-secret-key")
		_, err := c.ListCollections(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		logs := mux.getLogs()
		if len(logs) != 1 {
			t.Fatalf("expected 1 request, got %d", len(logs))
		}
		apiKey := logs[0].Header.Get("api-key")
		if apiKey != "my-secret-key" {
			t.Fatalf("expected api-key 'my-secret-key', got '%s'", apiKey)
		}
	})

	t.Run("no api-key header when key is empty", func(t *testing.T) {
		mux := newRecordingMux()
		mux.handle("GET /collections", func(w http.ResponseWriter, r *http.Request) {
			jsonOK(w, map[string]interface{}{
				"result": map[string]interface{}{
					"collections": []interface{}{},
				},
			})
		})
		ts := httptest.NewServer(mux)
		defer ts.Close()

		c := NewClient(ts.URL, "ci", "")
		_, err := c.ListCollections(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		logs := mux.getLogs()
		if len(logs) != 1 {
			t.Fatalf("expected 1 request, got %d", len(logs))
		}
		apiKey := logs[0].Header.Get("api-key")
		if apiKey != "" {
			t.Fatalf("expected no api-key header, got '%s'", apiKey)
		}
	})
}

func TestHTTPErrors(t *testing.T) {
	mux := newRecordingMux()
	mux.handle("PUT /collections/ci_proj/points", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"status":{"error":"internal error"}}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := NewClient(ts.URL, "ci", "")
	err := c.Upsert(context.Background(), "proj", []Point{
		{ID: "p1", Vector: map[string]interface{}{}, Payload: map[string]interface{}{}},
	})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "500") {
		t.Fatalf("expected error to contain status code 500, got: %s", errMsg)
	}
}
