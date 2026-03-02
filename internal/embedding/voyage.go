package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/AustinSchoen/codebase-intel/internal/metrics"
)

const (
	voyageAPIURL       = "https://api.voyageai.com/v1/embeddings"
	maxBatchSize       = 128
	maxBatchTokens     = 100000 // stay under Voyage's 120K limit
	charsPerTokenGuess = 4      // rough token estimation: ~4 chars per token
	defaultTimeout     = 30 * time.Second
)

// VoyageClient sends embedding requests to the Voyage AI API.
type VoyageClient struct {
	apiKey     string
	model      string
	dimensions int
	maxBatch   int
	concurrent int
	client     *http.Client
}

// NewVoyageClient creates a new Voyage AI embedding client.
func NewVoyageClient(apiKey, model string, dimensions, concurrent int) *VoyageClient {
	if concurrent <= 0 {
		concurrent = 10
	}
	return &VoyageClient{
		apiKey:     apiKey,
		model:      model,
		dimensions: dimensions,
		maxBatch:   maxBatchSize,
		concurrent: concurrent,
		client:     &http.Client{Timeout: defaultTimeout},
	}
}

type voyageRequest struct {
	Input     []string `json:"input"`
	Model     string   `json:"model"`
	InputType string   `json:"input_type,omitempty"`
}

type voyageResponse struct {
	Data  []voyageEmbedding `json:"data"`
	Usage voyageUsage       `json:"usage"`
}

type voyageEmbedding struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type voyageUsage struct {
	TotalTokens int `json:"total_tokens"`
}

// Embed produces an embedding for a single text input.
func (c *VoyageClient) Embed(ctx context.Context, text string) ([]float32, error) {
	results, err := c.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}
	return results[0], nil
}

// EmbedBatch produces embeddings for multiple texts, batching into groups of 128
// and running up to c.concurrent requests in parallel.
func (c *VoyageClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	batches := splitBatches(texts, c.maxBatch)

	// Precompute offsets for each batch since they may have different sizes
	offsets := make([]int, len(batches))
	offset := 0
	for i, b := range batches {
		offsets[i] = offset
		offset += len(b)
	}

	type batchResult struct {
		index      int
		embeddings [][]float32
		err        error
	}

	results := make(chan batchResult, len(batches))
	sem := make(chan struct{}, c.concurrent)
	var wg sync.WaitGroup

	for i, batch := range batches {
		wg.Add(1)
		go func(idx int, b []string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			embs, err := c.embedSingle(ctx, b)
			results <- batchResult{index: idx, embeddings: embs, err: err}
		}(i, batch)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	ordered := make([][]float32, len(texts))
	for r := range results {
		if r.err != nil {
			return nil, fmt.Errorf("batch %d: %w", r.index, r.err)
		}
		batchOffset := offsets[r.index]
		for j, emb := range r.embeddings {
			ordered[batchOffset+j] = emb
		}
	}

	return ordered, nil
}

func (c *VoyageClient) embedSingle(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := voyageRequest{
		Input:     texts,
		Model:     c.model,
		InputType: "document",
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, voyageAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode == http.StatusBadRequest {
		// 400 errors (e.g. token limit exceeded) — log and return empty embeddings
		// so we don't fail the entire batch
		metrics.EmbeddingErrorsTotal.Inc()
		return make([][]float32, len(texts)), nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("voyage API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var voyageResp voyageResponse
	if err := json.Unmarshal(respBody, &voyageResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	metrics.EmbeddingRequestsTotal.Inc()
	metrics.EmbeddingTokensTotal.Add(float64(voyageResp.Usage.TotalTokens))

	embeddings := make([][]float32, len(texts))
	for _, d := range voyageResp.Data {
		if d.Index < len(embeddings) {
			embeddings[d.Index] = d.Embedding
		}
	}

	return embeddings, nil
}

// splitBatches groups texts into batches respecting both item count and token limits.
// Estimated tokens = len(text) / charsPerTokenGuess.
func splitBatches(texts []string, maxItems int) [][]string {
	var batches [][]string
	var current []string
	currentTokens := 0

	for _, text := range texts {
		estTokens := len(text) / charsPerTokenGuess
		if estTokens < 1 {
			estTokens = 1
		}

		// Start a new batch if adding this text would exceed limits
		if len(current) > 0 && (len(current) >= maxItems || currentTokens+estTokens > maxBatchTokens) {
			batches = append(batches, current)
			current = nil
			currentTokens = 0
		}

		current = append(current, text)
		currentTokens += estTokens
	}

	if len(current) > 0 {
		batches = append(batches, current)
	}

	return batches
}
