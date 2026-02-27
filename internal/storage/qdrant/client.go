package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client communicates with a Qdrant instance via REST API.
type Client struct {
	url              string
	collectionPrefix string
	client           *http.Client
}

// NewClient creates a new Qdrant REST API client.
func NewClient(url, collectionPrefix string) *Client {
	return &Client{
		url:              url,
		collectionPrefix: collectionPrefix,
		client:           &http.Client{Timeout: 30 * time.Second},
	}
}

// CollectionName returns the full collection name for a codebase.
func (c *Client) CollectionName(codebaseID string) string {
	return fmt.Sprintf("%s_%s", c.collectionPrefix, codebaseID)
}

// Point represents a vector point to upsert into Qdrant.
type Point struct {
	ID      string                 `json:"id"`
	Vector  map[string]interface{} `json:"vector"`
	Payload map[string]interface{} `json:"payload"`
}

// SearchResult represents a single search hit from Qdrant.
type SearchResult struct {
	ID      string                 `json:"id"`
	Score   float64                `json:"score"`
	Payload map[string]interface{} `json:"payload"`
}

// EnsureCollection creates a collection if it doesn't exist.
func (c *Client) EnsureCollection(ctx context.Context, codebaseID string, vectorSize int) error {
	name := c.CollectionName(codebaseID)

	// Check if collection exists
	exists, err := c.collectionExists(ctx, name)
	if err != nil {
		return fmt.Errorf("checking collection: %w", err)
	}
	if exists {
		return nil
	}

	body := map[string]interface{}{
		"vectors": map[string]interface{}{
			"dense": map[string]interface{}{
				"size":     vectorSize,
				"distance": "Cosine",
			},
		},
		"sparse_vectors": map[string]interface{}{
			"text": map[string]interface{}{
				"modifier": "idf",
			},
		},
		"optimizers_config": map[string]interface{}{
			"indexing_threshold": 20000,
		},
	}

	if err := c.put(ctx, fmt.Sprintf("/collections/%s", name), body, nil); err != nil {
		return fmt.Errorf("creating collection %s: %w", name, err)
	}

	// Create payload indexes for filtered search
	indexes := []struct {
		field     string
		fieldType string
	}{
		{"module", "keyword"},
		{"kind", "keyword"},
		{"qualified_name", "keyword"},
		{"language", "keyword"},
		{"filepath", "keyword"},
	}

	for _, idx := range indexes {
		indexBody := map[string]interface{}{
			"field_name":   idx.field,
			"field_schema": idx.fieldType,
		}
		endpoint := fmt.Sprintf("/collections/%s/index", name)
		if err := c.put(ctx, endpoint, indexBody, nil); err != nil {
			return fmt.Errorf("creating index %s: %w", idx.field, err)
		}
	}

	return nil
}

// Upsert inserts or updates points in the collection.
func (c *Client) Upsert(ctx context.Context, codebaseID string, points []Point) error {
	name := c.CollectionName(codebaseID)
	body := map[string]interface{}{
		"points": points,
	}
	endpoint := fmt.Sprintf("/collections/%s/points", name)
	return c.put(ctx, endpoint, body, nil)
}

// SearchRequest configures a hybrid search query.
type SearchRequest struct {
	DenseVector  []float32
	SparseVector *SparseVector
	ModuleFilter string
	KindFilter   string
	Limit        int
}

// SparseVector holds sparse vector data for BM25-style search.
type SparseVector struct {
	Indices []int     `json:"indices"`
	Values  []float32 `json:"values"`
}

// HybridSearch performs a hybrid dense+sparse search with optional payload filtering.
func (c *Client) HybridSearch(ctx context.Context, codebaseID string, req SearchRequest) ([]SearchResult, error) {
	name := c.CollectionName(codebaseID)
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}

	// Build filter
	var filter map[string]interface{}
	var mustClauses []map[string]interface{}
	if req.ModuleFilter != "" {
		mustClauses = append(mustClauses, map[string]interface{}{
			"key":   "module",
			"match": map[string]interface{}{"value": req.ModuleFilter},
		})
	}
	if req.KindFilter != "" {
		mustClauses = append(mustClauses, map[string]interface{}{
			"key":   "kind",
			"match": map[string]interface{}{"value": req.KindFilter},
		})
	}
	if len(mustClauses) > 0 {
		filter = map[string]interface{}{"must": mustClauses}
	}

	// If we have both dense and sparse vectors, use query API with prefetch for RRF
	if req.SparseVector != nil && len(req.DenseVector) > 0 {
		return c.hybridQuery(ctx, name, req, filter, limit)
	}

	// Dense-only search
	body := map[string]interface{}{
		"vector": map[string]interface{}{
			"name":   "dense",
			"vector": req.DenseVector,
		},
		"limit":        limit,
		"with_payload": true,
	}
	if filter != nil {
		body["filter"] = filter
	}

	var resp struct {
		Result []SearchResult `json:"result"`
	}
	endpoint := fmt.Sprintf("/collections/%s/points/search", name)
	if err := c.post(ctx, endpoint, body, &resp); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	return resp.Result, nil
}

func (c *Client) hybridQuery(ctx context.Context, collection string, req SearchRequest, filter map[string]interface{}, limit int) ([]SearchResult, error) {
	// Use Qdrant's query API with prefetch for reciprocal rank fusion
	body := map[string]interface{}{
		"prefetch": []map[string]interface{}{
			{
				"query": map[string]interface{}{
					"name":   "dense",
					"vector": req.DenseVector,
				},
				"using":  "dense",
				"limit":  limit * 2,
				"filter": filter,
			},
			{
				"query": map[string]interface{}{
					"name":    "text",
					"indices": req.SparseVector.Indices,
					"values":  req.SparseVector.Values,
				},
				"using":  "text",
				"limit":  limit * 2,
				"filter": filter,
			},
		},
		"query":        map[string]interface{}{"fusion": "rrf"},
		"limit":        limit,
		"with_payload": true,
	}

	var resp struct {
		Result []SearchResult `json:"result"`
	}
	endpoint := fmt.Sprintf("/collections/%s/points/query", collection)
	if err := c.post(ctx, endpoint, body, &resp); err != nil {
		return nil, fmt.Errorf("hybrid query: %w", err)
	}
	return resp.Result, nil
}

// DeleteByFilter deletes points matching a filter.
func (c *Client) DeleteByFilter(ctx context.Context, codebaseID string, filepath string) error {
	name := c.CollectionName(codebaseID)
	body := map[string]interface{}{
		"filter": map[string]interface{}{
			"must": []map[string]interface{}{
				{
					"key":   "filepath",
					"match": map[string]interface{}{"value": filepath},
				},
			},
		},
	}
	endpoint := fmt.Sprintf("/collections/%s/points/delete", name)
	return c.post(ctx, endpoint, body, nil)
}

func (c *Client) collectionExists(ctx context.Context, name string) (bool, error) {
	endpoint := fmt.Sprintf("/collections/%s", name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+endpoint, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK, nil
}

func (c *Client) put(ctx context.Context, endpoint string, body interface{}, out interface{}) error {
	return c.doJSON(ctx, http.MethodPut, endpoint, body, out)
}

func (c *Client) post(ctx context.Context, endpoint string, body interface{}, out interface{}) error {
	return c.doJSON(ctx, http.MethodPost, endpoint, body, out)
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, body interface{}, out interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.url+endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("qdrant error (status %d): %s", resp.StatusCode, string(respBody))
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}

	return nil
}
