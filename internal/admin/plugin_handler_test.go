package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// fakePluginQuerier is an in-memory PluginQuerier for tests.
type fakePluginQuerier struct {
	instances   map[string]db.PluginInstance
	plugins     map[string]db.Plugin
	auditEvents []db.PluginAuditEvent
	policies    []db.Policy
	// audienceEntries maps instance_id → list of audience entries for that instance.
	audienceEntries map[string][]db.ListAudienceEntriesByInstanceRow
	// casFailOn is the plugin ID that should return 0 rows for UpdatePluginTrustedPubkey
	// or UpdatePluginManifest to simulate a CAS conflict.
	casFailOn        string
	updatePubkey     string // last value written by UpdatePluginTrustedPubkey
	scopeCASFailOnID  string // instance ID that should return 0 rows for UpdatePluginInstanceSubscriptionScope
	configCASFailOnID string // instance ID that should return 0 rows for UpdatePluginInstanceConfig
	createInstanceErr error  // if non-nil, CreatePluginInstance returns this error
	// deletedInstanceIDs tracks which instance IDs were deleted.
	deletedInstanceIDs []string
	// deletedPluginIDs tracks which plugin IDs were deleted.
	deletedPluginIDs []string
}

func newFakePluginQuerier() *fakePluginQuerier {
	return &fakePluginQuerier{
		instances:       make(map[string]db.PluginInstance),
		plugins:         make(map[string]db.Plugin),
		audienceEntries: make(map[string][]db.ListAudienceEntriesByInstanceRow),
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

func (f *fakePluginQuerier) ListPluginAuditEventsByType(_ context.Context, arg db.ListPluginAuditEventsByTypeParams) ([]db.PluginAuditEvent, error) {
	var result []db.PluginAuditEvent
	for i := len(f.auditEvents) - 1; i >= 0; i-- {
		ev := f.auditEvents[i]
		if ev.EventType == arg.EventType {
			result = append(result, ev)
		}
		if int64(len(result)) >= arg.Limit {
			break
		}
	}
	return result, nil
}

func (f *fakePluginQuerier) UpdatePluginInstanceSubscriptionScope(_ context.Context, arg db.UpdatePluginInstanceSubscriptionScopeParams) (int64, error) {
	if f.scopeCASFailOnID == arg.ID {
		return 0, nil // simulate CAS conflict
	}
	inst, ok := f.instances[arg.ID]
	if !ok || inst.Version != arg.ExpectedVersion {
		return 0, nil
	}
	inst.SubscriptionScopeJson = arg.SubscriptionScopeJson
	inst.UpdatedAt = arg.UpdatedAt
	inst.Version++
	f.instances[arg.ID] = inst
	return 1, nil
}

func (f *fakePluginQuerier) UpdatePluginManifest(_ context.Context, arg db.UpdatePluginManifestParams) (int64, error) {
	if f.casFailOn == arg.ID {
		return 0, nil // simulate CAS conflict
	}
	p, ok := f.plugins[arg.ID]
	if !ok || p.Version != arg.ExpectedVersion {
		return 0, nil
	}
	p.ManifestSnapshot = arg.ManifestSnapshot
	p.PluginVersion = arg.PluginVersion
	p.Status = arg.Status
	p.Version++
	f.plugins[arg.ID] = p
	return 1, nil
}

func (f *fakePluginQuerier) CreatePluginInstance(_ context.Context, arg db.CreatePluginInstanceParams) (db.PluginInstance, error) {
	if f.createInstanceErr != nil {
		return db.PluginInstance{}, f.createInstanceErr
	}
	inst := db.PluginInstance{
		ID:                    arg.ID,
		PluginID:              arg.PluginID,
		InstanceName:          arg.InstanceName,
		ConfigJson:            arg.ConfigJson,
		SubscriptionScopeJson: arg.SubscriptionScopeJson,
		CredentialsEncrypted:  arg.CredentialsEncrypted,
		CredentialsExpiresAt:  arg.CredentialsExpiresAt,
		HandshakeVersions:     arg.HandshakeVersions,
		HealthState:           arg.HealthState,
		HealthDetail:          arg.HealthDetail,
		LastOauthCallbackUrl:  arg.LastOauthCallbackUrl,
		Version:               0,
		CreatedAt:             arg.CreatedAt,
		UpdatedAt:             arg.UpdatedAt,
	}
	f.instances[inst.ID] = inst
	return inst, nil
}

func (f *fakePluginQuerier) GetPluginInstanceByName(_ context.Context, arg db.GetPluginInstanceByNameParams) (db.PluginInstance, error) {
	for _, inst := range f.instances {
		if inst.PluginID == arg.PluginID && inst.InstanceName == arg.InstanceName {
			return inst, nil
		}
	}
	return db.PluginInstance{}, ErrNotFound
}

func (f *fakePluginQuerier) UpdatePluginInstanceConfig(_ context.Context, arg db.UpdatePluginInstanceConfigParams) (int64, error) {
	if f.configCASFailOnID == arg.ID {
		return 0, nil // simulate CAS conflict
	}
	inst, ok := f.instances[arg.ID]
	if !ok || inst.Version != arg.ExpectedVersion {
		return 0, nil
	}
	inst.ConfigJson = arg.ConfigJson
	inst.UpdatedAt = arg.UpdatedAt
	inst.Version++
	f.instances[arg.ID] = inst
	return 1, nil
}

func (f *fakePluginQuerier) ListAudienceEntriesByInstance(_ context.Context, pluginInstanceID string) ([]db.ListAudienceEntriesByInstanceRow, error) {
	return f.audienceEntries[pluginInstanceID], nil
}

func (f *fakePluginQuerier) DeletePluginPendingRequestsByInstance(_ context.Context, pluginInstanceID string) error {
	return nil
}

func (f *fakePluginQuerier) DeletePluginOAuthNoncesByInstance(_ context.Context, instanceID string) error {
	return nil
}

func (f *fakePluginQuerier) DeletePlugin(_ context.Context, id string) (int64, error) {
	if _, ok := f.plugins[id]; !ok {
		return 0, nil
	}
	// Cascade: delete all instances belonging to this plugin.
	for instID, inst := range f.instances {
		if inst.PluginID == id {
			delete(f.instances, instID)
		}
	}
	delete(f.plugins, id)
	f.deletedPluginIDs = append(f.deletedPluginIDs, id)
	return 1, nil
}

func (f *fakePluginQuerier) DeletePluginInstance(_ context.Context, id string) (int64, error) {
	if _, ok := f.instances[id]; !ok {
		return 0, nil
	}
	delete(f.instances, id)
	f.deletedInstanceIDs = append(f.deletedInstanceIDs, id)
	return 1, nil
}

func (f *fakePluginQuerier) ListPolicies(_ context.Context) ([]db.Policy, error) {
	return f.policies, nil
}

// seedAudienceEntries registers audience entry rows for an instance, used in
// reference-guard tests.
func (f *fakePluginQuerier) seedAudienceEntries(instanceID string, entries []db.ListAudienceEntriesByInstanceRow) {
	f.audienceEntries[instanceID] = entries
}

// seedPolicy adds a policy to the fake's policy list.
func (f *fakePluginQuerier) seedPolicy(p db.Policy) {
	f.policies = append(f.policies, p)
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
				ConfigJson:   "{}",
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
				ConfigJson:   "{}",
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
				// Seed a matching plugin so GetPluginByID succeeds.
				q.seedPlugin(db.Plugin{
					ID:               tt.pluginID,
					Name:             "test-plugin",
					ManifestSnapshot: instanceConfigManifestNoSchema,
				})
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

// instanceConfigManifestWithSecret is a manifest whose config_schema marks
// app_level_token as x-gleipnir-secret: true. Used by GET and PUT secret tests.
const instanceConfigManifestWithSecret = "schema_version: v1\nname: test-plugin\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\nconfig_schema:\n  type: object\n  properties:\n    app_level_token:\n      type: string\n      x-gleipnir-secret: true\n  required:\n    - app_level_token\n"

func TestGetInstance_RedactsSecretConfigField(t *testing.T) {
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestWithSecret,
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:           "inst-1",
		PluginID:     "plugin-1",
		InstanceName: "prod",
		ConfigJson:   `{"app_level_token":"xapp-real-token","other":"value"}`,
		HealthState:  "healthy",
		Version:      1,
		UpdatedAt:    "2026-01-01T00:00:00Z",
	})

	h := NewPluginHandler(q, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.GetInstance(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var resp instanceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	// Secret field must be redacted.
	var cfg map[string]any
	if err := json.Unmarshal([]byte(resp.ConfigJson), &cfg); err != nil {
		t.Fatalf("unmarshal config_json: %v", err)
	}
	if cfg["app_level_token"] != "***" {
		t.Errorf("app_level_token = %v, want %q (redacted)", cfg["app_level_token"], "***")
	}
	// Non-secret field must be preserved.
	if cfg["other"] != "value" {
		t.Errorf("other = %v, want %q (preserved)", cfg["other"], "value")
	}
}

func TestGetInstance_ManifestParseFails_500(t *testing.T) {
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: "not yaml: ::::", // deliberately malformed
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  `{"app_level_token":"xapp-real-token"}`,
		HealthState: "healthy",
		Version:     0,
		UpdatedAt:   "2026-01-01T00:00:00Z",
	})

	h := NewPluginHandler(q, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.GetInstance(rec, req)

	// Fail-closed: must return 500, never the unredacted config (ADR-049 §6).
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 on malformed manifest", rec.Code)
	}
}

func TestPutInstanceConfig_RejectsSentinelInSecretField(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestWithSecret,
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  `{"app_level_token":"xapp-real"}`,
		HealthState: "healthy",
		Version:     0,
	})

	h := NewPluginHandler(q, nil, fixedClock)
	// Submitting "***" for a secret field must be rejected.
	body := `{"config":{"app_level_token":"***"},"expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for sentinel in secret field; body: %s", rec.Code, rec.Body.String())
	}
}

func TestPutInstanceConfig_AllowsSentinelInNonSecretField(t *testing.T) {
	// "***" is a valid value for non-secret fields (it is just a string).
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	// Use a manifest with no secret fields.
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestNoSchema,
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  "{}",
		HealthState: "healthy",
		Version:     0,
	})

	h := NewPluginHandler(q, nil, fixedClock)
	body := `{"config":{"any_field":"***"},"expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; sentinel is allowed in non-secret fields; body: %s", rec.Code, rec.Body.String())
	}
}

func TestPutInstanceConfig_ResponseRedactsWrittenSecret(t *testing.T) {
	// Re-fetch branch: secret must be redacted in the response after a
	// successful write when the post-write GetPluginInstanceByID succeeds.
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestWithSecret,
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  "{}",
		HealthState: "healthy",
		Version:     0,
		UpdatedAt:   "2026-01-01T00:00:00Z",
	})

	h := NewPluginHandler(q, nil, fixedClock)
	body := `{"config":{"app_level_token":"xapp-real-secret"},"expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var resp instanceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(resp.ConfigJson), &cfg); err != nil {
		t.Fatalf("unmarshal config_json: %v", err)
	}
	if cfg["app_level_token"] != "***" {
		t.Errorf("re-fetch branch: app_level_token = %v, want %q (redacted)", cfg["app_level_token"], "***")
	}
}

// failOnSecondGetInstance wraps fakePluginQuerier and fails the second
// GetPluginInstanceByID call for the given instance ID to force the fallback
// synthesized-response branch.
type failOnSecondGetInstance struct {
	*fakePluginQuerier
	targetID string
	callCount int
}

func (f *failOnSecondGetInstance) GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error) {
	if id == f.targetID {
		f.callCount++
		if f.callCount >= 2 {
			return db.PluginInstance{}, fmt.Errorf("simulated re-fetch failure")
		}
	}
	return f.fakePluginQuerier.GetPluginInstanceByID(ctx, id)
}

func TestPutInstanceConfig_FallbackResponseRedactsWrittenSecret(t *testing.T) {
	// Fallback branch: when the post-write re-fetch fails, the synthesized
	// response must still redact secret fields (ADR-049 §7).
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	base := newFakePluginQuerier()
	base.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestWithSecret,
		Version:          0,
	})
	base.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  "{}",
		HealthState: "healthy",
		Version:     0,
		UpdatedAt:   "2026-01-01T00:00:00Z",
	})

	q := &failOnSecondGetInstance{fakePluginQuerier: base, targetID: "inst-1"}
	h := NewPluginHandler(q, nil, fixedClock)

	body := `{"config":{"app_level_token":"xapp-real-secret"},"expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fallback path); body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var resp instanceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(resp.ConfigJson), &cfg); err != nil {
		t.Fatalf("unmarshal config_json: %v", err)
	}
	if cfg["app_level_token"] != "***" {
		t.Errorf("fallback branch: app_level_token = %v, want %q (redacted)", cfg["app_level_token"], "***")
	}
}

// ── PutInstanceConfigProperty tests ──────────────────────────────────────────

func TestPutInstanceConfigProperty_HappyPath(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestWithSecret,
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  "{}",
		HealthState: "healthy",
		Version:     0,
		UpdatedAt:   "2026-01-01T00:00:00Z",
	})

	h := NewPluginHandler(q, nil, fixedClock)
	body := `{"value":"xapp-new-real-token","expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1", "property": "app_level_token"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfigProperty(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	// The stored config must have the real token.
	inst := q.instances["inst-1"]
	var stored map[string]any
	if err := json.Unmarshal([]byte(inst.ConfigJson), &stored); err != nil {
		t.Fatalf("unmarshal stored config: %v", err)
	}
	if stored["app_level_token"] != "xapp-new-real-token" {
		t.Errorf("stored app_level_token = %v, want %q", stored["app_level_token"], "xapp-new-real-token")
	}

	// The response must redact the secret.
	data := parseDataResponse(t, rec)
	var resp instanceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(resp.ConfigJson), &cfg); err != nil {
		t.Fatalf("unmarshal response config_json: %v", err)
	}
	if cfg["app_level_token"] != "***" {
		t.Errorf("response app_level_token = %v, want %q (redacted)", cfg["app_level_token"], "***")
	}
}

func TestPutInstanceConfigProperty_UnknownProperty(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestWithSecret,
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  "{}",
		HealthState: "healthy",
		Version:     0,
	})

	h := NewPluginHandler(q, nil, fixedClock)
	body := `{"value":"whatever","expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1", "property": "nonexistent_field"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfigProperty(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for unknown property; body: %s", rec.Code, rec.Body.String())
	}
}

func TestPutInstanceConfigProperty_RejectsSentinelValue(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestWithSecret,
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  `{"app_level_token":"xapp-real"}`,
		HealthState: "healthy",
		Version:     0,
	})

	h := NewPluginHandler(q, nil, fixedClock)
	body := `{"value":"***","expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1", "property": "app_level_token"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfigProperty(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for sentinel value; body: %s", rec.Code, rec.Body.String())
	}
}

func TestPutInstanceConfigProperty_VersionConflict(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestWithSecret,
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  "{}",
		HealthState: "healthy",
		Version:     5, // real version
	})
	q.configCASFailOnID = "inst-1"

	h := NewPluginHandler(q, nil, fixedClock)
	body := `{"value":"xapp-new","expected_version":5}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1", "property": "app_level_token"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfigProperty(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for CAS conflict; body: %s", rec.Code, rec.Body.String())
	}
}

func TestPutInstanceConfigProperty_FallbackResponseRedactsWrittenSecret(t *testing.T) {
	// Fallback branch: when the post-write re-fetch fails, the synthesized
	// response must still redact secret fields (ADR-049 §7).
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	base := newFakePluginQuerier()
	base.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestWithSecret,
		Version:          0,
	})
	base.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  "{}",
		HealthState: "healthy",
		Version:     0,
		UpdatedAt:   "2026-01-01T00:00:00Z",
	})

	q := &failOnSecondGetInstance{fakePluginQuerier: base, targetID: "inst-1"}
	h := NewPluginHandler(q, nil, fixedClock)

	body := `{"value":"xapp-real-secret","expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1", "property": "app_level_token"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfigProperty(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fallback path); body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var resp instanceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(resp.ConfigJson), &cfg); err != nil {
		t.Fatalf("unmarshal config_json: %v", err)
	}
	if cfg["app_level_token"] != "***" {
		t.Errorf("fallback branch: app_level_token = %v, want %q (redacted)", cfg["app_level_token"], "***")
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

// candidateManifestAuditEvent builds a fake plugin_manifest_material_change audit
// event payload containing the given candidate manifest bytes for the given plugin.
func candidateManifestAuditEvent(pluginID string, candidateManifest []byte) db.PluginAuditEvent {
	payload, _ := json.Marshal(map[string]any{
		"plugin_id":                    pluginID,
		"name":                         "test-plugin",
		"old_version":                  "1.0.0",
		"new_version":                  "2.0.0",
		"material_fields":              []string{"services.tool"},
		"cosmetic_fields":              []string{},
		"candidate_manifest_b64":       base64.StdEncoding.EncodeToString(candidateManifest),
		"newly_required_config_fields": []string{},
	})
	return db.PluginAuditEvent{
		EventType:   "plugin_manifest_material_change",
		Severity:    "high",
		PayloadJson: string(payload),
		CreatedAt:   "2026-05-05T00:00:00Z",
	}
}

const (
	// v1 manifest for accept-manifest tests.
	v1ManifestYAML = "schema_version: v1\nname: test-plugin\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n"
	// v2 manifest for accept-manifest tests (material change: services.tool v1→v2).
	v2ManifestYAML = "schema_version: v1\nname: test-plugin\nversion: 2.0.0\nservices:\n  tool: v2\nauth:\n  mode: instance_credentials\n  strategy: none\n"
	// v2 manifest with a newly required config field.
	v2ManifestWithRequiredField = "schema_version: v1\nname: test-plugin\nversion: 2.0.0\nservices:\n  tool: v2\nauth:\n  mode: instance_credentials\n  strategy: none\nconfig_schema:\n  type: object\n  properties:\n    api_key:\n      type: string\n  required:\n    - api_key\n"

	// triggerManifestWithScope is a minimal TriggerService manifest with subscription_schema.
	triggerManifestWithScope = "schema_version: v1\nname: trigger-plugin\nversion: 1.0.0\nservices:\n  trigger: v1\nauth:\n  mode: instance_credentials\n  strategy: none\nsubscription_schema:\n  type: object\n  additionalProperties: false\n  required:\n    - channels\n  properties:\n    channels:\n      type: array\n      items:\n        type: string\n"
	// triggerManifestNoScope is a TriggerService manifest without subscription_schema.
	triggerManifestNoScope = "schema_version: v1\nname: trigger-plugin\nversion: 1.0.0\nservices:\n  trigger: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n"
)

func TestPluginHandler_AcceptManifest(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }

	t.Run("happy path: updates snapshot and unblocks instances", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-1",
			Name:             "test-plugin",
			ManifestSnapshot: v1ManifestYAML,
			PluginVersion:    "1.0.0",
			Status:           "pending_review",
			Version:          1,
		})
		q.seed(db.PluginInstance{
			ID:          "inst-a",
			PluginID:    "plugin-1",
			HealthState: string(model.PluginHealthStatePendingManifestApproval),
			Version:     0,
		})
		// Seed the audit event as if a material change was previously detected.
		q.auditEvents = append(q.auditEvents, candidateManifestAuditEvent("plugin-1", []byte(v2ManifestYAML)))

		h := NewPluginHandler(q, nil, fixedClock)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-1/accept-manifest", bytes.NewBufferString(`{}`))
		req = withChiParams(req, map[string]string{"id": "plugin-1"})
		rec := httptest.NewRecorder()
		h.AcceptManifest(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		data := parseDataResponse(t, rec)
		var resp acceptManifestResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.AcceptedManifestVersion != "2.0.0" {
			t.Errorf("accepted_manifest_version = %q, want 2.0.0", resp.AcceptedManifestVersion)
		}
		if resp.InstancesUnblocked != 1 {
			t.Errorf("instances_unblocked = %d, want 1", resp.InstancesUnblocked)
		}
		if resp.InstancesPendingConfig != 0 {
			t.Errorf("instances_pending_config = %d, want 0", resp.InstancesPendingConfig)
		}

		// Instance must now be healthy.
		instA := q.instances["inst-a"]
		if instA.HealthState != string(model.PluginHealthStateHealthy) {
			t.Errorf("inst-a health_state = %q, want healthy", instA.HealthState)
		}
		// Snapshot must be updated.
		if q.plugins["plugin-1"].ManifestSnapshot != v2ManifestYAML {
			t.Error("manifest_snapshot must be updated to v2 on accept")
		}
	})

	t.Run("newly required config field transitions to pending_config_migration", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-2",
			Name:             "test-plugin",
			ManifestSnapshot: v1ManifestYAML,
			PluginVersion:    "1.0.0",
			Status:           "pending_review",
			Version:          1,
		})
		q.seed(db.PluginInstance{
			ID:          "inst-b",
			PluginID:    "plugin-2",
			HealthState: string(model.PluginHealthStatePendingManifestApproval),
			Version:     0,
		})
		q.auditEvents = append(q.auditEvents, candidateManifestAuditEvent("plugin-2", []byte(v2ManifestWithRequiredField)))

		h := NewPluginHandler(q, nil, fixedClock)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-2/accept-manifest", bytes.NewBufferString(`{}`))
		req = withChiParams(req, map[string]string{"id": "plugin-2"})
		rec := httptest.NewRecorder()
		h.AcceptManifest(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		data := parseDataResponse(t, rec)
		var resp acceptManifestResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.InstancesPendingConfig != 1 {
			t.Errorf("instances_pending_config = %d, want 1", resp.InstancesPendingConfig)
		}
		if resp.InstancesUnblocked != 0 {
			t.Errorf("instances_unblocked = %d, want 0", resp.InstancesUnblocked)
		}

		instB := q.instances["inst-b"]
		if instB.HealthState != string(model.PluginHealthStatePendingConfigMigration) {
			t.Errorf("inst-b health_state = %q, want pending_config_migration", instB.HealthState)
		}
	})

	t.Run("no pending change returns 409", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-3", Name: "test-plugin", ManifestSnapshot: v1ManifestYAML, Version: 0})

		h := NewPluginHandler(q, nil, fixedClock)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-3/accept-manifest", bytes.NewBufferString(`{}`))
		req = withChiParams(req, map[string]string{"id": "plugin-3"})
		rec := httptest.NewRecorder()
		h.AcceptManifest(rec, req)

		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 when no pending change", rec.Code)
		}
	})

	t.Run("plugin not found returns 404", func(t *testing.T) {
		q := newFakePluginQuerier()

		h := NewPluginHandler(q, nil, fixedClock)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/nonexistent/accept-manifest", bytes.NewBufferString(`{}`))
		req = withChiParams(req, map[string]string{"id": "nonexistent"})
		rec := httptest.NewRecorder()
		h.AcceptManifest(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for unknown plugin", rec.Code)
		}
	})

	t.Run("CAS conflict on UpdatePluginManifest returns 409", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-4",
			Name:             "test-plugin",
			ManifestSnapshot: v1ManifestYAML,
			PluginVersion:    "1.0.0",
			Version:          0,
		})
		q.auditEvents = append(q.auditEvents, candidateManifestAuditEvent("plugin-4", []byte(v2ManifestYAML)))
		q.casFailOn = "plugin-4" // trigger CAS miss on UpdatePluginManifest

		h := NewPluginHandler(q, nil, fixedClock)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-4/accept-manifest", bytes.NewBufferString(`{}`))
		req = withChiParams(req, map[string]string{"id": "plugin-4"})
		rec := httptest.NewRecorder()
		h.AcceptManifest(rec, req)

		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 for CAS conflict", rec.Code)
		}
	})

	t.Run("emits plugin_manifest_accepted audit event with actor", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-5",
			Name:             "test-plugin",
			ManifestSnapshot: v1ManifestYAML,
			PluginVersion:    "1.0.0",
			Status:           "pending_review",
			Version:          1,
		})
		q.auditEvents = append(q.auditEvents, candidateManifestAuditEvent("plugin-5", []byte(v2ManifestYAML)))

		h := NewPluginHandler(q, nil, fixedClock)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-5/accept-manifest", bytes.NewBufferString(`{}`))
		req = withChiParams(req, map[string]string{"id": "plugin-5"})
		// Inject an authenticated user so AcceptManifest can record actor_user_id.
		req = req.WithContext(auth.WithUserContext(req.Context(), "user-admin-1", "admin", []string{"admin"}))
		rec := httptest.NewRecorder()
		h.AcceptManifest(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		var found *db.PluginAuditEvent
		for i := range q.auditEvents {
			if q.auditEvents[i].EventType == "plugin_manifest_accepted" {
				found = &q.auditEvents[i]
				break
			}
		}
		if found == nil {
			t.Fatal("expected a plugin_manifest_accepted audit event, got none")
		}
		if found.Severity != "info" {
			t.Errorf("audit severity = %q, want info", found.Severity)
		}
		wantActorID := "user-admin-1"
		if found.ActorUserID == nil || *found.ActorUserID != wantActorID {
			t.Errorf("actor_user_id = %v, want %q", found.ActorUserID, wantActorID)
		}
	})
}

// ── PutSubscriptionScope tests ────────────────────────────────────────────────

// fakeTriggerRestarter records Restart calls for assertion in tests.
type fakeTriggerRestarter struct {
	mu         sync.Mutex
	restartIDs []string
}

func (f *fakeTriggerRestarter) Restart(_ context.Context, instanceID string) {
	f.mu.Lock()
	f.restartIDs = append(f.restartIDs, instanceID)
	f.mu.Unlock()
}

func (f *fakeTriggerRestarter) restarts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.restartIDs))
	copy(out, f.restartIDs)
	return out
}

func TestPluginHandler_PutSubscriptionScope(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }

	t.Run("200 happy path: valid scope, correct version", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-1",
			Name:             "trigger-plugin",
			ManifestSnapshot: triggerManifestWithScope,
			Version:          0,
		})
		q.seed(db.PluginInstance{
			ID:                    "inst-1",
			PluginID:              "plugin-1",
			InstanceName:          "prod",
			SubscriptionScopeJson: "{}",
			HealthState:           "healthy",
			Version:               2,
			UpdatedAt:             "2026-05-01T00:00:00Z",
		})

		restarter := &fakeTriggerRestarter{}
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetTriggerRestarter(restarter)

		body := `{"scope":{"channels":["#incidents","#ops"]},"expected_version":2}`
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
		req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
		rec := httptest.NewRecorder()
		h.PutSubscriptionScope(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		// Querier must have persisted the scope.
		inst := q.instances["inst-1"]
		if inst.SubscriptionScopeJson == "{}" {
			t.Error("subscription_scope_json not updated in querier")
		}

		// Restarter must have been called.
		if restarts := restarter.restarts(); len(restarts) != 1 || restarts[0] != "inst-1" {
			t.Errorf("expected Restart(inst-1), got %v", restarts)
		}
	})

	t.Run("400 missing expected_version", func(t *testing.T) {
		q := newFakePluginQuerier()
		h := NewPluginHandler(q, nil, fixedClock)
		restarter := &fakeTriggerRestarter{}
		h.SetTriggerRestarter(restarter)

		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(`{"scope":{}}`))
		req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
		rec := httptest.NewRecorder()
		h.PutSubscriptionScope(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
		if len(restarter.restarts()) != 0 {
			t.Error("restarter must not be called when request is invalid")
		}
	})

	t.Run("400 manifest has no subscription_schema", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-2",
			Name:             "trigger-plugin",
			ManifestSnapshot: triggerManifestNoScope,
			Version:          0,
		})
		q.seed(db.PluginInstance{
			ID:          "inst-2",
			PluginID:    "plugin-2",
			HealthState: "healthy",
			Version:     0,
		})

		restarter := &fakeTriggerRestarter{}
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetTriggerRestarter(restarter)

		body := `{"scope":{"channels":["#a"]},"expected_version":0}`
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
		req = withChiParams(req, map[string]string{"id": "plugin-2", "iid": "inst-2"})
		rec := httptest.NewRecorder()
		h.PutSubscriptionScope(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
		if len(restarter.restarts()) != 0 {
			t.Error("restarter must not be called when manifest has no subscription_schema")
		}
	})

	t.Run("422 schema validation failure", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-3",
			Name:             "trigger-plugin",
			ManifestSnapshot: triggerManifestWithScope,
			Version:          0,
		})
		q.seed(db.PluginInstance{
			ID:          "inst-3",
			PluginID:    "plugin-3",
			HealthState: "healthy",
			Version:     0,
		})

		restarter := &fakeTriggerRestarter{}
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetTriggerRestarter(restarter)

		// "channels" is required but absent; additionalProperties:false means extra keys are rejected.
		body := `{"scope":{"bad_field":"x"},"expected_version":0}`
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
		req = withChiParams(req, map[string]string{"id": "plugin-3", "iid": "inst-3"})
		rec := httptest.NewRecorder()
		h.PutSubscriptionScope(rec, req)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, want 422", rec.Code)
		}
		if len(restarter.restarts()) != 0 {
			t.Error("restarter must not be called when validation fails")
		}
	})

	t.Run("409 CAS conflict (rows == 0)", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-4",
			Name:             "trigger-plugin",
			ManifestSnapshot: triggerManifestWithScope,
			Version:          0,
		})
		q.seed(db.PluginInstance{
			ID:          "inst-4",
			PluginID:    "plugin-4",
			HealthState: "healthy",
			Version:     5, // real version
		})
		q.scopeCASFailOnID = "inst-4"

		restarter := &fakeTriggerRestarter{}
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetTriggerRestarter(restarter)

		// Send expected_version=5 but scopeCASFailOnID will force 0 rows.
		body := `{"scope":{"channels":["#x"]},"expected_version":5}`
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
		req = withChiParams(req, map[string]string{"id": "plugin-4", "iid": "inst-4"})
		rec := httptest.NewRecorder()
		h.PutSubscriptionScope(rec, req)

		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409", rec.Code)
		}
		if len(restarter.restarts()) != 0 {
			t.Error("restarter must not be called on CAS conflict")
		}
	})

	t.Run("404 instance does not belong to plugin", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seed(db.PluginInstance{
			ID:          "inst-5",
			PluginID:    "plugin-other",
			HealthState: "healthy",
			Version:     0,
		})

		h := NewPluginHandler(q, nil, fixedClock)

		body := `{"scope":{},"expected_version":0}`
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
		req = withChiParams(req, map[string]string{"id": "plugin-5", "iid": "inst-5"})
		rec := httptest.NewRecorder()
		h.PutSubscriptionScope(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 on mismatched plugin", rec.Code)
		}
	})
}

// ── PutInstanceConfig tests ───────────────────────────────────────────────────

// Manifest with a config_schema requiring app_level_token: string.
const instanceConfigManifestYAML = "schema_version: v1\nname: test-plugin\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\nconfig_schema:\n  type: object\n  properties:\n    app_level_token:\n      type: string\n  required:\n    - app_level_token\n"

// Manifest with no config_schema (accepts any object).
const instanceConfigManifestNoSchema = "schema_version: v1\nname: test-plugin\nversion: 1.0.0\nservices:\n  tool: v1\nauth:\n  mode: instance_credentials\n  strategy: none\n"

func TestPluginHandler_PutInstanceConfig_HappyPath(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestYAML,
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		InstanceName: "prod",
		ConfigJson:  "{}",
		HealthState: "healthy",
		Version:     0,
		UpdatedAt:   "2026-01-01T00:00:00Z",
	})

	h := NewPluginHandler(q, nil, fixedClock)
	body := `{"config":{"app_level_token":"xapp-1-test"},"expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var resp instanceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.ConfigJson == "" {
		t.Error("expected config_json to be set in response")
	}

	// Verify the stored config.
	inst := q.instances["inst-1"]
	if inst.ConfigJson != `{"app_level_token":"xapp-1-test"}` {
		t.Errorf("stored config_json = %q, want {\"app_level_token\":\"xapp-1-test\"}", inst.ConfigJson)
	}
}

func TestPluginHandler_PutInstanceConfig_ValidationFailure_422(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestYAML,
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  "{}",
		HealthState: "healthy",
		Version:     0,
	})

	h := NewPluginHandler(q, nil, fixedClock)
	// Missing required app_level_token.
	body := `{"config":{},"expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestPluginHandler_PutInstanceConfig_NoSchema_AcceptsAnyObject(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestNoSchema,
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  "{}",
		HealthState: "healthy",
		Version:     0,
	})

	h := NewPluginHandler(q, nil, fixedClock)
	body := `{"config":{"any":"value","nested":{"x":1}},"expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for no-schema manifest; body: %s", rec.Code, rec.Body.String())
	}
}

func TestPluginHandler_PutInstanceConfig_MissingExpectedVersion_400(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	h := NewPluginHandler(q, nil, fixedClock)

	body := `{"config":{"app_level_token":"tok"}}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing expected_version", rec.Code)
	}
}

func TestPluginHandler_PutInstanceConfig_CASConflict_409(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: instanceConfigManifestNoSchema,
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  "{}",
		HealthState: "healthy",
		Version:     5,
	})
	q.configCASFailOnID = "inst-1"

	h := NewPluginHandler(q, nil, fixedClock)
	body := `{"config":{},"expected_version":5}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for CAS conflict", rec.Code)
	}
}

func TestPluginHandler_PutInstanceConfig_InstanceNotFound_404(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()

	h := NewPluginHandler(q, nil, fixedClock)
	body := `{"config":{},"expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-missing"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for missing instance", rec.Code)
	}
}

func TestPluginHandler_PutInstanceConfig_PluginIDMismatch_404(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-other",
		HealthState: "healthy",
		Version:     0,
	})

	h := NewPluginHandler(q, nil, fixedClock)
	body := `{"config":{},"expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 on plugin ID mismatch", rec.Code)
	}
}

func TestPluginHandler_PutInstanceConfig_MalformedManifest_500(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		ManifestSnapshot: "not yaml: ::::", // deliberately malformed
		Version:          0,
	})
	q.seed(db.PluginInstance{
		ID:          "inst-1",
		PluginID:    "plugin-1",
		ConfigJson:  "{}",
		HealthState: "healthy",
		Version:     0,
	})

	h := NewPluginHandler(q, nil, fixedClock)
	body := `{"config":{},"expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for malformed manifest", rec.Code)
	}
}

// ── DeleteInstance tests ──────────────────────────────────────────────────────

// fakeProcessManager is a PluginProcessManager stub that records Stop calls.
type fakeProcessManager struct {
	mu            sync.Mutex
	stoppedIDs    []string
	stopErr       error
	stoppedByPlugin []string
}

func (f *fakeProcessManager) Stop(_ context.Context, instanceID string) error {
	f.mu.Lock()
	f.stoppedIDs = append(f.stoppedIDs, instanceID)
	f.mu.Unlock()
	return f.stopErr
}

func (f *fakeProcessManager) StopByPluginID(_ context.Context, pluginID string) error {
	f.mu.Lock()
	f.stoppedByPlugin = append(f.stoppedByPlugin, pluginID)
	f.mu.Unlock()
	return f.stopErr
}

// serveDeleteInstance wires the DeleteInstance handler into a chi router.
func serveDeleteInstance(h *PluginHandler, pluginID, instanceID string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Delete("/admin/plugins/{id}/instances/{iid}", h.DeleteInstance)
	req := httptest.NewRequest(http.MethodDelete,
		"/admin/plugins/"+pluginID+"/instances/"+instanceID, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// serveUninstall wires the Uninstall handler into a chi router.
func serveUninstall(h *PluginHandler, pluginID string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Delete("/admin/plugins/{id}", h.Uninstall)
	req := httptest.NewRequest(http.MethodDelete, "/admin/plugins/"+pluginID, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestPluginHandler_DeleteInstance(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }

	t.Run("503 when store is nil", func(t *testing.T) {
		q := newFakePluginQuerier()
		h := NewPluginHandler(q, nil, fixedClock)
		// store not wired: DeleteInstance must return 503.
		rec := serveDeleteInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503 when store is nil", rec.Code)
		}
	})

	t.Run("404 unknown plugin", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)
		rec := serveDeleteInstance(h, "nonexistent-plugin", "inst-1")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for unknown plugin", rec.Code)
		}
	})

	t.Run("404 unknown instance", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)
		rec := serveDeleteInstance(h, "plugin-1", "nonexistent-inst")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for unknown instance", rec.Code)
		}
	})

	t.Run("404 instance belongs to different plugin", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-other", InstanceName: "prod", HealthState: "healthy"})
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)
		rec := serveDeleteInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 on plugin/instance mismatch", rec.Code)
		}
	})

	t.Run("409 audience reference", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		q.seedAudienceEntries("inst-1", []db.ListAudienceEntriesByInstanceRow{
			{ID: "ae-1", AudienceID: "aud-1", PluginInstanceID: "inst-1", AudienceName: "ops-audience"},
			{ID: "ae-2", AudienceID: "aud-1", PluginInstanceID: "inst-1", AudienceName: "ops-audience"},
		})
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)
		rec := serveDeleteInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 for audience reference", rec.Code)
		}
		// Detail must mention the audience name (deduped to one occurrence).
		body := rec.Body.String()
		if !strings.Contains(body, "ops-audience") {
			t.Errorf("detail must mention audience name, got: %s", body)
		}
	})

	t.Run("409 policy reference", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "slack-prod", HealthState: "healthy"})
		q.seedPolicy(db.Policy{
			ID:   "pol-1",
			Name: "Slack Policy",
			Yaml: "trigger:\n  type: webhook\ncapabilities:\n  tools:\n    - tool: slack-prod.post_message\n",
		})
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)
		rec := serveDeleteInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 for policy reference", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Slack Policy") {
			t.Errorf("detail must mention policy name, got: %s", rec.Body.String())
		}
	})

	t.Run("204 happy path: subprocess stopped, audit event emitted", func(t *testing.T) {
		store := newPluginTestStore(t)
		// Seed fake querier for pre-delete reads (GetPlugin / GetPluginInstance).
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		// Seed real store so the transactional DELETE finds actual rows.
		seedStorePlugin(t, store, "plugin-1", "p", nil)
		seedStoreInstance(t, store, "inst-1", "plugin-1", "prod")

		pm := &fakeProcessManager{}
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)
		h.SetProcessManager(pm)

		rec := serveDeleteInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
		}

		// Subprocess must have been stopped.
		pm.mu.Lock()
		stoppedIDs := pm.stoppedIDs
		pm.mu.Unlock()
		if len(stoppedIDs) != 1 || stoppedIDs[0] != "inst-1" {
			t.Errorf("expected Stop(inst-1), got %v", stoppedIDs)
		}

		// Audit event must be emitted.
		var found bool
		for _, ev := range q.auditEvents {
			if ev.EventType == auditInstanceDeleted {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected plugin_instance_deleted audit event, got none")
		}

		// Instance must be gone from the real store (the transactional path).
		if storeHasInstance(t, store, "inst-1") {
			t.Error("instance should be deleted from real store after DeleteInstance")
		}
	})

	t.Run("204 succeeds even when subprocess Stop returns error", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		seedStorePlugin(t, store, "plugin-1", "p", nil)
		seedStoreInstance(t, store, "inst-1", "plugin-1", "prod")

		pm := &fakeProcessManager{stopErr: errors.New("subprocess wedged")}
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)
		h.SetProcessManager(pm)

		rec := serveDeleteInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204 even when Stop fails", rec.Code)
		}
		// DB row must still be deleted even when Stop fails.
		if storeHasInstance(t, store, "inst-1") {
			t.Error("instance should be deleted from store despite Stop failure")
		}
	})

	t.Run("204 succeeds with nil processManager", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		seedStorePlugin(t, store, "plugin-1", "p", nil)
		seedStoreInstance(t, store, "inst-1", "plugin-1", "prod")

		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)
		// No processManager set — must not panic.

		rec := serveDeleteInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204 with nil processManager", rec.Code)
		}
		if storeHasInstance(t, store, "inst-1") {
			t.Error("instance should be deleted from store when processManager is nil")
		}
	})
}

func TestPluginHandler_Uninstall(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }

	t.Run("503 when store is nil", func(t *testing.T) {
		q := newFakePluginQuerier()
		h := NewPluginHandler(q, nil, fixedClock)
		rec := serveUninstall(h, "plugin-1")
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503 when store is nil", rec.Code)
		}
	})

	t.Run("404 unknown plugin", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)
		rec := serveUninstall(h, "nonexistent")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for unknown plugin", rec.Code)
		}
	})

	t.Run("409 policy refs aggregated across instances", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "my-plugin", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-a", PluginID: "plugin-1", InstanceName: "slack-prod", HealthState: "healthy"})
		q.seed(db.PluginInstance{ID: "inst-b", PluginID: "plugin-1", InstanceName: "slack-staging", HealthState: "healthy"})
		q.seedPolicy(db.Policy{
			ID:   "pol-1",
			Name: "Prod Policy",
			Yaml: "trigger:\n  type: webhook\ncapabilities:\n  tools:\n    - tool: slack-prod.send\n",
		})
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)

		rec := serveUninstall(h, "plugin-1")
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 for policy refs", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Prod Policy") {
			t.Errorf("detail must mention policy name, got: %s", rec.Body.String())
		}
	})

	t.Run("409 audience refs aggregated across instances", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "my-plugin", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-a", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		q.seedAudienceEntries("inst-a", []db.ListAudienceEntriesByInstanceRow{
			{AudienceName: "ops-audience"},
		})
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)

		rec := serveUninstall(h, "plugin-1")
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 for audience refs", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "ops-audience") {
			t.Errorf("detail must mention audience name, got: %s", rec.Body.String())
		}
	})

	t.Run("204 happy path: instances and plugin removed, binary dir removed", func(t *testing.T) {
		store := newPluginTestStore(t)
		pluginsDir := t.TempDir()
		// Create a fake binary directory to verify FS cleanup.
		binaryDir := filepath.Join(pluginsDir, "installed", "my-plugin")
		if err := os.MkdirAll(binaryDir, 0o755); err != nil {
			t.Fatalf("create binary dir: %v", err)
		}
		binaryPath := filepath.Join(binaryDir, "my-plugin")
		if err := os.WriteFile(binaryPath, []byte("fake binary"), 0o755); err != nil {
			t.Fatalf("write binary: %v", err)
		}

		bp := binaryPath
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-1",
			Name:             "my-plugin",
			ManifestSnapshot: instanceConfigManifestNoSchema,
			BinaryPath:       &bp,
		})
		q.seed(db.PluginInstance{ID: "inst-a", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		q.seed(db.PluginInstance{ID: "inst-b", PluginID: "plugin-1", InstanceName: "staging", HealthState: "healthy"})
		// Seed real store so DELETE finds actual rows (CASCADE removes instances).
		seedStorePlugin(t, store, "plugin-1", "my-plugin", &bp)
		seedStoreInstance(t, store, "inst-a", "plugin-1", "prod")
		seedStoreInstance(t, store, "inst-b", "plugin-1", "staging")

		pm := &fakeProcessManager{}
		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)
		h.SetProcessManager(pm)
		h.SetPluginsDir(pluginsDir)

		rec := serveUninstall(h, "plugin-1")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
		}

		// Plugin row and both instances must be gone from the real store.
		if storeHasPlugin(t, store, "plugin-1") {
			t.Error("plugin should be deleted from real store")
		}
		if storeHasInstance(t, store, "inst-a") {
			t.Error("instance inst-a should be deleted (CASCADE)")
		}
		if storeHasInstance(t, store, "inst-b") {
			t.Error("instance inst-b should be deleted (CASCADE)")
		}

		// StopByPluginID must have been called.
		pm.mu.Lock()
		stoppedByPlugin := pm.stoppedByPlugin
		pm.mu.Unlock()
		if len(stoppedByPlugin) == 0 {
			t.Error("expected StopByPluginID to be called")
		}

		// Audit event must be emitted.
		var found bool
		for _, ev := range q.auditEvents {
			if ev.EventType == auditPluginUninstalled {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected plugin_uninstalled audit event, got none")
		}

		// Binary directory must be removed.
		if _, err := os.Stat(binaryDir); !os.IsNotExist(err) {
			t.Errorf("expected binary dir to be removed, stat err: %v", err)
		}
	})

	t.Run("204 binary_path nil: no FS op, plugin still removed", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-2",
			Name:             "no-binary",
			ManifestSnapshot: instanceConfigManifestNoSchema,
			BinaryPath:       nil, // no binary path
		})
		// Seed real store so DELETE finds the row.
		seedStorePlugin(t, store, "plugin-2", "no-binary", nil)

		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)
		h.SetPluginsDir(t.TempDir())

		rec := serveUninstall(h, "plugin-2")
		if rec.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204 when binary_path is nil", rec.Code)
		}
		if storeHasPlugin(t, store, "plugin-2") {
			t.Error("plugin should be deleted from store even when binary_path is nil")
		}
	})

	t.Run("binary path outside pluginsDir: FS skipped, plugin still removed", func(t *testing.T) {
		store := newPluginTestStore(t)
		pluginsDir := t.TempDir()
		// A path that resolves to outside pluginsDir (traversal attempt).
		outsidePath := filepath.Join("/tmp", "evil", "binary")
		bp := outsidePath

		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-evil",
			Name:             "evil",
			ManifestSnapshot: instanceConfigManifestNoSchema,
			BinaryPath:       &bp,
		})
		seedStorePlugin(t, store, "plugin-evil", "evil", &bp)

		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)
		h.SetPluginsDir(pluginsDir)

		rec := serveUninstall(h, "plugin-evil")
		if rec.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204 (containment skips FS, plugin removed)", rec.Code)
		}
		// Plugin row must still be gone even when containment check skips FS.
		if storeHasPlugin(t, store, "plugin-evil") {
			t.Error("plugin should be deleted from store even when containment check fails")
		}
	})

	t.Run("nil processManager: succeeds", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-3", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		seedStorePlugin(t, store, "plugin-3", "p", nil)

		h := NewPluginHandler(q, nil, fixedClock)
		h.SetStore(store)

		rec := serveUninstall(h, "plugin-3")
		if rec.Code != http.StatusNoContent {
			t.Errorf("status = %d, want 204 with nil processManager", rec.Code)
		}
	})
}

// Note: mid-transaction rollback for DeleteInstance and Uninstall is not
// exercised through the split-querier fake because the handler constructs a
// fresh db.New(tx) inside the transaction — the fake querier is bypassed for
// writes. Rollback correctness is governed by SQLite + database/sql semantics
// (automatic on error or Rollback call) and is not meaningfully testable at
// this layer without replacing the entire *sql.DB with a proxy.

// newPluginTestStore opens a temp-dir SQLite store for plugin handler tests
// that require transactional deletes via h.store.DB().BeginTx.
func newPluginTestStore(tb testing.TB) *db.Store {
	tb.Helper()
	dir := tb.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := db.Open(dbPath)
	if err != nil {
		tb.Fatalf("open test store: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		tb.Fatalf("migrate test store: %v", err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	return store
}

// seedStorePlugin inserts a plugin row into the real store so transactional
// DELETEs in the handler actually find rows to remove.
func seedStorePlugin(tb testing.TB, store *db.Store, pluginID, name string, binaryPath *string) {
	tb.Helper()
	q := db.New(store.DB())
	now := "2026-01-01T00:00:00Z"
	_, err := q.CreatePlugin(context.Background(), db.CreatePluginParams{
		ID:               pluginID,
		Name:             name,
		PluginVersion:    "1.0.0",
		ManifestSnapshot: instanceConfigManifestNoSchema,
		TrustedPubkey:    "",
		Status:           "active",
		BinaryPath:       binaryPath,
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		tb.Fatalf("seedStorePlugin(%s): %v", pluginID, err)
	}
}

// seedStoreInstance inserts a plugin instance row into the real store.
// pluginID must already exist in the store (use seedStorePlugin first).
func seedStoreInstance(tb testing.TB, store *db.Store, instanceID, pluginID, instanceName string) {
	tb.Helper()
	q := db.New(store.DB())
	now := "2026-01-01T00:00:00Z"
	_, err := q.CreatePluginInstance(context.Background(), db.CreatePluginInstanceParams{
		ID:                    instanceID,
		PluginID:              pluginID,
		InstanceName:          instanceName,
		ConfigJson:            "{}",
		SubscriptionScopeJson: "{}",
		HandshakeVersions:     "{}",
		HealthState:           "healthy",
		CreatedAt:             now,
		UpdatedAt:             now,
	})
	if err != nil {
		tb.Fatalf("seedStoreInstance(%s): %v", instanceID, err)
	}
}

// storeHasPlugin returns true when the plugin row still exists in the real store.
func storeHasPlugin(tb testing.TB, store *db.Store, pluginID string) bool {
	tb.Helper()
	q := db.New(store.DB())
	_, err := q.GetPluginByID(context.Background(), pluginID)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		tb.Fatalf("storeHasPlugin: %v", err)
	}
	return true
}

// storeHasInstance returns true when the plugin instance row still exists.
func storeHasInstance(tb testing.TB, store *db.Store, instanceID string) bool {
	tb.Helper()
	q := db.New(store.DB())
	_, err := q.GetPluginInstanceByID(context.Background(), instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		tb.Fatalf("storeHasInstance: %v", err)
	}
	return true
}
