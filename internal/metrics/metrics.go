package metrics

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// ToolCallsTotal counts MCP tool calls by tool name.
	ToolCallsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "codebase_intel_tool_calls_total",
			Help: "Total number of MCP tool calls by tool name",
		},
		[]string{"tool"},
	)

	// ToolLatency tracks tool call duration in seconds.
	ToolLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "codebase_intel_tool_latency_seconds",
			Help:    "Latency of MCP tool calls in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"tool"},
	)

	// IndexSize tracks the total number of indexed items.
	IndexSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "codebase_intel_index_size",
			Help: "Current index size by type (chunks, symbols, relationships)",
		},
		[]string{"type"},
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
)

func init() {
	prometheus.MustRegister(ToolCallsTotal)
	prometheus.MustRegister(ToolLatency)
	prometheus.MustRegister(IndexSize)
	prometheus.MustRegister(EmbeddingTokensTotal)
	prometheus.MustRegister(EmbeddingRequestsTotal)
}

// RecordToolCall records a tool call's count and latency.
func RecordToolCall(tool string, duration time.Duration) {
	ToolCallsTotal.WithLabelValues(tool).Inc()
	ToolLatency.WithLabelValues(tool).Observe(duration.Seconds())
}

// SetIndexSize updates index size gauges.
func SetIndexSize(typ string, count float64) {
	IndexSize.WithLabelValues(typ).Set(count)
}

// StartHTTPServer starts a Prometheus metrics HTTP server on the given port.
// It blocks until the server returns an error.
func StartHTTPServer(port int) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	addr := fmt.Sprintf(":%d", port)
	return http.ListenAndServe(addr, mux)
}
