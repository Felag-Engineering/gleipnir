package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// stubStartupReader answers InstanceStartup from a fixed map and records which
// instance was asked about.
type stubStartupReader struct {
	faults map[string]*InstanceStartupFault
	err    error
	asked  []string
}

func (s *stubStartupReader) InstanceStartup(_ context.Context, instanceID string) (*InstanceStartupFault, error) {
	s.asked = append(s.asked, instanceID)
	if s.err != nil {
		return nil, s.err
	}
	return s.faults[instanceID], nil
}

func TestGetInstance_StartupFault(t *testing.T) {
	failedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	serving := int64(4)

	tests := []struct {
		name       string
		reader     *stubStartupReader // nil = no reader wired (v1)
		wantStatus int
		// wantStartup is the decoded `startup` object, or nil when the key
		// must be absent.
		wantStartup map[string]any
	}{
		{
			name:       "v1 has no reader and no startup key",
			reader:     nil,
			wantStatus: http.StatusOK,
		},
		{
			name:       "no fault leaves the key absent",
			reader:     &stubStartupReader{},
			wantStatus: http.StatusOK,
		},
		{
			name: "nothing serving",
			reader: &stubStartupReader{faults: map[string]*InstanceStartupFault{"inst-1": {
				Failures: 3, LastError: "start container: no ipv6 sysctl", LastGeneration: 3,
				LastFailedAt: failedAt, NextAttemptAt: failedAt.Add(40 * time.Second),
			}}},
			wantStatus: http.StatusOK,
			wantStartup: map[string]any{
				"failures":            float64(3),
				"last_error":          "start container: no ipv6 sysctl",
				"last_generation":     float64(3),
				"last_failed_at":      "2026-10-01T12:00:00Z",
				"next_attempt_at":     "2026-10-01T12:00:40Z",
				"attempt_in_progress": false,
				"serving_generation":  nil,
			},
		},
		{
			name: "failed upgrade names the generation still serving",
			reader: &stubStartupReader{faults: map[string]*InstanceStartupFault{"inst-1": {
				Failures: 1, LastError: "health gate: exited 1", LastGeneration: 5,
				LastFailedAt: failedAt, NextAttemptAt: failedAt.Add(10 * time.Second),
				AttemptInProgress: true, ServingGeneration: &serving,
			}}},
			wantStatus: http.StatusOK,
			wantStartup: map[string]any{
				"failures":            float64(1),
				"last_error":          "health gate: exited 1",
				"last_generation":     float64(5),
				"last_failed_at":      "2026-10-01T12:00:00Z",
				"next_attempt_at":     "2026-10-01T12:00:10Z",
				"attempt_in_progress": true,
				"serving_generation":  float64(4),
			},
		},
		{
			// An absent key reads as "no fault"; when the read fails the
			// handler must not claim that.
			name:       "read error is a 500, not a silent no-fault",
			reader:     &stubStartupReader{err: errors.New("database is locked")},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := newFakePluginQuerier()
			q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "test-plugin", ManifestSnapshot: instanceConfigManifestWithSecret})
			q.seed(db.PluginInstance{
				ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", ConfigJson: "{}",
				HealthState: "unhealthy", Version: 1, UpdatedAt: "2026-01-01T00:00:00Z",
			})

			cfg := testPluginHandlerConfig{}
			if tc.reader != nil {
				cfg.startup = tc.reader
			}
			h := newTestPluginHandler(q, nil, cfg)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
			rec := httptest.NewRecorder()
			h.GetInstance(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.reader != nil && (len(tc.reader.asked) != 1 || tc.reader.asked[0] != "inst-1") {
				t.Errorf("reader asked about %v, want [inst-1]", tc.reader.asked)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}

			var body map[string]any
			if err := json.Unmarshal(parseDataResponse(t, rec), &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got, present := body["startup"]
			if tc.wantStartup == nil {
				if present {
					t.Errorf("startup = %v, want the key absent", got)
				}
				return
			}
			gotMap, ok := got.(map[string]any)
			if !ok {
				t.Fatalf("startup = %#v, want an object", got)
			}
			if len(gotMap) != len(tc.wantStartup) {
				t.Errorf("startup keys = %v, want %v", gotMap, tc.wantStartup)
			}
			for k, want := range tc.wantStartup {
				if gotMap[k] != want {
					t.Errorf("startup.%s = %#v, want %#v", k, gotMap[k], want)
				}
			}
		})
	}
}

// Mutation responses share instanceResponse but never read the startup
// fault: it would be one more query on every config write, for a field the
// page reads from GET.
func TestInstanceMutationResponses_OmitStartup(t *testing.T) {
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "test-plugin", ManifestSnapshot: instanceConfigManifestWithSecret})
	inst := db.PluginInstance{
		ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", ConfigJson: "{}",
		HealthState: "unhealthy", Version: 1, UpdatedAt: "2026-01-01T00:00:00Z",
	}
	q.seed(inst)
	reader := &stubStartupReader{faults: map[string]*InstanceStartupFault{"inst-1": {Failures: 1}}}
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{startup: reader})

	rec := httptest.NewRecorder()
	if !h.writeInstanceResponseWithRedaction(context.Background(), rec, "plugin-1", inst) {
		t.Fatalf("write failed: %s", rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(parseDataResponse(t, rec), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := body["startup"]; present {
		t.Error("a mutation response carried startup")
	}
	if len(reader.asked) != 0 {
		t.Errorf("reader asked about %v on a mutation response", reader.asked)
	}
}
