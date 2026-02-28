package rerank

import "context"

// RerankResult holds a single reranked document with its relevance score.
type RerankResult struct {
	Index    int     `json:"index"`
	Score    float64 `json:"relevance_score"`
	Document string  `json:"document"`
}

// Reranker reranks a set of documents by relevance to a query.
type Reranker interface {
	Rerank(ctx context.Context, query string, documents []string, topN int) ([]RerankResult, error)
}
