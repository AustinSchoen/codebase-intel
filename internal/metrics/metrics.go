package metrics

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// ToolCallsTotal counts MCP tool calls by tool name and codebase.
	ToolCallsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "codebase_intel_tool_calls_total",
			Help: "Total number of MCP tool calls by tool name and codebase",
		},
		[]string{"tool", "codebase"},
	)

	// ToolLatency tracks tool call duration in seconds.
	ToolLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "codebase_intel_tool_latency_seconds",
			Help:    "Latency of MCP tool calls in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"tool", "codebase"},
	)

	// IndexSize tracks the total number of indexed items per type and codebase.
	IndexSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "codebase_intel_index_size",
			Help: "Current index size by type and codebase",
		},
		[]string{"type", "codebase"},
	)

	// EmbeddingTokensTotal tracks total tokens sent to the Voyage embedding API.
	EmbeddingTokensTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "codebase_intel_embedding_tokens_total",
			Help: "Total tokens sent to Voyage embedding API",
		},
	)

	// EmbeddingRequestsTotal tracks total Voyage API requests.
	EmbeddingRequestsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "codebase_intel_embedding_requests_total",
			Help: "Total requests to Voyage embedding API",
		},
	)

	// SearchResultsTotal counts total search results returned per codebase.
	SearchResultsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "codebase_intel_search_results_total",
			Help: "Total number of search results returned",
		},
		[]string{"codebase"},
	)

	// SearchRelevanceScore tracks the top-1 result score distribution per codebase.
	SearchRelevanceScore = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "codebase_intel_search_relevance_score",
			Help:    "Distribution of top-1 search result scores",
			Buckets: []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0},
		},
		[]string{"codebase"},
	)

	// SearchEmptyTotal counts searches that returned 0 results per codebase.
	SearchEmptyTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "codebase_intel_search_empty_total",
			Help: "Total number of searches that returned 0 results",
		},
		[]string{"codebase"},
	)
)

func init() {
	prometheus.MustRegister(ToolCallsTotal)
	prometheus.MustRegister(ToolLatency)
	prometheus.MustRegister(IndexSize)
	prometheus.MustRegister(EmbeddingTokensTotal)
	prometheus.MustRegister(EmbeddingRequestsTotal)
	prometheus.MustRegister(SearchResultsTotal)
	prometheus.MustRegister(SearchRelevanceScore)
	prometheus.MustRegister(SearchEmptyTotal)
}

// RecordToolCall records a tool call's count and latency with codebase label.
func RecordToolCall(tool, codebase string, duration time.Duration) {
	ToolCallsTotal.WithLabelValues(tool, codebase).Inc()
	ToolLatency.WithLabelValues(tool, codebase).Observe(duration.Seconds())
}

// SetIndexSize updates index size gauges with codebase label.
func SetIndexSize(typ, codebase string, count float64) {
	IndexSize.WithLabelValues(typ, codebase).Set(count)
}

// RecordSearchMetrics records search quality metrics for a codebase.
func RecordSearchMetrics(codebase string, resultCount int, topScore float64) {
	SearchResultsTotal.WithLabelValues(codebase).Add(float64(resultCount))
	if resultCount == 0 {
		SearchEmptyTotal.WithLabelValues(codebase).Inc()
	} else {
		SearchRelevanceScore.WithLabelValues(codebase).Observe(topScore)
	}
}

// StartHTTPServer starts a Prometheus metrics HTTP server on the given port.
// It blocks until the server returns an error.
func StartHTTPServer(port int) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	addr := fmt.Sprintf(":%d", port)
	return http.ListenAndServe(addr, mux)
}
