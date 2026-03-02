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

	// EmbeddingErrorsTotal tracks Voyage API errors (e.g. 400 token limit).
	EmbeddingErrorsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "codebase_intel_embedding_errors_total",
			Help: "Total Voyage embedding API errors (400 responses)",
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

	// ReindexRequestsTotal counts reindex requests by codebase and type.
	ReindexRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "codebase_intel_reindex_requests_total",
			Help: "Total number of reindex requests",
		},
		[]string{"codebase", "type"},
	)

	// ReindexDuration tracks reindex duration in seconds by codebase.
	ReindexDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "codebase_intel_reindex_duration_seconds",
			Help:    "Duration of reindex operations in seconds",
			Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600, 1800},
		},
		[]string{"codebase"},
	)

	// IndexerConnectedNodes tracks the number of connected indexer daemon nodes.
	IndexerConnectedNodes = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "codebase_intel_indexer_connected_nodes",
			Help: "Number of currently connected indexer daemon nodes",
		},
	)

	// FileWatchEventsTotal counts fsnotify events processed by codebase.
	FileWatchEventsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "codebase_intel_file_watch_events_total",
			Help: "Total number of file watch events processed",
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
	prometheus.MustRegister(EmbeddingErrorsTotal)
	prometheus.MustRegister(SearchResultsTotal)
	prometheus.MustRegister(SearchRelevanceScore)
	prometheus.MustRegister(SearchEmptyTotal)
	prometheus.MustRegister(ReindexRequestsTotal)
	prometheus.MustRegister(ReindexDuration)
	prometheus.MustRegister(IndexerConnectedNodes)
	prometheus.MustRegister(FileWatchEventsTotal)
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

// RecordReindexRequest records a reindex request.
func RecordReindexRequest(codebase, reindexType string) {
	ReindexRequestsTotal.WithLabelValues(codebase, reindexType).Inc()
}

// RecordReindexDuration records the duration of a reindex operation.
func RecordReindexDuration(codebase string, duration time.Duration) {
	ReindexDuration.WithLabelValues(codebase).Observe(duration.Seconds())
}

// SetIndexerConnectedNodes sets the current number of connected indexer nodes.
func SetIndexerConnectedNodes(count float64) {
	IndexerConnectedNodes.Set(count)
}

// RecordFileWatchEvent records a file watch event.
func RecordFileWatchEvent(codebase string) {
	FileWatchEventsTotal.WithLabelValues(codebase).Inc()
}

// StartHTTPServer starts a Prometheus metrics HTTP server on the given port.
// It blocks until the server returns an error.
func StartHTTPServer(port int) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	addr := fmt.Sprintf(":%d", port)
	return http.ListenAndServe(addr, mux)
}
