package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/AustinSchoen/codebase-intel/internal/config"
	"github.com/AustinSchoen/codebase-intel/internal/embedding"
	"github.com/AustinSchoen/codebase-intel/internal/storage/qdrant"
)

type benchmarkEntry struct {
	Query           string   `json:"query"`
	ExpectedSymbols []string `json:"expected_symbols"`
	ExpectedFiles   []string `json:"expected_files"`
}

type benchmarkResult struct {
	Query       string  `json:"query"`
	Precision5  float64 `json:"precision_at_5"`
	MRR         float64 `json:"mrr"`
	TopResults  []hit   `json:"top_results"`
	MatchCount  int     `json:"match_count"`
	TotalTop5   int     `json:"total_top5"`
}

type hit struct {
	QualifiedName string  `json:"qualified_name"`
	Filepath      string  `json:"filepath"`
	Score         float64 `json:"score"`
	IsMatch       bool    `json:"is_match"`
}

type benchmarkSummary struct {
	TotalQueries    int               `json:"total_queries"`
	MeanPrecision5  float64           `json:"mean_precision_at_5"`
	MeanMRR         float64           `json:"mean_mrr"`
	Results         []benchmarkResult `json:"results"`
}

func main() {
	var (
		benchFile  = flag.String("file", "benchmarks/known_answers.json", "Path to known answers JSON file")
		configFile = flag.String("config", "", "Path to config.yaml")
	)
	flag.Parse()

	// Load config
	var cfg *config.Config
	var err error
	if *configFile != "" {
		cfg, err = config.LoadFromFile(*configFile)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	env, err := cfg.ResolveEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve env: %v\n", err)
		os.Exit(1)
	}

	// Load benchmark entries
	data, err := os.ReadFile(*benchFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read benchmark file: %v\n", err)
		os.Exit(1)
	}

	var entries []benchmarkEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse benchmark file: %v\n", err)
		os.Exit(1)
	}

	// Initialize clients
	embedder := embedding.NewVoyageClient(
		env.EmbeddingAPIKey,
		cfg.Embedding.Model,
		cfg.Embedding.Dimensions,
		cfg.Indexing.ConcurrentReqs,
	)
	qdrantClient := qdrant.NewClient(cfg.Vector.URL, cfg.Vector.CollectionPrefix, env.VectorAPIKey)

	ctx := context.Background()

	var results []benchmarkResult
	var totalP5, totalMRR float64

	for _, entry := range entries {
		result := runBenchmark(ctx, entry, embedder, qdrantClient, cfg.Codebase.Name)
		results = append(results, result)
		totalP5 += result.Precision5
		totalMRR += result.MRR
	}

	n := float64(len(entries))
	summary := benchmarkSummary{
		TotalQueries:   len(entries),
		MeanPrecision5: totalP5 / n,
		MeanMRR:        totalMRR / n,
		Results:        results,
	}

	out, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Println(string(out))
}

func runBenchmark(ctx context.Context, entry benchmarkEntry, embedder *embedding.VoyageClient, qdrantClient *qdrant.Client, codebaseID string) benchmarkResult {
	result := benchmarkResult{
		Query: entry.Query,
	}

	// Embed query
	vec, err := embedder.Embed(ctx, entry.Query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to embed query '%s': %v\n", entry.Query, err)
		return result
	}

	// Search
	searchResults, err := qdrantClient.HybridSearch(ctx, codebaseID, qdrant.SearchRequest{
		DenseVector: vec,
		Limit:       10,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: search failed for '%s': %v\n", entry.Query, err)
		return result
	}

	// Build expected set (case-insensitive matching)
	expectedSymbols := make(map[string]bool)
	for _, s := range entry.ExpectedSymbols {
		expectedSymbols[strings.ToLower(s)] = true
	}
	expectedFiles := make(map[string]bool)
	for _, f := range entry.ExpectedFiles {
		expectedFiles[strings.ToLower(f)] = true
	}

	// Evaluate top-5
	top5 := searchResults
	if len(top5) > 5 {
		top5 = top5[:5]
	}

	matchCount := 0
	firstMatchRank := 0

	for rank, sr := range top5 {
		qualifiedName, _ := sr.Payload["qualified_name"].(string)
		filepath, _ := sr.Payload["filepath"].(string)

		isMatch := matchesExpected(qualifiedName, filepath, expectedSymbols, expectedFiles)
		if isMatch {
			matchCount++
			if firstMatchRank == 0 {
				firstMatchRank = rank + 1
			}
		}

		result.TopResults = append(result.TopResults, hit{
			QualifiedName: qualifiedName,
			Filepath:      filepath,
			Score:         sr.Score,
			IsMatch:       isMatch,
		})
	}

	result.TotalTop5 = len(top5)
	if len(top5) > 0 {
		result.Precision5 = float64(matchCount) / float64(len(top5))
	}
	if firstMatchRank > 0 {
		result.MRR = 1.0 / float64(firstMatchRank)
	}
	result.MatchCount = matchCount

	return result
}

// matchesExpected checks if a search result matches any expected symbol or file.
func matchesExpected(qualifiedName, filepath string, expectedSymbols, expectedFiles map[string]bool) bool {
	qnLower := strings.ToLower(qualifiedName)
	fpLower := strings.ToLower(filepath)

	// Check file match
	if expectedFiles[fpLower] {
		return true
	}

	// Check symbol match (substring or exact)
	for sym := range expectedSymbols {
		if strings.Contains(qnLower, sym) {
			return true
		}
	}

	return false
}
