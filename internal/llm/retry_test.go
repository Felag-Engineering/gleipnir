package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// swapRetryDeps replaces the package-level sleep/rand/clock indirection points
// for the duration of a test and restores them via t.Cleanup. Tests using it
// must not call t.Parallel() — they mutate shared package state (CLAUDE.md).
// The returned slice records every backoff duration the loop slept for.
func swapRetryDeps(t *testing.T) *[]time.Duration {
	t.Helper()
	origSleep, origRand := retrySleep, retryRand
	slept := []time.Duration{}
	retrySleep = func(ctx context.Context, d time.Duration) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slept = append(slept, d)
		return nil
	}
	retryRand = func() float64 { return 0.5 } // full jitter: wait = 0.5 * capped target
	t.Cleanup(func() {
		retrySleep, retryRand = origSleep, origRand
	})
	return &slept
}

func transientHTTP(status int) RetryClassifier {
	return func(error) RetryDecision {
		switch {
		case status == 429:
			return RetryDecision{Retry: true, ErrorType: "rate_limit"}
		case status >= 500:
			return RetryDecision{Retry: true, ErrorType: "server_error"}
		default:
			return RetryDecision{}
		}
	}
}

func TestDoWithRetry_SucceedsFirstAttempt(t *testing.T) {
	slept := swapRetryDeps(t)
	cfg := RetryConfig{MaxAttempts: 4, InitialBackoff: time.Second, MaxBackoff: 30 * time.Second}

	calls := 0
	err := DoWithRetry(context.Background(), cfg, "test", transientHTTP(429), func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
	if len(*slept) != 0 {
		t.Fatalf("expected no backoff sleeps, got %v", *slept)
	}
}

func TestDoWithRetry_RetriesThenSucceeds(t *testing.T) {
	slept := swapRetryDeps(t)
	cfg := RetryConfig{MaxAttempts: 4, InitialBackoff: time.Second, MaxBackoff: 30 * time.Second}

	transient := errors.New("429")
	calls := 0
	err := DoWithRetry(context.Background(), cfg, "test", transientHTTP(429), func() error {
		calls++
		if calls < 3 {
			return transient
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
	// Two backoffs at full-jitter factor 0.5: 0.5*(1s,2s) = 500ms, 1s.
	want := []time.Duration{500 * time.Millisecond, time.Second}
	if len(*slept) != len(want) {
		t.Fatalf("expected %d sleeps, got %v", len(want), *slept)
	}
	for i, d := range want {
		if (*slept)[i] != d {
			t.Errorf("sleep[%d] = %v, want %v", i, (*slept)[i], d)
		}
	}
}

func TestDoWithRetry_ExhaustsAttempts(t *testing.T) {
	swapRetryDeps(t)
	cfg := RetryConfig{MaxAttempts: 3, InitialBackoff: time.Second, MaxBackoff: 30 * time.Second}

	transient := errors.New("rate limited")
	calls := 0
	err := DoWithRetry(context.Background(), cfg, "test", transientHTTP(429), func() error {
		calls++
		return transient
	})
	if !errors.Is(err, transient) {
		t.Fatalf("expected the transient error to surface, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls (MaxAttempts), got %d", calls)
	}
}

func TestDoWithRetry_NonRetryableReturnsImmediately(t *testing.T) {
	slept := swapRetryDeps(t)
	cfg := RetryConfig{MaxAttempts: 4, InitialBackoff: time.Second, MaxBackoff: 30 * time.Second}

	permanent := errors.New("400 bad request")
	calls := 0
	err := DoWithRetry(context.Background(), cfg, "test", transientHTTP(400), func() error {
		calls++
		return permanent
	})
	if !errors.Is(err, permanent) {
		t.Fatalf("expected the permanent error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call for non-retryable error, got %d", calls)
	}
	if len(*slept) != 0 {
		t.Fatalf("expected no sleeps, got %v", *slept)
	}
}

func TestDoWithRetry_ContextCancelledDuringBackoffReturnsAPIError(t *testing.T) {
	origSleep, origRand := retrySleep, retryRand
	retryRand = func() float64 { return 0.5 }
	// Simulate a context that is already done when backoff is attempted.
	retrySleep = func(ctx context.Context, d time.Duration) error {
		return context.DeadlineExceeded
	}
	t.Cleanup(func() { retrySleep, retryRand = origSleep, origRand })

	cfg := RetryConfig{MaxAttempts: 4, InitialBackoff: time.Second, MaxBackoff: 30 * time.Second}
	apiErr := errors.New("429 rate limited")
	calls := 0
	err := DoWithRetry(context.Background(), cfg, "test", transientHTTP(429), func() error {
		calls++
		return apiErr
	})
	// The real API error must surface, not the sleep/context error, so the
	// caller's audit trail shows the true failure cause.
	if !errors.Is(err, apiErr) {
		t.Fatalf("expected API error to surface, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call before backoff cancellation, got %d", calls)
	}
}

func TestBackoffDuration_HonorsRetryAfterClampedToMax(t *testing.T) {
	cfg := RetryConfig{MaxAttempts: 4, InitialBackoff: time.Second, MaxBackoff: 30 * time.Second}

	// Within ceiling: honored verbatim.
	if got := backoffDuration(cfg, 1, 5*time.Second); got != 5*time.Second {
		t.Errorf("Retry-After 5s: got %v, want 5s", got)
	}
	// Above ceiling: clamped.
	if got := backoffDuration(cfg, 1, 90*time.Second); got != 30*time.Second {
		t.Errorf("Retry-After 90s: got %v, want 30s (clamped)", got)
	}
}

func TestRetryDecisionForStatus(t *testing.T) {
	const ra = 3 * time.Second
	tests := []struct {
		code      int
		wantRetry bool
		wantType  string
		wantAfter time.Duration // expected RetryAfter passthrough on retryable codes
	}{
		// Retryable transient failures.
		{408, true, "timeout", ra},
		{429, true, "rate_limit", ra},
		{500, true, "server_error", ra},
		{502, true, "server_error", ra},
		{503, true, "server_error", ra},
		{504, true, "server_error", ra},
		{529, true, "server_error", ra}, // Anthropic overloaded
		{522, true, "server_error", ra}, // CDN gateway fault
		// Permanent — must NOT retry.
		{400, false, "", 0},
		{401, false, "", 0},
		{403, false, "", 0},
		{404, false, "", 0},
		{413, false, "", 0}, // context/payload too large
		{422, false, "", 0},
		{501, false, "", 0}, // not implemented
		{505, false, "", 0},
		{511, false, "", 0},
		{200, false, "", 0},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.code), func(t *testing.T) {
			got := RetryDecisionForStatus(tt.code, ra)
			if got.Retry != tt.wantRetry {
				t.Errorf("code %d: Retry = %v, want %v", tt.code, got.Retry, tt.wantRetry)
			}
			if got.ErrorType != tt.wantType {
				t.Errorf("code %d: ErrorType = %q, want %q", tt.code, got.ErrorType, tt.wantType)
			}
			if got.RetryAfter != tt.wantAfter {
				t.Errorf("code %d: RetryAfter = %v, want %v", tt.code, got.RetryAfter, tt.wantAfter)
			}
		})
	}
}

func TestIsRetryableNetworkError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"wrapped context canceled", fmt.Errorf("dial: %w", context.Canceled), false},
		{"plain error", errors.New("boom"), false},
		{"net.OpError (dial refused)", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"net.DNSError", &net.DNSError{Err: "no such host"}, true},
		{"url.Error wrapping net error", &url.Error{Op: "Post", URL: "http://x", Err: &net.OpError{Op: "dial"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryableNetworkError(tt.err); got != tt.want {
				t.Errorf("IsRetryableNetworkError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestSDKMaxRetries(t *testing.T) {
	orig := DefaultRetryConfig
	t.Cleanup(func() { DefaultRetryConfig = orig })

	for _, tt := range []struct{ attempts, want int }{
		{4, 3}, {2, 1}, {1, 0}, {0, 0},
	} {
		DefaultRetryConfig = RetryConfig{MaxAttempts: tt.attempts}
		if got := SDKMaxRetries(); got != tt.want {
			t.Errorf("MaxAttempts=%d: SDKMaxRetries() = %d, want %d", tt.attempts, got, tt.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	origNow := timeNow
	fixed := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return fixed }
	t.Cleanup(func() { timeNow = origNow })

	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{"nil header", nil, 0},
		{"empty", http.Header{}, 0},
		{"retry-after-ms preferred", http.Header{"Retry-After-Ms": {"1500"}, "Retry-After": {"5"}}, 1500 * time.Millisecond},
		{"delta seconds", http.Header{"Retry-After": {"7"}}, 7 * time.Second},
		{"zero seconds ignored", http.Header{"Retry-After": {"0"}}, 0},
		{"negative seconds ignored", http.Header{"Retry-After": {"-3"}}, 0},
		{"http-date future", http.Header{"Retry-After": {fixed.Add(10 * time.Second).Format(http.TimeFormat)}}, 10 * time.Second},
		{"http-date past ignored", http.Header{"Retry-After": {fixed.Add(-10 * time.Second).Format(http.TimeFormat)}}, 0},
		{"garbage ignored", http.Header{"Retry-After": {"soon"}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseRetryAfter(tt.header); got != tt.want {
				t.Errorf("ParseRetryAfter() = %v, want %v", got, tt.want)
			}
		})
	}
}
