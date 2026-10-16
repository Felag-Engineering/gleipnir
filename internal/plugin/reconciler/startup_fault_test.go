package reconciler

import (
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

func TestDescribeStartupFault(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	desired := db.PluginContainer{ImageDigest: "sha256:new", ConfigHash: "cfg-1", DesiredState: DesiredRunning}

	gen := func(n int64, status, detail, digest string, updated time.Duration) db.PluginContainerGeneration {
		g := db.PluginContainerGeneration{
			Generation: n, Status: status, ImageDigest: digest, ConfigHash: "cfg-1",
			UpdatedAt: base.Add(updated).Format(time.RFC3339Nano),
		}
		if detail != "" {
			g.StatusDetail = strPtr(detail)
		}
		return g
	}
	startFail := func(n int64, updated time.Duration) db.PluginContainerGeneration {
		return gen(n, GenFailed, startupFailurePrefix+"start container: no ipv6 sysctl", "sha256:new", updated)
	}

	tests := []struct {
		name    string
		history []db.PluginContainerGeneration
		stopped bool
		wantOK  bool
		want    StartupFault
	}{
		{
			name:    "no history",
			history: nil,
		},
		{
			name:    "first boot still in flight is not a fault",
			history: []db.PluginContainerGeneration{gen(1, GenPending, "", "sha256:new", 0)},
		},
		{
			name: "first boot failing, nothing serving",
			history: []db.PluginContainerGeneration{
				startFail(1, 0), startFail(2, 10*time.Second), startFail(3, 30*time.Second),
			},
			wantOK: true,
			want: StartupFault{
				Failures: 3, LastError: "start container: no ipv6 sysctl", LastGeneration: 3,
				LastFailedAt:  base.Add(30 * time.Second),
				NextAttemptAt: base.Add(30*time.Second + startupBackoff(3)),
			},
		},
		{
			// A retry on its way up must not make the fault vanish until it
			// fails too: the failures before it are still the evidence.
			name: "retry in progress keeps the fault and says so",
			history: []db.PluginContainerGeneration{
				startFail(1, 0), startFail(2, 10*time.Second), gen(3, GenStarting, "", "sha256:new", 40*time.Second),
			},
			wantOK: true,
			want: StartupFault{
				Failures: 2, LastError: "start container: no ipv6 sysctl", LastGeneration: 2,
				LastFailedAt:      base.Add(10 * time.Second),
				NextAttemptAt:     base.Add(10*time.Second + startupBackoff(2)),
				AttemptInProgress: true,
			},
		},
		{
			// The failed upgrade: the old generation keeps serving, and the
			// fault names it rather than reading the instance as down.
			name: "upgrade failing while the previous generation serves",
			history: []db.PluginContainerGeneration{
				gen(1, GenActive, "", "sha256:old", 0), startFail(2, time.Minute), startFail(3, 2*time.Minute),
			},
			wantOK: true,
			want: StartupFault{
				Failures: 2, LastError: "start container: no ipv6 sysctl", LastGeneration: 3,
				LastFailedAt:      base.Add(2 * time.Minute),
				NextAttemptAt:     base.Add(2*time.Minute + startupBackoff(2)),
				ServingGeneration: 1,
			},
		},
		{
			name: "a draining generation counts as serving",
			history: []db.PluginContainerGeneration{
				gen(1, GenDraining, "", "sha256:old", 0), startFail(2, time.Minute),
			},
			wantOK: true,
			want: StartupFault{
				Failures: 1, LastError: "start container: no ipv6 sysctl", LastGeneration: 2,
				LastFailedAt:      base.Add(time.Minute),
				NextAttemptAt:     base.Add(time.Minute + startupBackoff(1)),
				ServingGeneration: 1,
			},
		},
		{
			name: "clears once a generation reaches active",
			history: []db.PluginContainerGeneration{
				startFail(1, 0), startFail(2, 10*time.Second), gen(3, GenActive, "", "sha256:new", time.Minute),
			},
		},
		{
			// Shipping a fixed bundle starts a fresh slate: failures earned by
			// the broken image are not this image's fault.
			name: "failures at a superseded image are not reported",
			history: []db.PluginContainerGeneration{
				gen(1, GenFailed, startupFailurePrefix+"bad image", "sha256:broken", 0),
			},
		},
		{
			name: "a non-startup failure is not a startup fault",
			history: []db.PluginContainerGeneration{
				gen(1, GenFailed, "stopped by operator", "sha256:new", 0),
			},
		},
		{
			name:    "a stopped instance reports nothing",
			stopped: true,
			history: []db.PluginContainerGeneration{startFail(1, 0)},
		},
		{
			name:    "history order does not matter",
			history: []db.PluginContainerGeneration{startFail(2, 10*time.Second), startFail(1, 0)},
			wantOK:  true,
			want: StartupFault{
				Failures: 2, LastError: "start container: no ipv6 sysctl", LastGeneration: 2,
				LastFailedAt:  base.Add(10 * time.Second),
				NextAttemptAt: base.Add(10*time.Second + startupBackoff(2)),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := desired
			if tc.stopped {
				row.DesiredState = DesiredStopped
			}
			got, ok := DescribeStartupFault(tc.history, row)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (fault %+v)", ok, tc.wantOK, got)
			}
			if got != tc.want {
				t.Errorf("fault = %+v\nwant    %+v", got, tc.want)
			}
		})
	}
}
