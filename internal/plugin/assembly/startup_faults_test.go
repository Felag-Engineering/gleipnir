//go:build substratev2

package assembly

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
)

type fakeStartupFaultStore struct {
	desired    db.PluginContainer
	desiredErr error
	history    []db.PluginContainerGeneration
	historyErr error
}

func (f fakeStartupFaultStore) GetPluginContainerByInstance(context.Context, string) (db.PluginContainer, error) {
	return f.desired, f.desiredErr
}

func (f fakeStartupFaultStore) ListContainerGenerationsByInstance(context.Context, string) ([]db.PluginContainerGeneration, error) {
	return f.history, f.historyErr
}

func TestStartupFaults_InstanceStartup(t *testing.T) {
	failedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	desired := db.PluginContainer{ImageDigest: "sha256:new", ConfigHash: "cfg", DesiredState: reconciler.DesiredRunning}
	gen := func(n int64, status, detail, digest string) db.PluginContainerGeneration {
		g := db.PluginContainerGeneration{
			Generation: n, Status: status, ImageDigest: digest, ConfigHash: "cfg",
			UpdatedAt: failedAt.Format(time.RFC3339Nano),
		}
		if detail != "" {
			g.StatusDetail = &detail
		}
		return g
	}
	// The reconciler's own marker for a startup failure (startupFailurePrefix).
	const startFailed = "startup failed: start container: no ipv6 sysctl"

	tests := []struct {
		name        string
		store       fakeStartupFaultStore
		wantErr     bool
		wantNil     bool
		wantServing *int64
	}{
		{
			name:    "no desired row is no fault",
			store:   fakeStartupFaultStore{desiredErr: sql.ErrNoRows},
			wantNil: true,
		},
		{
			name:    "desired row read failure is an error",
			store:   fakeStartupFaultStore{desiredErr: errors.New("disk I/O error")},
			wantErr: true,
		},
		{
			name:    "history read failure is an error",
			store:   fakeStartupFaultStore{desired: desired, historyErr: errors.New("disk I/O error")},
			wantErr: true,
		},
		{
			name:    "healthy instance is no fault",
			store:   fakeStartupFaultStore{desired: desired, history: []db.PluginContainerGeneration{gen(1, reconciler.GenActive, "", "sha256:new")}},
			wantNil: true,
		},
		{
			name:  "nothing serving maps serving_generation to nil",
			store: fakeStartupFaultStore{desired: desired, history: []db.PluginContainerGeneration{gen(1, reconciler.GenFailed, startFailed, "sha256:new")}},
		},
		{
			name: "failed upgrade maps the serving generation",
			store: fakeStartupFaultStore{desired: desired, history: []db.PluginContainerGeneration{
				gen(1, reconciler.GenActive, "", "sha256:old"),
				gen(2, reconciler.GenFailed, startFailed, "sha256:new"),
			}},
			wantServing: func() *int64 { n := int64(1); return &n }(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := startupFaults{store: tc.store}.InstanceStartup(context.Background(), "inst-1")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("fault = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("fault = nil, want one")
			}
			if got.Failures != 1 || !strings.Contains(got.LastError, "no ipv6 sysctl") || strings.HasPrefix(got.LastError, "startup failed") {
				t.Errorf("fault = %d failures, %q; want 1 failure with the bare start error", got.Failures, got.LastError)
			}
			if !got.LastFailedAt.Equal(failedAt) || !got.NextAttemptAt.After(failedAt) {
				t.Errorf("last failed %v, next attempt %v; want %v and later", got.LastFailedAt, got.NextAttemptAt, failedAt)
			}
			switch {
			case tc.wantServing == nil && got.ServingGeneration != nil:
				t.Errorf("serving_generation = %d, want nil", *got.ServingGeneration)
			case tc.wantServing != nil && (got.ServingGeneration == nil || *got.ServingGeneration != *tc.wantServing):
				t.Errorf("serving_generation = %v, want %d", got.ServingGeneration, *tc.wantServing)
			}
		})
	}
}
