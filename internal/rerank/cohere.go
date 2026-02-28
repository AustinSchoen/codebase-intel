package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CohereReranker uses the Cohere Rerank API to rerank documents.
type CohereReranker struct {
	apiKey string
	model  string
	client *http.Client
}

// NewCohereReranker creates a CohereReranker with the given API key and model.
func NewCohereReranker(apiKey, model string) *CohereReranker {
	if model == "" {
		model = "rerank-v3.5"
	}
	return &CohereReranker{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

type cohereRerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            int      `json:"top_n"`
	ReturnDocuments bool     `json:"return_documents"`
}

type cohereRerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
		Document       *struct {
			Text string `json:"text"`
		} `json:"document,omitempty"`
	} `json:"results"`
}

// Rerank sends documents to the Cohere Rerank API and returns reranked results.
func (c *CohereReranker) Rerank(ctx context.Context, query string, documents []string, topN int) ([]RerankResult, error) {
	if len(documents) == 0 {
		return nil, nil
	}
	if topN <= 0 || topN > len(documents) {
		topN = len(documents)
	}

	reqBody := cohereRerankRequest{
		Model:           c.model,
		Query:           query,
		Documents:       documents,
		TopN:            topN,
		ReturnDocuments: true,
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.cohere.com/v2/rerank", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cohere rerank error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp cohereRerankResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	results := make([]RerankResult, len(apiResp.Results))
	for i, r := range apiResp.Results {
		doc := ""
		if r.Document != nil {
			doc = r.Document.Text
		} else if r.Index < len(documents) {
			doc = documents[r.Index]
		}
		results[i] = RerankResult{
			Index:    r.Index,
			Score:    r.RelevanceScore,
			Document: doc,
		}
	}

	return results, nil
}
