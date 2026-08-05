package agent

import (
	"testing"
	"time"
)

// base is a fixed reference instant. Every case is expressed relative to it, so
// the table reads as durations rather than timestamps.
var base = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// The §6.3 precedence rule: the effective deadline is the minimum of the
// applicable clocks, and the source records which one won.
func TestEffectiveDeadline(t *testing.T) {
	tests := []struct {
		name       string
		in         DeadlineInputs
		wantOffset time.Duration // relative to base
		wantSource DeadlineSource
	}{
		{
			name:       "only the policy clock applies",
			in:         DeadlineInputs{Now: base, PolicyTimeout: 30 * time.Minute},
			wantOffset: 30 * time.Minute,
			wantSource: DeadlineSourcePolicy,
		},
		{
			name: "policy shorter than the server TTL wins",
			in: DeadlineInputs{
				Now:           base,
				PolicyTimeout: 10 * time.Minute,
				ServerTaskTTL: base.Add(time.Hour),
			},
			wantOffset: 10 * time.Minute,
			wantSource: DeadlineSourcePolicy,
		},
		{
			name: "server TTL shorter than the policy wins",
			in: DeadlineInputs{
				Now:           base,
				PolicyTimeout: time.Hour,
				ServerTaskTTL: base.Add(5 * time.Minute),
			},
			wantOffset: 5 * time.Minute,
			wantSource: DeadlineSourceServerTTL,
		},
		{
			name: "requestState TTL shortest of the three wins",
			in: DeadlineInputs{
				Now:             base,
				PolicyTimeout:   time.Hour,
				ServerTaskTTL:   base.Add(30 * time.Minute),
				RequestStateTTL: base.Add(2 * time.Minute),
			},
			wantOffset: 2 * time.Minute,
			wantSource: DeadlineSourceRequestState,
		},
		{
			name: "server TTL beats requestState TTL when it is shorter",
			in: DeadlineInputs{
				Now:             base,
				PolicyTimeout:   time.Hour,
				ServerTaskTTL:   base.Add(2 * time.Minute),
				RequestStateTTL: base.Add(30 * time.Minute),
			},
			wantOffset: 2 * time.Minute,
			wantSource: DeadlineSourceServerTTL,
		},
		{
			// A tie is Gleipnir's own timeout elapsing. Reporting a server TTL
			// would point the operator at someone else's system for a deadline
			// that was ours.
			name: "a tie goes to the policy clock",
			in: DeadlineInputs{
				Now:           base,
				PolicyTimeout: 10 * time.Minute,
				ServerTaskTTL: base.Add(10 * time.Minute),
			},
			wantOffset: 10 * time.Minute,
			wantSource: DeadlineSourcePolicy,
		},
		{
			// Clamped to Now rather than left in the past: expiring before an
			// operator could see the request would make a stale server look
			// like an instant, unexplained failure. The source still records
			// that the server ended it.
			name: "an already-expired server TTL clamps to now",
			in: DeadlineInputs{
				Now:           base,
				PolicyTimeout: time.Hour,
				ServerTaskTTL: base.Add(-time.Hour),
			},
			wantOffset: 0,
			wantSource: DeadlineSourceServerTTL,
		},
		{
			name: "a zero server clock does not apply",
			in: DeadlineInputs{
				Now:           base,
				PolicyTimeout: 15 * time.Minute,
				ServerTaskTTL: time.Time{},
			},
			wantOffset: 15 * time.Minute,
			wantSource: DeadlineSourcePolicy,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, source := EffectiveDeadline(tc.in)
			want := base.Add(tc.wantOffset)
			if !got.Equal(want) {
				t.Errorf("deadline = %s, want %s", got, want)
			}
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
		})
	}
}

func TestDeadlineSourceValid(t *testing.T) {
	for _, s := range []DeadlineSource{DeadlineSourcePolicy, DeadlineSourceServerTTL, DeadlineSourceRequestState} {
		if !s.Valid() {
			t.Errorf("DeadlineSource(%q).Valid() = false, want true", s)
		}
	}
	for _, s := range []DeadlineSource{"", "clock", "Policy"} {
		if s.Valid() {
			t.Errorf("DeadlineSource(%q).Valid() = true, want false", s)
		}
	}
}

// The failure message names the clock that ended the wait, because "nobody
// answered" and "the server discarded its state" send an operator to different
// places.
func TestTimeoutMessage(t *testing.T) {
	tests := []struct {
		source   DeadlineSource
		contains string
	}{
		{source: DeadlineSourcePolicy, contains: "operator did not answer"},
		{source: DeadlineSourceServerTTL, contains: "the server, not Gleipnir, ended this wait"},
		{source: DeadlineSourceRequestState, contains: "the server, not Gleipnir, ended this wait"},
	}

	for _, tc := range tests {
		t.Run(string(tc.source), func(t *testing.T) {
			got := timeoutMessage("myserver.deploy", tc.source, 30*time.Minute)
			if !contains(got, tc.contains) {
				t.Errorf("message %q does not contain %q", got, tc.contains)
			}
			if !contains(got, "myserver.deploy") {
				t.Errorf("message %q does not name the tool", got)
			}
		})
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
