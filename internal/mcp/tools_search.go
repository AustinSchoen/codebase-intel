package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AustinSchoen/codebase-intel/internal/metrics"
	"github.com/AustinSchoen/codebase-intel/internal/storage/qdrant"
)

func (s *Server) toolSearchCode(ctx context.Context, args json.RawMessage) (interface{}, error, string) {
	codebase, err := s.extractCodebase(ctx, args)
	if err != nil {
		return nil, err, ""
	}

	var params struct {
		Query        string `json:"query"`
		ModuleFilter string `json:"module_filter"`
		KindFilter   string `json:"kind_filter"`
		Limit        int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err), codebase
	}
	if params.Limit <= 0 {
		params.Limit = 10
	}
	if params.Limit > 25 {
		params.Limit = 25
	}

	if s.embedder == nil || s.qdrant == nil {
		return nil, fmt.Errorf("search backends not available"), codebase
	}

	// Generate embedding for query
	vec, err := s.embedder.Embed(ctx, params.Query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err), codebase
	}

	searchReq := qdrant.SearchRequest{
		DenseVector:  vec,
		ModuleFilter: params.ModuleFilter,
		KindFilter:   params.KindFilter,
		Limit:        params.Limit,
	}

	results, err := s.qdrant.HybridSearch(ctx, codebase, searchReq)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err), codebase
	}

	// Rerank results if enabled
	if s.reranker != nil && len(results) > 1 {
		var docs []string
		for _, r := range results {
			content, _ := r.Payload["content"].(string)
			docs = append(docs, content)
		}
		reranked, err := s.reranker.Rerank(ctx, params.Query, docs, params.Limit)
		if err != nil {
			s.logger.Printf("warning: reranking failed, using original order: %v", err)
		} else if len(reranked) > 0 {
			reordered := make([]qdrant.SearchResult, len(reranked))
			for i, rr := range reranked {
				reordered[i] = results[rr.Index]
				reordered[i].Score = rr.Score
			}
			results = reordered
		}
	}

	// Record search quality metrics
	var topScore float64
	if len(results) > 0 {
		topScore = results[0].Score
	}
	metrics.RecordSearchMetrics(codebase, len(results), topScore)

	type searchResult struct {
		Score         float64 `json:"score"`
		Filepath      string  `json:"filepath"`
		QualifiedName string  `json:"qualified_name"`
		Kind          string  `json:"kind"`
		Module        string  `json:"module"`
		LineStart     int     `json:"line_start"`
		LineEnd       int     `json:"line_end"`
		Content       string  `json:"content"`
	}

	var out []searchResult
	for _, r := range results {
		sr := searchResult{Score: r.Score}
		if v, ok := r.Payload["filepath"].(string); ok {
			sr.Filepath = v
		}
		if v, ok := r.Payload["qualified_name"].(string); ok {
			sr.QualifiedName = v
		}
		if v, ok := r.Payload["kind"].(string); ok {
			sr.Kind = v
		}
		if v, ok := r.Payload["module"].(string); ok {
			sr.Module = v
		}
		if v, ok := r.Payload["line_start"].(float64); ok {
			sr.LineStart = int(v)
		}
		if v, ok := r.Payload["line_end"].(float64); ok {
			sr.LineEnd = int(v)
		}
		if v, ok := r.Payload["content"].(string); ok {
			sr.Content = v
		}
		out = append(out, sr)
	}

	return map[string]interface{}{"results": out, "codebase": codebase}, nil, codebase
}

func (s *Server) toolExplainSubsystem(ctx context.Context, args json.RawMessage) (interface{}, error, string) {
	codebase, err := s.extractCodebase(ctx, args)
	if err != nil {
		return nil, err, ""
	}

	var params struct {
		Topic string `json:"topic"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("parsing args: %w", err), codebase
	}

	summarGen := s.getSummarGen(codebase)
	if summarGen == nil {
		return nil, fmt.Errorf("summary generation not available (check summaries.enabled and API key)"), codebase
	}

	// Check for cached subsystem summary
	cached, err := s.store.GetSummary(ctx, codebase, params.Topic, "subsystem")
	if err != nil {
		return nil, fmt.Errorf("check cached summary: %w", err), codebase
	}

	if cached != nil {
		var keyClasses, relatedModules []string
		json.Unmarshal([]byte(cached.KeyClasses), &keyClasses)
		json.Unmarshal([]byte(cached.Dependencies), &relatedModules)
		return map[string]interface{}{
			"topic":           params.Topic,
			"explanation":     cached.SummaryText,
			"key_classes":     keyClasses,
			"related_modules": relatedModules,
			"codebase":        codebase,
		}, nil, codebase
	}

	// Use semantic search to find relevant code chunks
	var relevantChunks []string
	if s.embedder != nil && s.qdrant != nil {
		vec, err := s.embedder.Embed(ctx, params.Topic)
		if err == nil {
			results, err := s.qdrant.HybridSearch(ctx, codebase, qdrant.SearchRequest{
				DenseVector: vec,
				Limit:       10,
			})
			if err == nil {
				for _, r := range results {
					if content, ok := r.Payload["content"].(string); ok {
						prefix := ""
						if fp, ok := r.Payload["filepath"].(string); ok {
							prefix = "// File: " + fp + "\n"
						}
						if qn, ok := r.Payload["qualified_name"].(string); ok {
							prefix += "// Symbol: " + qn + "\n"
						}
						relevantChunks = append(relevantChunks, prefix+content)
					}
				}
			}
		}
	}

	// Generate explanation
	explanation, keyClasses, relatedModules, err := summarGen.GenerateSubsystemExplanation(ctx, params.Topic, relevantChunks)
	if err != nil {
		return nil, fmt.Errorf("generate explanation: %w", err), codebase
	}

	return map[string]interface{}{
		"topic":           params.Topic,
		"explanation":     explanation,
		"key_classes":     keyClasses,
		"related_modules": relatedModules,
		"codebase":        codebase,
	}, nil, codebase
}
