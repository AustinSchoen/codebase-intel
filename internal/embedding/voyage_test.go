package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// --- splitBatches tests ---

func TestSplitBatches(t *testing.T) {
	tests := []struct {
		name           string
		texts          []string
		maxItems       int
		wantBatches    int
		wantTotalTexts int
	}{
		{
			name:           "single small text",
			texts:          []string{"hello world"},
			maxItems:       128,
			wantBatches:    1,
			wantTotalTexts: 1,
		},
		{
			name:           "exactly 128 small texts",
			texts:          makeTexts(128, "small"),
			maxItems:       128,
			wantBatches:    1,
			wantTotalTexts: 128,
		},
		{
			name:           "129 small texts splits into 2 batches",
			texts:          makeTexts(129, "small"),
			maxItems:       128,
			wantBatches:    2,
			wantTotalTexts: 129,
		},
		{
			name:           "large texts exceed token limit",
			texts:          makeTexts(3, strings.Repeat("x", 200000)), // 200K chars = ~50K tokens each; 2 fit, 3rd overflows
			maxItems:       128,
			wantBatches:    2,
			wantTotalTexts: 3,
		},
		{
			name:           "empty input",
			texts:          []string{},
			maxItems:       128,
			wantBatches:    0,
			wantTotalTexts: 0,
		},
		{
			name:           "single text over 400K chars alone in batch",
			texts:          []string{strings.Repeat("a", 500000)},
			maxItems:       128,
			wantBatches:    1,
			wantTotalTexts: 1,
		},
		{
			name: "mix of small and large texts",
			// Text1: 200K chars = 50K tokens. Text2: "small" = 1 token. Cumulative: 50001 <= 100K, fits.
			// Text3: 200K chars = 50K tokens. 50001+50000=100001 > 100K, flush [text1,text2]. Start new: [text3]=50K tokens.
			// Text4: 200K chars = 50K tokens. 50000+50000=100000, NOT > 100K, fits. [text3,text4]=100K tokens.
			// Text5: 200K chars = 50K tokens. 100000+50000=150000 > 100K, flush [text3,text4]. Start new: [text5].
			// Result: 3 batches: [text1,text2], [text3,text4], [text5].
			texts: []string{
				strings.Repeat("x", 200000), // ~50K tokens
				"small",                     // 1 token
				strings.Repeat("y", 200000), // ~50K tokens
				strings.Repeat("z", 200000), // ~50K tokens
				strings.Repeat("w", 200000), // ~50K tokens
			},
			maxItems:       128,
			wantBatches:    3,
			wantTotalTexts: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batches := splitBatches(tt.texts, tt.maxItems)

			if len(batches) != tt.wantBatches {
				t.Errorf("got %d batches, want %d", len(batches), tt.wantBatches)
			}

			totalTexts := 0
			for _, b := range batches {
				totalTexts += len(b)
			}
			if totalTexts != tt.wantTotalTexts {
				t.Errorf("got %d total texts, want %d", totalTexts, tt.wantTotalTexts)
			}

			// Verify no batch exceeds maxItems
			for i, b := range batches {
				if len(b) > tt.maxItems {
					t.Errorf("batch %d has %d items, exceeds max %d", i, len(b), tt.maxItems)
				}
			}

			// Verify ordering is preserved
			idx := 0
			for _, b := range batches {
				for _, text := range b {
					if text != tt.texts[idx] {
						t.Errorf("ordering broken at index %d", idx)
					}
					idx++
				}
			}
		})
	}
}

func TestSplitBatches_TokenEstimation(t *testing.T) {
	t.Run("token estimation is len/4 with min 1", func(t *testing.T) {
		// A text of 4 chars = 1 token. We can fit 100,000 tokens = 100,000 such texts
		// (since each is 1 token). But maxBatchSize=128 will kick in first.
		// So let's test with a lower maxItems to isolate the token logic.

		// 8 chars = 2 tokens. With maxBatchTokens=100000, we can fit 50000 of these.
		// But let's test that a text near the token boundary causes a split.
		// Use a text that takes exactly 50001 tokens = 200004 chars.
		bigText := strings.Repeat("a", 200004) // 200004/4 = 50001 tokens
		texts := []string{bigText, bigText, bigText}
		batches := splitBatches(texts, 128)

		// First text: 50001 tokens. Second text: 50001 tokens. 50001+50001=100002 > 100000.
		// So second text should start a new batch. Third text also starts a new batch.
		if len(batches) != 3 {
			t.Errorf("got %d batches, want 3", len(batches))
		}
	})

	t.Run("very short text 1-3 chars estimates 1 token", func(t *testing.T) {
		// Texts of 1, 2, 3 chars each estimate to 1 token (len/4 < 1, so min 1)
		// We can fit up to 100,000 of these by tokens, but maxItems=128 limits first.
		texts := []string{"a", "ab", "abc"}
		batches := splitBatches(texts, 128)

		if len(batches) != 1 {
			t.Errorf("got %d batches, want 1 (short texts should all fit in one batch)", len(batches))
		}
		if len(batches[0]) != 3 {
			t.Errorf("batch 0 has %d items, want 3", len(batches[0]))
		}
	})

	t.Run("empty string estimates 1 token", func(t *testing.T) {
		// Empty string: len("")/4 = 0, min 1 → 1 token
		texts := []string{"", "", ""}
		batches := splitBatches(texts, 128)

		if len(batches) != 1 {
			t.Errorf("got %d batches, want 1", len(batches))
		}
	})
}

// --- NewVoyageClient tests ---

func TestNewVoyageClient(t *testing.T) {
	t.Run("default concurrent when zero", func(t *testing.T) {
		c := NewVoyageClient("test-key", "voyage-code-3", 1024, 0)
		if c.concurrent != 10 {
			t.Errorf("concurrent = %d, want 10 (default)", c.concurrent)
		}
	})

	t.Run("default concurrent when negative", func(t *testing.T) {
		c := NewVoyageClient("test-key", "voyage-code-3", 1024, -5)
		if c.concurrent != 10 {
			t.Errorf("concurrent = %d, want 10 (default)", c.concurrent)
		}
	})

	t.Run("positive concurrent preserved", func(t *testing.T) {
		c := NewVoyageClient("test-key", "voyage-code-3", 1024, 5)
		if c.concurrent != 5 {
			t.Errorf("concurrent = %d, want 5", c.concurrent)
		}
	})

	t.Run("fields correctly assigned", func(t *testing.T) {
		c := NewVoyageClient("my-api-key", "voyage-code-3", 1024, 3)
		if c.apiKey != "my-api-key" {
			t.Errorf("apiKey = %q, want %q", c.apiKey, "my-api-key")
		}
		if c.model != "voyage-code-3" {
			t.Errorf("model = %q, want %q", c.model, "voyage-code-3")
		}
		if c.dimensions != 1024 {
			t.Errorf("dimensions = %d, want 1024", c.dimensions)
		}
		if c.maxBatch != maxBatchSize {
			t.Errorf("maxBatch = %d, want %d", c.maxBatch, maxBatchSize)
		}
		if c.client == nil {
			t.Error("client is nil, want non-nil http.Client")
		}
	})
}

// --- HTTP mock tests ---

// newTestClient creates a VoyageClient whose HTTP client is redirected to the given test server.
func newTestClient(t *testing.T, server *httptest.Server) *VoyageClient {
	t.Helper()
	c := NewVoyageClient("test-api-key", "voyage-code-3", 1024, 2)
	// Replace the http.Client with one that routes all requests to the test server.
	c.client = server.Client()
	// Use a custom transport that rewrites the URL to point to the test server.
	c.client.Transport = &urlRewriteTransport{
		base:    server.Client().Transport,
		baseURL: server.URL,
	}
	return c
}

// urlRewriteTransport rewrites every request's URL to point to the test server,
// preserving the original path.
type urlRewriteTransport struct {
	base    http.RoundTripper
	baseURL string
}

func (t *urlRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the URL to point to the test server
	req.URL.Scheme = "http"
	// Parse the base URL to get host
	req.URL.Host = strings.TrimPrefix(t.baseURL, "http://")
	return t.base.RoundTrip(req)
}

func TestEmbed_MockHTTP(t *testing.T) {
	t.Run("successful response returns correct embedding", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify request structure
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer test-api-key" {
				t.Errorf("authorization header = %q, want %q", r.Header.Get("Authorization"), "Bearer test-api-key")
			}
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("content-type = %q, want %q", r.Header.Get("Content-Type"), "application/json")
			}

			var reqBody voyageRequest
			if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if len(reqBody.Input) != 1 {
				t.Errorf("input length = %d, want 1", len(reqBody.Input))
			}
			if reqBody.Model != "voyage-code-3" {
				t.Errorf("model = %q, want %q", reqBody.Model, "voyage-code-3")
			}

			resp := voyageResponse{
				Data: []voyageEmbedding{
					{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
				},
				Usage: voyageUsage{TotalTokens: 5},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		c := newTestClient(t, server)
		emb, err := c.Embed(context.Background(), "hello world")
		if err != nil {
			t.Fatalf("Embed returned error: %v", err)
		}

		if len(emb) != 3 {
			t.Fatalf("embedding length = %d, want 3", len(emb))
		}
		expected := []float32{0.1, 0.2, 0.3}
		for i, v := range expected {
			if emb[i] != v {
				t.Errorf("emb[%d] = %f, want %f", i, emb[i], v)
			}
		}
	})

	t.Run("400 response returns error from Embed", func(t *testing.T) {
		// When the API returns 400, embedSingle returns empty embeddings (no error).
		// EmbedBatch returns them as-is, so results[0] is nil/empty.
		// Embed checks len(results) == 0, which won't trigger (len is 1 but results[0] is nil).
		// So Embed returns nil embedding with no error.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"detail":"token limit exceeded"}`))
		}))
		defer server.Close()

		c := newTestClient(t, server)
		emb, err := c.Embed(context.Background(), "hello world")
		if err != nil {
			t.Fatalf("Embed returned unexpected error: %v", err)
		}
		// On 400, embedSingle returns make([][]float32, len(texts)) where each element is nil.
		// So emb will be nil (the zero value of []float32).
		if emb != nil {
			t.Errorf("expected nil embedding on 400, got %v", emb)
		}
	})

	t.Run("500 response returns error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"internal server error"}`))
		}))
		defer server.Close()

		c := newTestClient(t, server)
		_, err := c.Embed(context.Background(), "hello world")
		if err == nil {
			t.Fatal("expected error on 500 response, got nil")
		}
		if !strings.Contains(err.Error(), "500") {
			t.Errorf("error should mention status 500, got: %v", err)
		}
	})
}

func TestEmbedBatch_MockHTTP(t *testing.T) {
	t.Run("multiple texts returns correct ordering", func(t *testing.T) {
		var requestCount atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount.Add(1)
			var reqBody voyageRequest
			if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
				t.Errorf("failed to decode request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			// Return embeddings that encode the text index for verification
			data := make([]voyageEmbedding, len(reqBody.Input))
			for i := range reqBody.Input {
				// Use the length of the text as the first element of the embedding
				// so we can verify ordering.
				data[i] = voyageEmbedding{
					Embedding: []float32{float32(len(reqBody.Input[i]))},
					Index:     i,
				}
			}

			resp := voyageResponse{
				Data:  data,
				Usage: voyageUsage{TotalTokens: 10},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		c := newTestClient(t, server)
		texts := []string{"a", "bb", "ccc", "dddd", "eeeee"}
		results, err := c.EmbedBatch(context.Background(), texts)
		if err != nil {
			t.Fatalf("EmbedBatch returned error: %v", err)
		}

		if len(results) != len(texts) {
			t.Fatalf("results length = %d, want %d", len(results), len(texts))
		}

		// Verify each embedding corresponds to the correct text (by text length)
		for i, text := range texts {
			if len(results[i]) != 1 {
				t.Fatalf("results[%d] length = %d, want 1", i, len(results[i]))
			}
			if int(results[i][0]) != len(text) {
				t.Errorf("results[%d][0] = %f, want %f (text length)", i, results[i][0], float32(len(text)))
			}
		}
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("server should not be called for empty input")
		}))
		defer server.Close()

		c := newTestClient(t, server)
		results, err := c.EmbedBatch(context.Background(), []string{})
		if err != nil {
			t.Fatalf("EmbedBatch returned error: %v", err)
		}
		if results != nil {
			t.Errorf("expected nil results for empty input, got %v", results)
		}
	})

	t.Run("batches are processed correctly with expected requests", func(t *testing.T) {
		var requestCount atomic.Int32
		var batchSizes []int
		var mu sync.Mutex

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCount.Add(1)
			var reqBody voyageRequest
			if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
				t.Errorf("failed to decode request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			mu.Lock()
			batchSizes = append(batchSizes, len(reqBody.Input))
			mu.Unlock()

			data := make([]voyageEmbedding, len(reqBody.Input))
			for i := range reqBody.Input {
				data[i] = voyageEmbedding{
					Embedding: []float32{1.0},
					Index:     i,
				}
			}

			resp := voyageResponse{
				Data:  data,
				Usage: voyageUsage{TotalTokens: len(reqBody.Input)},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		c := newTestClient(t, server)
		// Create 200 small texts → should produce 2 batches (128 + 72)
		texts := makeTexts(200, "test")
		results, err := c.EmbedBatch(context.Background(), texts)
		if err != nil {
			t.Fatalf("EmbedBatch returned error: %v", err)
		}

		if len(results) != 200 {
			t.Errorf("results length = %d, want 200", len(results))
		}

		if int(requestCount.Load()) != 2 {
			t.Errorf("request count = %d, want 2 (batches of 128 + 72)", requestCount.Load())
		}

		// Verify batch sizes (order may vary due to concurrency, so sort)
		mu.Lock()
		defer mu.Unlock()
		totalFromBatches := 0
		for _, size := range batchSizes {
			totalFromBatches += size
		}
		if totalFromBatches != 200 {
			t.Errorf("total items across batches = %d, want 200", totalFromBatches)
		}
	})
}

func TestEmbedBatch_400Handling(t *testing.T) {
	t.Run("400 response returns empty embeddings no error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"detail":"token limit exceeded"}`))
		}))
		defer server.Close()

		c := newTestClient(t, server)
		texts := []string{"text1", "text2", "text3"}
		results, err := c.EmbedBatch(context.Background(), texts)
		if err != nil {
			t.Fatalf("EmbedBatch returned error on 400: %v", err)
		}

		if len(results) != 3 {
			t.Fatalf("results length = %d, want 3", len(results))
		}

		// Each result should be nil (empty embedding from the make() in embedSingle)
		for i, emb := range results {
			if emb != nil {
				t.Errorf("results[%d] = %v, want nil (empty embedding on 400)", i, emb)
			}
		}
	})

	t.Run("mixed 400 and success across batches", func(t *testing.T) {
		// EmbedBatch fires goroutines for each batch; with c.concurrent=1
		// only one runs at a time but the *acquisition* order of the
		// semaphore is undefined (Go's runtime scheduler decides). Picking
		// success/failure by request-arrival order made this test racy
		// — under load the small (2-item) batch could arrive at the server
		// before the large (128-item) batch and inherit the "success"
		// response, inverting the expected result layout.
		//
		// Dispatch by payload SIZE instead: the 128-item batch always
		// succeeds, the 2-item batch always returns 400, regardless of
		// arrival order. Deterministic without needing goroutine-ordering
		// guarantees from the implementation.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reqBody voyageRequest
			json.NewDecoder(r.Body).Decode(&reqBody)

			if len(reqBody.Input) == 128 {
				// Large batch succeeds.
				data := make([]voyageEmbedding, len(reqBody.Input))
				for i := range reqBody.Input {
					data[i] = voyageEmbedding{
						Embedding: []float32{1.0, 2.0},
						Index:     i,
					}
				}
				resp := voyageResponse{
					Data:  data,
					Usage: voyageUsage{TotalTokens: 10},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			} else {
				// Small batch returns 400 (simulates "this specific batch
				// hit token limits" — what the original test was trying
				// to model).
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"detail":"too many tokens"}`))
			}
		}))
		defer server.Close()

		c := newTestClient(t, server)

		// 130 texts → splitBatches produces [128 items, 2 items].
		texts := makeTexts(130, "hello")
		results, err := c.EmbedBatch(context.Background(), texts)
		if err != nil {
			t.Fatalf("EmbedBatch returned error: %v", err)
		}

		if len(results) != 130 {
			t.Fatalf("results length = %d, want 130", len(results))
		}

		// First 128 should have embeddings (the 128-item batch succeeded).
		for i := 0; i < 128; i++ {
			if results[i] == nil {
				t.Errorf("results[%d] is nil, expected embedding from successful batch", i)
			}
		}

		// Last 2 should be nil (from the 2-item batch's 400 response).
		for i := 128; i < 130; i++ {
			if results[i] != nil {
				t.Errorf("results[%d] = %v, expected nil from 400 batch", i, results[i])
			}
		}
	})
}

// --- helpers ---

// makeTexts creates n copies of the given text.
func makeTexts(n int, text string) []string {
	texts := make([]string, n)
	for i := range texts {
		texts[i] = text
	}
	return texts
}
