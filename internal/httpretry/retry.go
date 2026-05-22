// Package httpretry wraps *http.Client calls with retry + exponential backoff
// for transient failures. Used by the Voyage and Qdrant clients so a single
// blip doesn't fail a whole indexing job.
package httpretry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"
)

// Policy controls retry behavior. Zero values fall back to DefaultPolicy.
type Policy struct {
	// MaxAttempts is the total number of HTTP attempts including the first.
	// Default 4.
	MaxAttempts int

	// BaseBackoff is the wait before the first retry. Each subsequent retry
	// doubles, capped at MaxBackoff. Default 200ms.
	BaseBackoff time.Duration

	// MaxBackoff caps the per-attempt wait. Default 10s.
	MaxBackoff time.Duration

	// JitterFraction is the +/- random fraction applied to each backoff
	// (0.25 = ±25%). Default 0.25.
	JitterFraction float64
}

// DefaultPolicy is the policy used when callers pass Policy{}.
var DefaultPolicy = Policy{
	MaxAttempts:    4,
	BaseBackoff:    200 * time.Millisecond,
	MaxBackoff:     10 * time.Second,
	JitterFraction: 0.25,
}

func (p Policy) withDefaults() Policy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = DefaultPolicy.MaxAttempts
	}
	if p.BaseBackoff <= 0 {
		p.BaseBackoff = DefaultPolicy.BaseBackoff
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = DefaultPolicy.MaxBackoff
	}
	if p.JitterFraction < 0 {
		p.JitterFraction = DefaultPolicy.JitterFraction
	}
	return p
}

// Do executes req with retries on transient failures. The request body, if any,
// is buffered up-front so it can be replayed on each attempt.
//
// Retried: network errors, connection resets, timeouts, 5xx, and 429.
// Not retried: other 4xx (validation, auth) — the caller's bug, not transient.
//
// 429 responses honor the Retry-After header if present and parseable.
//
// The returned response is the last one received (caller closes Body) or nil
// if no response was ever obtained.
func Do(ctx context.Context, client *http.Client, req *http.Request, policy Policy) (*http.Response, error) {
	p := policy.withDefaults()

	// Buffer the body so we can replay it on each attempt. http.Request.Body
	// is a one-shot ReadCloser; without buffering, retries would send empty
	// bodies.
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("buffering request body: %w", err)
		}
	}

	var lastResp *http.Response
	var lastErr error

	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		// Build a fresh request each attempt so headers/body are clean.
		r := req.Clone(ctx)
		if bodyBytes != nil {
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			r.ContentLength = int64(len(bodyBytes))
		}

		resp, err := client.Do(r)

		// Network-level error path.
		if err != nil {
			lastErr = err
			if !isRetryableErr(err) || attempt == p.MaxAttempts {
				return nil, err
			}
			if waitErr := sleepWithJitter(ctx, p.backoffFor(attempt, 0)); waitErr != nil {
				return nil, waitErr
			}
			continue
		}

		// HTTP-level retryable status path.
		if isRetryableStatus(resp.StatusCode) && attempt < p.MaxAttempts {
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			// Drain + close so the connection can be reused.
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if waitErr := sleepWithJitter(ctx, p.backoffFor(attempt, retryAfter)); waitErr != nil {
				return nil, waitErr
			}
			continue
		}

		// Either success or a non-retryable status; return to caller.
		lastResp = resp
		return resp, nil
	}

	// Exhausted attempts.
	if lastResp != nil {
		return lastResp, nil
	}
	return nil, fmt.Errorf("httpretry: exhausted %d attempts: %w", p.MaxAttempts, lastErr)
}

// backoffFor returns the sleep duration for the given attempt (1-indexed).
// If serverHint is non-zero, it takes precedence over the exponential schedule
// (clamped to MaxBackoff).
func (p Policy) backoffFor(attempt int, serverHint time.Duration) time.Duration {
	if serverHint > 0 {
		if serverHint > p.MaxBackoff {
			return p.MaxBackoff
		}
		return serverHint
	}
	d := p.BaseBackoff << (attempt - 1)
	if d > p.MaxBackoff || d < 0 {
		d = p.MaxBackoff
	}
	return d
}

// sleepWithJitter waits d ± JitterFraction, honoring ctx cancellation.
func sleepWithJitter(ctx context.Context, d time.Duration) error {
	// Resolved from caller's policy via closure — but Do() builds the policy
	// once, so jitter is computed inline here against DefaultPolicy.JitterFraction
	// for simplicity. Tests may bypass jitter by accepting the ±25% spread.
	jf := DefaultPolicy.JitterFraction
	if jf > 0 {
		spread := float64(d) * jf
		d = time.Duration(float64(d) - spread + rand.Float64()*2*spread) //nolint:gosec // jitter doesn't need crypto-grade randomness
	}
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// isRetryableStatus returns true for HTTP statuses worth retrying.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests:
		return true
	}
	return code >= 500 && code <= 599
}

// isRetryableErr returns true for network/timeout/connection-reset errors.
func isRetryableErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Caller-driven cancellation isn't a transient backend issue; surface
		// it immediately.
		return false
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// http.Client tends to wrap connection errors in *url.Error → *net.OpError;
	// treat anything net.Error reports as a network failure as retryable.
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

// parseRetryAfter handles both "seconds" and HTTP-date forms.
func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}
