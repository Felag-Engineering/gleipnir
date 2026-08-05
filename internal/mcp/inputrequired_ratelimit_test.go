package mcp

import (
	"errors"
	"testing"
	"time"
)

// The bucket allows a burst, then refuses until the clock refills it. The
// clock is frozen and advanced explicitly — this test must never race the wall
// clock, and (per the package-wide rule that comes with the shared timeNow
// var) it must not call t.Parallel().
func TestElicitationLimiter_BurstThenSustainedRate(t *testing.T) {
	advance := freezeClock(t, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))

	// 2 per second, burst of 3.
	l := newElicitationLimiter(2, 3)

	for i := 0; i < 3; i++ {
		if !l.allow() {
			t.Fatalf("burst request %d was refused; the bucket starts full", i+1)
		}
	}
	if l.allow() {
		t.Fatal("a fourth immediate request was allowed; the burst is 3")
	}

	// Half a second refills exactly one token at 2/sec.
	advance(500 * time.Millisecond)
	if !l.allow() {
		t.Fatal("request after a 500ms refill was refused")
	}
	if l.allow() {
		t.Fatal("two requests were served from one refilled token")
	}

	// A long idle period refills to the burst ceiling, not beyond it.
	advance(time.Hour)
	for i := 0; i < 3; i++ {
		if !l.allow() {
			t.Fatalf("post-idle burst request %d was refused", i+1)
		}
	}
	if l.allow() {
		t.Fatal("the bucket refilled past its burst ceiling")
	}
}

// A misconfigured rate or burst falls back to the default. It can never
// resolve to "no limit" — that is the direction the cap exists to prevent.
func TestNewElicitationLimiter_BadValuesFallBackToDefaults(t *testing.T) {
	tests := []struct {
		name       string
		ratePerSec float64
		burst      int
	}{
		{name: "zero", ratePerSec: 0, burst: 0},
		{name: "negative", ratePerSec: -5, burst: -1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			advance := freezeClock(t, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
			l := newElicitationLimiter(tc.ratePerSec, tc.burst)

			for i := 0; i < defaultElicitationBurst; i++ {
				if !l.allow() {
					t.Fatalf("request %d was refused; the default burst is %d", i+1, defaultElicitationBurst)
				}
			}
			if l.allow() {
				t.Fatalf("request %d was allowed; the default burst is %d", defaultElicitationBurst+1, defaultElicitationBurst)
			}
			advance(time.Second) // one second at the default 1/sec
			if !l.allow() {
				t.Fatal("request after a one-second refill was refused")
			}
		})
	}
}

func TestElicitationLimits_Resolve(t *testing.T) {
	tests := []struct {
		name  string
		given ElicitationLimits
		want  ElicitationLimits
	}{
		{
			name: "zero value takes every default",
			want: ElicitationLimits{
				MaxRequestStateBytes: defaultMaxRequestStateBytes,
				MaxRequests:          defaultMaxInputRequests,
				MaxRequestsBytes:     defaultMaxInputRequestsBytes,
			},
		},
		{
			name:  "explicit values win",
			given: ElicitationLimits{MaxRequestStateBytes: 1, MaxRequests: 2, MaxRequestsBytes: 3},
			want:  ElicitationLimits{MaxRequestStateBytes: 1, MaxRequests: 2, MaxRequestsBytes: 3},
		},
		{
			name:  "negative values fall back per field",
			given: ElicitationLimits{MaxRequestStateBytes: -1, MaxRequests: 2, MaxRequestsBytes: -3},
			want: ElicitationLimits{
				MaxRequestStateBytes: defaultMaxRequestStateBytes,
				MaxRequests:          2,
				MaxRequestsBytes:     defaultMaxInputRequestsBytes,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.given.resolve(); got != tc.want {
				t.Errorf("resolve() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The configured caps actually reach the decoder: a result that would pass the
// defaults is rejected under a tighter limit.
func TestDecodeInputRequiredResult_HonorsConfiguredLimits(t *testing.T) {
	result := toolsCallResult{
		RequestState:  []byte(`{"cursor":"abc"}`),
		InputRequests: []byte(`[{"message":"one"},{"message":"two"}]`),
	}

	if _, err := decodeInputRequiredResult(result, ElicitationLimits{}); err != nil {
		t.Fatalf("two requests under the default cap: %v", err)
	}

	_, err := decodeInputRequiredResult(result, ElicitationLimits{MaxRequests: 1})
	if err == nil {
		t.Fatal("two requests under a cap of 1 were accepted")
	}
	var reqErr *InputRequiredError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error = %v, want *InputRequiredError", err)
	}

	if _, err := decodeInputRequiredResult(result, ElicitationLimits{MaxRequestStateBytes: 4}); err == nil {
		t.Fatal("an oversize requestState was accepted under a 4-byte cap")
	}
}
