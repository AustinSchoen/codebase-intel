package httpretry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fastPolicy returns a policy with negligible sleeps for unit tests.
func fastPolicy() Policy {
	return Policy{
		MaxAttempts:    4,
		BaseBackoff:    time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
		JitterFraction: 0,
	}
}

func TestDo_SuccessFirstTry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := Do(context.Background(), srv.Client(), req, fastPolicy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want 1", got)
	}
}

func TestDo_RetriesOn5xxThenSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := Do(context.Background(), srv.Client(), req, fastPolicy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("final status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("hits = %d, want 3", got)
	}
}

func TestDo_DoesNotRetry4xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := Do(context.Background(), srv.Client(), req, fastPolicy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits = %d, want 1 (no retries on 400)", got)
	}
}

func TestDo_Retries429AndHonorsRetryAfterSeconds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1") // 1 second
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Policy with BaseBackoff=1ms so a true 1s Retry-After sleep is easy to detect.
	policy := Policy{
		MaxAttempts:    3,
		BaseBackoff:    time.Millisecond,
		MaxBackoff:     2 * time.Second,
		JitterFraction: 0,
	}

	start := time.Now()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := Do(context.Background(), srv.Client(), req, policy)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	// Retry-After=1s gets jittered by sleepWithJitter, which uses
	// DefaultPolicy.JitterFraction (0.25) regardless of the policy passed
	// in. The actual sleep is therefore 1s × (1 ± 0.25) = [750ms, 1250ms].
	// The lower bound here must accommodate that full range; observed CI
	// failures at ~780ms were inside the valid jitter window. We add a
	// small safety margin under 750ms to cover scheduler latency on slow
	// runners (loaded GitHub Actions hosts).
	if elapsed < 700*time.Millisecond {
		t.Errorf("elapsed = %v, expected at least ~750ms from Retry-After (1s × jitter min)", elapsed)
	}
}

func TestDo_RetryAfterCappedByMaxBackoff(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "120") // 2 minutes
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	policy := Policy{
		MaxAttempts:    3,
		BaseBackoff:    time.Millisecond,
		MaxBackoff:     50 * time.Millisecond, // forces cap
		JitterFraction: 0,
	}

	start := time.Now()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, _ := Do(context.Background(), srv.Client(), req, policy)
	elapsed := time.Since(start)
	resp.Body.Close()

	// Should have been capped to MaxBackoff (50ms), not the 120s the server asked for.
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, expected MaxBackoff cap (~50ms)", elapsed)
	}
}

func TestDo_ExhaustsAttemptsAndReturnsLastResp(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := Do(context.Background(), srv.Client(), req, fastPolicy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("final status = %d, want 502", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != int32(fastPolicy().MaxAttempts) {
		t.Errorf("hits = %d, want %d", got, fastPolicy().MaxAttempts)
	}
}

func TestDo_ReplaysRequestBody(t *testing.T) {
	var hits int32
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		if n < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const payload = `{"hello":"world"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(payload))
	resp, err := Do(context.Background(), srv.Client(), req, fastPolicy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if lastBody != payload {
		t.Errorf("server saw body %q on retry, want %q", lastBody, payload)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("hits = %d, want 2", got)
	}
}

func TestDo_ContextCancellationAborts(t *testing.T) {
	// Server always 500s so the retry loop would keep going forever without
	// context cancellation.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// Generous backoffs that would otherwise outlast the context.
	policy := Policy{
		MaxAttempts:    10,
		BaseBackoff:    100 * time.Millisecond,
		MaxBackoff:     1 * time.Second,
		JitterFraction: 0,
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	start := time.Now()
	_, err := Do(ctx, srv.Client(), req, policy)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	// Should bail well before MaxAttempts * MaxBackoff.
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, expected near-immediate abort on ctx done", elapsed)
	}
}

func TestPolicy_BackoffSchedule(t *testing.T) {
	p := Policy{
		MaxAttempts:    5,
		BaseBackoff:    100 * time.Millisecond,
		MaxBackoff:     500 * time.Millisecond,
		JitterFraction: 0,
	}
	// Expected: 100ms, 200ms, 400ms, then capped at 500ms.
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{4, 500 * time.Millisecond}, // capped
		{5, 500 * time.Millisecond}, // capped
	}
	for _, c := range cases {
		got := p.backoffFor(c.attempt, 0)
		if got != c.want {
			t.Errorf("backoffFor(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in       string
		wantZero bool
	}{
		{"", true},
		{"5", false},
		{"0", true},
		{"-3", true},
		{"banana", true},
	}
	for _, c := range cases {
		d := parseRetryAfter(c.in)
		if c.wantZero && d != 0 {
			t.Errorf("parseRetryAfter(%q) = %v, want 0", c.in, d)
		}
		if !c.wantZero && d == 0 {
			t.Errorf("parseRetryAfter(%q) = 0, want non-zero", c.in)
		}
	}
}

// Sanity check that Do drains and closes intermediate responses so the
// connection can be reused. Detected by reusing the same client for many
// requests — leaked bodies would exhaust the connection pool.
func TestDo_DrainsIntermediateBodies(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write(bytes.Repeat([]byte("x"), 4096)) // non-trivial body
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := Do(context.Background(), srv.Client(), req, fastPolicy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
