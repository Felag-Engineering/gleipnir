package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// fakePluginQuerier is an in-memory PluginQuerier for tests.
type fakePluginQuerier struct {
	instances    map[string]db.PluginInstance
	plugins      map[string]db.Plugin
	auditEvents  []db.PluginAuditEvent
	// casFailOn is the plugin ID that should return 0 rows for UpdatePluginTrustedPubkey
	// to simulate a CAS conflict.
	casFailOn    string
	updatePubkey string // last value written by UpdatePluginTrustedPubkey
}

func newFakePluginQuerier() *fakePluginQuerier {
	return &fakePluginQuerier{
		instances: make(map[string]db.PluginInstance),
		plugins:   make(map[string]db.Plugin),
	}
}

func (f *fakePluginQuerier) seed(inst db.PluginInstance) {
	f.instances[inst.ID] = inst
}

func (f *fakePluginQuerier) seedPlugin(p db.Plugin) {
	f.plugins[p.ID] = p
}

func (f *fakePluginQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	row, ok := f.instances[id]
	if !ok {
		return db.PluginInstance{}, ErrNotFound
	}
	return row, nil
}

func (f *fakePluginQuerier) GetPluginByID(_ context.Context, id string) (db.Plugin, error) {
	row, ok := f.plugins[id]
	if !ok {
		return db.Plugin{}, ErrNotFound
	}
	return row, nil
}

func (f *fakePluginQuerier) UpdatePluginTrustedPubkey(_ context.Context, arg db.UpdatePluginTrustedPubkeyParams) (int64, error) {
	if f.casFailOn == arg.ID {
		return 0, nil // simulate CAS conflict
	}
	p, ok := f.plugins[arg.ID]
	if !ok {
		return 0, nil
	}
	p.TrustedPubkey = arg.TrustedPubkey
	p.Version++
	f.plugins[arg.ID] = p
	f.updatePubkey = arg.TrustedPubkey
	return 1, nil
}

func (f *fakePluginQuerier) ListPluginInstancesByPlugin(_ context.Context, pluginID string) ([]db.PluginInstance, error) {
	var result []db.PluginInstance
	for _, inst := range f.instances {
		if inst.PluginID == pluginID {
			result = append(result, inst)
		}
	}
	return result, nil
}

func (f *fakePluginQuerier) UpdatePluginInstanceHealth(_ context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error) {
	inst, ok := f.instances[arg.ID]
	if !ok || inst.Version != arg.ExpectedVersion {
		return 0, nil
	}
	inst.HealthState = arg.HealthState
	inst.HealthDetail = arg.HealthDetail
	inst.Version++
	f.instances[arg.ID] = inst
	return 1, nil
}

func (f *fakePluginQuerier) InsertPluginAuditEvent(_ context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	ev := db.PluginAuditEvent{
		ID:               int64(len(f.auditEvents)),
		PluginInstanceID: arg.PluginInstanceID,
		EventType:        arg.EventType,
		Severity:         arg.Severity,
		ActorUserID:      arg.ActorUserID,
		PayloadJson:      arg.PayloadJson,
		CreatedAt:        arg.CreatedAt,
	}
	f.auditEvents = append(f.auditEvents, ev)
	return ev, nil
}

func TestPluginHandler_GetInstance(t *testing.T) {
	detail := "verified by host"

	tests := []struct {
		name         string
		pluginID     string
		instanceID   string
		seed         *db.PluginInstance // nil means don't seed
		wantStatus   int
		wantState    string
		wantPluginID string
	}{
		{
			name:       "200 happy path",
			pluginID:   "plugin-1",
			instanceID: "inst-1",
			seed: &db.PluginInstance{
				ID:           "inst-1",
				PluginID:     "plugin-1",
				InstanceName: "prod",
				HealthState:  "healthy",
				HealthDetail: &detail,
				Version:      3,
				UpdatedAt:    "2024-01-01T00:00:00Z",
			},
			wantStatus:   http.StatusOK,
			wantState:    "healthy",
			wantPluginID: "plugin-1",
		},
		{
			name:       "404 instance not found",
			pluginID:   "plugin-1",
			instanceID: "inst-missing",
			seed:       nil,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "404 mismatched plugin_id",
			pluginID:   "plugin-X",
			instanceID: "inst-1",
			seed: &db.PluginInstance{
				ID:           "inst-1",
				PluginID:     "plugin-1", // belongs to a different plugin
				InstanceName: "prod",
				HealthState:  "healthy",
				Version:      0,
				UpdatedAt:    "2024-01-01T00:00:00Z",
			},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newFakePluginQuerier()
			if tt.seed != nil {
				q.seed(*tt.seed)
			}
			h := NewPluginHandler(q, nil, nil)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/"+tt.pluginID+"/instances/"+tt.instanceID, nil)
			req = withChiParams(req, map[string]string{"id": tt.pluginID, "iid": tt.instanceID})
			rec := httptest.NewRecorder()
			h.GetInstance(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}

			if tt.wantStatus != http.StatusOK {
				return
			}

			data := parseDataResponse(t, rec)
			var resp instanceResponse
			if err := json.Unmarshal(data, &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if resp.State != tt.wantState {
				t.Errorf("state = %q, want %q", resp.State, tt.wantState)
			}
			if resp.PluginID != tt.wantPluginID {
				t.Errorf("plugin_id = %q, want %q", resp.PluginID, tt.wantPluginID)
			}
			if resp.ID != tt.instanceID {
				t.Errorf("id = %q, want %q", resp.ID, tt.instanceID)
			}
		})
	}
}

// makeTestPubkey generates a valid Minisign public key and returns its bytes
// and the base64-encoded form suitable for the accept-new-key request body.
func makeTestPubkey(t *testing.T) (rawBytes []byte, b64 string) {
	t.Helper()
	pk, _, err := signing.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	raw := signing.MarshalPublicKey(pk, "test key")
	return raw, base64.StdEncoding.EncodeToString(raw)
}

func TestPluginHandler_AcceptNewKey(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }

	t.Run("rotates trusted_pubkey and emits audit event", func(t *testing.T) {
		q := newFakePluginQuerier()
		oldPubkey, _ := makeTestPubkey(t)
		q.seedPlugin(db.Plugin{
			ID:            "plugin-1",
			Name:          "my-plugin",
			TrustedPubkey: string(oldPubkey),
			Version:       2,
		})

		_, newB64 := makeTestPubkey(t)
		body := fmt.Sprintf(`{"candidate_pubkey": %q}`, newB64)

		h := NewPluginHandler(q, nil, fixedClock)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-1/accept-new-key", bytes.NewBufferString(body))
		req = withChiParams(req, map[string]string{"id": "plugin-1"})
		rec := httptest.NewRecorder()
		h.AcceptNewKey(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		// Check audit event was emitted.
		if len(q.auditEvents) == 0 {
			t.Fatal("expected audit event, got none")
		}
		if q.auditEvents[0].EventType != "plugin_pubkey_rotated" {
			t.Errorf("audit event type = %q, want plugin_pubkey_rotated", q.auditEvents[0].EventType)
		}
		if q.auditEvents[0].Severity != "high" {
			t.Errorf("audit severity = %q, want high", q.auditEvents[0].Severity)
		}

		// Check pubkey was updated.
		newRaw, _ := base64.StdEncoding.DecodeString(newB64)
		if q.updatePubkey != string(newRaw) {
			t.Errorf("updatePubkey mismatch: got %q, want decoded pubkey bytes", q.updatePubkey)
		}
	})

	t.Run("transitions pending_key_approval instances to healthy", func(t *testing.T) {
		q := newFakePluginQuerier()
		oldPubkey, _ := makeTestPubkey(t)
		q.seedPlugin(db.Plugin{
			ID:            "plugin-2",
			Name:          "blocked-plugin",
			TrustedPubkey: string(oldPubkey),
			Version:       1,
		})
		q.seed(db.PluginInstance{
			ID:          "inst-a",
			PluginID:    "plugin-2",
			HealthState: string(model.PluginHealthStatePendingKeyApproval),
			Version:     0,
		})
		q.seed(db.PluginInstance{
			ID:          "inst-b",
			PluginID:    "plugin-2",
			HealthState: string(model.PluginHealthStateHealthy), // already healthy — should not be touched
			Version:     0,
		})

		_, newB64 := makeTestPubkey(t)
		body := fmt.Sprintf(`{"candidate_pubkey": %q}`, newB64)

		h := NewPluginHandler(q, nil, fixedClock)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-2/accept-new-key", bytes.NewBufferString(body))
		req = withChiParams(req, map[string]string{"id": "plugin-2"})
		rec := httptest.NewRecorder()
		h.AcceptNewKey(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		data := parseDataResponse(t, rec)
		var resp acceptNewKeyResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.InstancesUnblocked != 1 {
			t.Errorf("instances_unblocked = %d, want 1", resp.InstancesUnblocked)
		}

		// inst-a should be healthy now.
		instA := q.instances["inst-a"]
		if instA.HealthState != string(model.PluginHealthStateHealthy) {
			t.Errorf("inst-a health_state = %q, want healthy", instA.HealthState)
		}
		// inst-b should remain unchanged.
		instB := q.instances["inst-b"]
		if instB.HealthState != string(model.PluginHealthStateHealthy) {
			t.Errorf("inst-b health_state = %q, want healthy (unchanged)", instB.HealthState)
		}
	})

	t.Run("rejects malformed candidate_pubkey with 400", func(t *testing.T) {
		q := newFakePluginQuerier()
		oldPubkey, _ := makeTestPubkey(t)
		q.seedPlugin(db.Plugin{ID: "plugin-3", Name: "p", TrustedPubkey: string(oldPubkey), Version: 0})

		h := NewPluginHandler(q, nil, fixedClock)

		// Not valid base64.
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"candidate_pubkey": "not-valid-base64!!!"}`))
		req = withChiParams(req, map[string]string{"id": "plugin-3"})
		rec := httptest.NewRecorder()
		h.AcceptNewKey(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 for invalid base64", rec.Code)
		}

		// Valid base64 but not a Minisign pubkey.
		garbage := base64.StdEncoding.EncodeToString([]byte("not a pubkey"))
		req2 := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(fmt.Sprintf(`{"candidate_pubkey": %q}`, garbage)))
		req2 = withChiParams(req2, map[string]string{"id": "plugin-3"})
		rec2 := httptest.NewRecorder()
		h.AcceptNewKey(rec2, req2)
		if rec2.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 for invalid pubkey format", rec2.Code)
		}
	})

	t.Run("returns 404 on unknown plugin id", func(t *testing.T) {
		q := newFakePluginQuerier()
		_, newB64 := makeTestPubkey(t)
		body := fmt.Sprintf(`{"candidate_pubkey": %q}`, newB64)

		h := NewPluginHandler(q, nil, fixedClock)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
		req = withChiParams(req, map[string]string{"id": "nonexistent"})
		rec := httptest.NewRecorder()
		h.AcceptNewKey(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for unknown plugin", rec.Code)
		}
	})

	t.Run("CAS conflict surfaces as 409", func(t *testing.T) {
		q := newFakePluginQuerier()
		oldPubkey, _ := makeTestPubkey(t)
		q.seedPlugin(db.Plugin{ID: "plugin-4", Name: "p", TrustedPubkey: string(oldPubkey), Version: 0})
		q.casFailOn = "plugin-4" // trigger CAS miss

		_, newB64 := makeTestPubkey(t)
		body := fmt.Sprintf(`{"candidate_pubkey": %q}`, newB64)

		h := NewPluginHandler(q, nil, fixedClock)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
		req = withChiParams(req, map[string]string{"id": "plugin-4"})
		rec := httptest.NewRecorder()
		h.AcceptNewKey(rec, req)

		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 for CAS conflict", rec.Code)
		}
	})
}

// withChiParams sets multiple chi URL params on a request in a single route
// context so none are lost when chaining (withChiParam creates a new context
// each time, which would overwrite any previously set params).
func withChiParams(r *http.Request, params map[string]string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}
