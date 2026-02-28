package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
)

func TestRecordToolCall(t *testing.T) {
	// Reset counters for this test
	RecordToolCall("search_code", "test-project", 150*time.Millisecond)
	RecordToolCall("search_code", "test-project", 200*time.Millisecond)
	RecordToolCall("get_symbol", "test-project", 50*time.Millisecond)

	// Verify counter values
	ch := make(chan prometheus.Metric, 10)
	ToolCallsTotal.Collect(ch)

	gotSearch := false
	gotSymbol := false
	for i := 0; i < 2; i++ {
		m := <-ch
		var dto io_prometheus_client.Metric
		m.Write(&dto)
		val := dto.GetCounter().GetValue()

		var toolName string
		for _, lp := range dto.GetLabel() {
			if lp.GetName() == "tool" {
				toolName = lp.GetValue()
			}
		}

		switch toolName {
		case "search_code":
			if val != 2 {
				t.Errorf("expected search_code count=2, got %f", val)
			}
			gotSearch = true
		case "get_symbol":
			if val != 1 {
				t.Errorf("expected get_symbol count=1, got %f", val)
			}
			gotSymbol = true
		}
	}

	if !gotSearch {
		t.Error("missing search_code metric")
	}
	if !gotSymbol {
		t.Error("missing get_symbol metric")
	}
}

func TestSetIndexSize(t *testing.T) {
	SetIndexSize("chunks", "test-project", 1000)
	SetIndexSize("symbols", "test-project", 500)

	ch := make(chan prometheus.Metric, 10)
	IndexSize.Collect(ch)

	found := 0
	for i := 0; i < 2; i++ {
		m := <-ch
		var dto io_prometheus_client.Metric
		m.Write(&dto)
		val := dto.GetGauge().GetValue()

		for _, lp := range dto.GetLabel() {
			if lp.GetName() == "type" {
				switch lp.GetValue() {
				case "chunks":
					if val != 1000 {
						t.Errorf("expected chunks=1000, got %f", val)
					}
					found++
				case "symbols":
					if val != 500 {
						t.Errorf("expected symbols=500, got %f", val)
					}
					found++
				}
			}
		}
	}

	if found != 2 {
		t.Errorf("expected 2 gauge entries, found %d", found)
	}
}

func TestRecordSearchMetrics(t *testing.T) {
	// Test with results
	RecordSearchMetrics("test-project", 5, 0.85)

	ch := make(chan prometheus.Metric, 10)
	SearchResultsTotal.Collect(ch)
	m := <-ch
	var dto io_prometheus_client.Metric
	m.Write(&dto)
	if dto.GetCounter().GetValue() != 5 {
		t.Errorf("expected search_results_total=5, got %f", dto.GetCounter().GetValue())
	}

	// Test empty search
	RecordSearchMetrics("test-project", 0, 0)

	ch2 := make(chan prometheus.Metric, 10)
	SearchEmptyTotal.Collect(ch2)
	m2 := <-ch2
	var dto2 io_prometheus_client.Metric
	m2.Write(&dto2)
	if dto2.GetCounter().GetValue() != 1 {
		t.Errorf("expected search_empty_total=1, got %f", dto2.GetCounter().GetValue())
	}
}
