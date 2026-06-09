package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// fakeInstaller is a test stub for PluginInstaller.
type fakeInstaller struct {
	returnID  string
	returnErr error
	gotPath   string
}

func (f *fakeInstaller) Install(_ context.Context, tarPath string) (string, error) {
	f.gotPath = tarPath
	return f.returnID, f.returnErr
}

// serveInstall wires the Install handler into a chi router so URL params bind
// correctly. Returns the recorded response.
func serveInstall(h *PluginHandler, body []byte) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Post("/api/v1/admin/plugins", h.Install)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// serveCreateInstance wires the CreateInstance handler into a chi router and
// sets the {id} URL param to pluginID.
func serveCreateInstance(h *PluginHandler, pluginID string, body []byte) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Post("/api/v1/admin/plugins/{id}/instances", h.CreateInstance)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/"+pluginID+"/instances", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestPluginHandler_Install(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }

	// tinyTar is a non-empty body that doesn't decode as a valid tarball.
	tinyTar := []byte("not a real tarball")

	tests := []struct {
		name          string
		body          []byte
		installer     *fakeInstaller
		seedPlugin    *db.Plugin
		wantStatus    int
		wantErrSubstr string
		wantDataID    string
	}{
		{
			name:      "happy_path: 201 with plugin ID in body",
			body:      tinyTar,
			installer: &fakeInstaller{returnID: "plg_123"},
			seedPlugin: &db.Plugin{
				ID:            "plg_123",
				Name:          "slack",
				PluginVersion: "0.1.0",
				Status:        "pending_review",
			},
			wantStatus: http.StatusCreated,
			wantDataID: "plg_123",
		},
		{
			name:          "empty_body: 400",
			body:          []byte{},
			installer:     &fakeInstaller{returnID: "x"},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "empty tarball body",
		},
		{
			name: "installer_tarball_extract_failed: 400",
			body: tinyTar,
			installer: &fakeInstaller{
				returnErr: errors.New(`extract tarball "/tmp/x": unexpected EOF`),
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "tarball extraction failed",
		},
		{
			name: "installer_cas_conflict: 409",
			body: tinyTar,
			installer: &fakeInstaller{
				returnErr: errors.New("update plugin: CAS conflict (version mismatch)"),
			},
			wantStatus:    http.StatusConflict,
			wantErrSubstr: "concurrent plugin update",
		},
		{
			name:          "signature_invalid: 422 when installer returns empty id and nil err",
			body:          tinyTar,
			installer:     &fakeInstaller{returnID: ""},
			wantStatus:    http.StatusUnprocessableEntity,
			wantErrSubstr: "plugin signature invalid",
		},
		{
			name:          "disabled: 503 when installer is nil",
			body:          tinyTar,
			installer:     nil,
			wantStatus:    http.StatusServiceUnavailable,
			wantErrSubstr: "plugin install endpoint disabled",
		},
		{
			name: "generic_failure: 500",
			body: tinyTar,
			installer: &fakeInstaller{
				returnErr: errors.New("boom"),
			},
			wantStatus:    http.StatusInternalServerError,
			wantErrSubstr: "install failed",
		},
		{
			name:      "missing_plugin_lookup: 500 when GetPluginByID returns ErrNotFound after install",
			body:      tinyTar,
			installer: &fakeInstaller{returnID: "plg_xyz"},
			// seedPlugin is nil — GetPluginByID will return ErrNotFound
			wantStatus:    http.StatusInternalServerError,
			wantErrSubstr: "install succeeded but plugin not found",
		},
		{
			name: "read_manifest_error: 400",
			body: tinyTar,
			installer: &fakeInstaller{
				returnErr: errors.New("read manifest from \"/tmp/x\": stat manifest.yaml: no such file"),
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "manifest.yaml missing",
		},
		{
			name: "manifest_name_path_traversal: 400",
			body: tinyTar,
			installer: &fakeInstaller{
				returnErr: errors.New(`manifest.name "../escape" escapes bundle directory`),
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "invalid manifest.name",
		},
		{
			name: "invalid_bundle_layout: 400",
			body: tinyTar,
			installer: &fakeInstaller{
				returnErr: errors.New(`resolve bundle root in "/tmp/x": manifest.yaml not found at bundle root or under a single top-level directory`),
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "invalid bundle layout",
		},
		{
			name: "parse_manifest_error: 400",
			body: tinyTar,
			installer: &fakeInstaller{
				returnErr: errors.New(`read manifest from "/tmp/x": parse manifest.yaml: yaml: line 3: did not find expected key`),
			},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "not valid YAML",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newFakePluginQuerier()
			if tt.seedPlugin != nil {
				q.seedPlugin(*tt.seedPlugin)
			}
			h := NewPluginHandler(q, nil, fixedClock)
			if tt.installer != nil {
				h.SetInstaller(tt.installer)
			}

			rec := serveInstall(h, tt.body)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}

			if tt.wantErrSubstr != "" {
				if !strings.Contains(rec.Body.String(), tt.wantErrSubstr) {
					t.Errorf("body = %q, want substring %q", rec.Body.String(), tt.wantErrSubstr)
				}
			}

			if tt.wantDataID != "" {
				data := parseDataResponse(t, rec)
				var resp installResponse
				if err := json.Unmarshal(data, &resp); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if resp.ID != tt.wantDataID {
					t.Errorf("data.id = %q, want %q", resp.ID, tt.wantDataID)
				}
				// Verify temp file was deleted (installer received a non-empty path).
				if tt.installer != nil && tt.installer.gotPath == "" {
					t.Error("installer was not called with a temp file path")
				}
				// Location header must be present.
				loc := rec.Header().Get("Location")
				if !strings.Contains(loc, tt.wantDataID) {
					t.Errorf("Location = %q, want it to contain %q", loc, tt.wantDataID)
				}
			}
		})
	}
}

func TestPluginHandler_CreateInstance(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }

	const pluginID = "plugin-abc"

	tests := []struct {
		name              string
		seedPlugin        bool
		body              string
		seedInstance      *db.PluginInstance // pre-existing instance to trigger duplicate
		createInstanceErr error
		wantStatus        int
		wantErrSubstr     string
		wantHealthState   string
		wantHealthDetail  string
	}{
		{
			name:             "happy_path: 201 with ULID id and unhealthy/config_missing defaults",
			seedPlugin:       true,
			body:             `{"instance_name":"slack-e2e"}`,
			wantStatus:       http.StatusCreated,
			wantHealthState:  "unhealthy",
			wantHealthDetail: "config_missing",
		},
		{
			name:          "missing_plugin: 404",
			seedPlugin:    false,
			body:          `{"instance_name":"foo"}`,
			wantStatus:    http.StatusNotFound,
			wantErrSubstr: "plugin not found",
		},
		{
			name:          "empty_name: 400",
			seedPlugin:    true,
			body:          `{"instance_name":""}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "instance_name is required",
		},
		{
			name:          "whitespace_name: 400",
			seedPlugin:    true,
			body:          `{"instance_name":"   "}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "instance_name is required",
		},
		{
			name:          "malformed_body: 400",
			seedPlugin:    true,
			body:          `not json`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "",
		},
		{
			name:       "duplicate_name: 409",
			seedPlugin: true,
			body:       `{"instance_name":"existing"}`,
			seedInstance: &db.PluginInstance{
				ID:           "existing-inst",
				PluginID:     pluginID,
				InstanceName: "existing",
				HealthState:  "unhealthy",
			},
			wantStatus:    http.StatusConflict,
			wantErrSubstr: "instance_name already exists",
		},
		{
			name:              "db_error: 500",
			seedPlugin:        true,
			body:              `{"instance_name":"new-inst"}`,
			createInstanceErr: errors.New("disk full"),
			wantStatus:        http.StatusInternalServerError,
			wantErrSubstr:     "failed to create instance",
		},
		{
			name:              "unique_constraint_race: 409",
			seedPlugin:        true,
			body:              `{"instance_name":"new-inst"}`,
			createInstanceErr: errors.New("UNIQUE constraint failed: plugin_instances.plugin_id, plugin_instances.instance_name"),
			wantStatus:        http.StatusConflict,
			wantErrSubstr:     "instance_name already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newFakePluginQuerier()
			if tt.seedPlugin {
				q.seedPlugin(db.Plugin{
					ID:            pluginID,
					Name:          "slack",
					PluginVersion: "0.1.0",
					Status:        "pending_review",
				})
			}
			if tt.seedInstance != nil {
				q.seed(*tt.seedInstance)
			}
			if tt.createInstanceErr != nil {
				q.createInstanceErr = tt.createInstanceErr
			}

			h := NewPluginHandler(q, nil, fixedClock)
			rec := serveCreateInstance(h, pluginID, []byte(tt.body))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}

			if tt.wantErrSubstr != "" {
				if !strings.Contains(rec.Body.String(), tt.wantErrSubstr) {
					t.Errorf("body = %q, want substring %q", rec.Body.String(), tt.wantErrSubstr)
				}
			}

			if tt.wantStatus == http.StatusCreated {
				data := parseDataResponse(t, rec)
				var resp createInstanceResponse
				if err := json.Unmarshal(data, &resp); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				// ID must be a ULID (26 chars).
				if len(resp.ID) != 26 {
					t.Errorf("id = %q, want 26-char ULID", resp.ID)
				}
				if resp.PluginID != pluginID {
					t.Errorf("plugin_id = %q, want %q", resp.PluginID, pluginID)
				}
				if resp.HealthState != tt.wantHealthState {
					t.Errorf("health_state = %q, want %q", resp.HealthState, tt.wantHealthState)
				}
				if tt.wantHealthDetail != "" {
					if resp.HealthDetail == nil || *resp.HealthDetail != tt.wantHealthDetail {
						t.Errorf("health_detail = %v, want %q", resp.HealthDetail, tt.wantHealthDetail)
					}
				}
				// Location header must be present and contain the instance ID.
				loc := rec.Header().Get("Location")
				if !strings.Contains(loc, resp.ID) {
					t.Errorf("Location = %q, want it to contain instance ID %q", loc, resp.ID)
				}
			}
		})
	}

	// Sub-tests that verify the spawn-on-create behaviour introduced by #402.
	// These use fakeProcessManager and run outside the table loop so they can
	// assert on the process manager's recorded calls.

	t.Run("spawn_on_create: StartByPluginID called after 201", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:            pluginID,
			Name:          "slack",
			PluginVersion: "0.1.0",
			Status:        "active",
		})
		pm := &fakeProcessManager{}
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetProcessManager(pm)

		rec := serveCreateInstance(h, pluginID, []byte(`{"instance_name":"spawn-test"}`))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
		}

		pm.mu.Lock()
		started := pm.startedByPlugin
		pm.mu.Unlock()
		if len(started) != 1 || started[0] != pluginID {
			t.Errorf("startedByPlugin = %v, want [%s]", started, pluginID)
		}
	})

	t.Run("spawn_on_create: spawn failure does not change 201 response", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:            pluginID,
			Name:          "slack",
			PluginVersion: "0.1.0",
			Status:        "active",
		})
		pm := &fakeProcessManager{startErr: errors.New("binary not found")}
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetProcessManager(pm)

		rec := serveCreateInstance(h, pluginID, []byte(`{"instance_name":"spawn-fail-test"}`))
		// The row was created; spawn failure must not retroactively change the status.
		if rec.Code != http.StatusCreated {
			t.Errorf("status = %d, want 201 even when spawn fails", rec.Code)
		}
	})

	t.Run("spawn_on_create: nil processManager does not panic", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:            pluginID,
			Name:          "slack",
			PluginVersion: "0.1.0",
			Status:        "active",
		})
		h := NewPluginHandler(q, nil, fixedClock)
		// No SetProcessManager — simulates GLEIPNIR_PLUGINS_ENABLED=false path.

		rec := serveCreateInstance(h, pluginID, []byte(`{"instance_name":"no-pm-test"}`))
		if rec.Code != http.StatusCreated {
			t.Errorf("status = %d, want 201 with nil processManager", rec.Code)
		}
	})

	t.Run("spawn_on_create: no spawn attempted on validation failure", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:            pluginID,
			Name:          "slack",
			PluginVersion: "0.1.0",
			Status:        "active",
		})
		pm := &fakeProcessManager{}
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetProcessManager(pm)

		// Empty instance_name triggers a 400 before the DB insert.
		rec := serveCreateInstance(h, pluginID, []byte(`{"instance_name":""}`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 for empty instance_name", rec.Code)
		}

		pm.mu.Lock()
		started := pm.startedByPlugin
		pm.mu.Unlock()
		if len(started) != 0 {
			t.Errorf("startedByPlugin = %v, want empty (no spawn on validation failure)", started)
		}
	})
}
