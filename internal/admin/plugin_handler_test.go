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
	"sort"
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
	instances        map[string]db.PluginInstance
	plugins          map[string]db.Plugin
	auditEvents      []db.PluginAuditEvent
	pendingManifests map[string]db.PluginPendingManifest
	policies         []db.Policy
	// audienceEntries maps instance_id → list of audience entries for that instance.
	audienceEntries map[string][]db.ListAudienceEntriesByInstanceRow
	// casFailOn is the plugin ID that should return 0 rows for UpdatePluginTrustedPubkey
	// or UpdatePluginManifest to simulate a CAS conflict.
	casFailOn         string
	updatePubkey      string // last value written by UpdatePluginTrustedPubkey
	scopeCASFailOnID  string // instance ID that should return 0 rows for UpdatePluginInstanceSubscriptionScope
	configCASFailOnID string // instance ID that should return 0 rows for UpdatePluginInstanceConfig
	createInstanceErr error  // if non-nil, CreatePluginInstance returns this error
	// deletedInstanceIDs tracks which instance IDs were deleted.
	deletedInstanceIDs []string
	// deletedPluginIDs tracks which plugin IDs were deleted.
	deletedPluginIDs []string
	// getInstanceCallCounts tracks how many times GetPluginInstanceByID has been
	// called per instance ID, used to inject errors on the Nth call.
	getInstanceCallCounts map[string]int
	// getInstanceErrAfterN maps instance ID → call count threshold; calls
	// strictly after that count return errFakeGetInstance instead of the row.
	getInstanceErrAfterN map[string]int
}

// errFakeGetInstance is returned by GetPluginInstanceByID when the call
// threshold set via getInstanceErrAfterN is exceeded.
var errFakeGetInstance = errors.New("fake: GetPluginInstanceByID injected error")

func newFakePluginQuerier() *fakePluginQuerier {
	return &fakePluginQuerier{
		instances:             make(map[string]db.PluginInstance),
		plugins:               make(map[string]db.Plugin),
		pendingManifests:      make(map[string]db.PluginPendingManifest),
		audienceEntries:       make(map[string][]db.ListAudienceEntriesByInstanceRow),
		getInstanceCallCounts: make(map[string]int),
		getInstanceErrAfterN:  make(map[string]int),
	}
}

// seedPendingManifest registers a pending manifest row for a plugin.
func (f *fakePluginQuerier) seedPendingManifest(pluginID string, candidateYAML []byte, oldVersion, newVersion string) {
	f.pendingManifests[pluginID] = db.PluginPendingManifest{
		PluginID:          pluginID,
		CandidateManifest: string(candidateYAML),
		OldVersion:        oldVersion,
		NewVersion:        newVersion,
		CreatedAt:         "2026-05-05T00:00:00Z",
		UpdatedAt:         "2026-05-05T00:00:00Z",
	}
}

func (f *fakePluginQuerier) seed(inst db.PluginInstance) {
	f.instances[inst.ID] = inst
}

func (f *fakePluginQuerier) seedPlugin(p db.Plugin) {
	f.plugins[p.ID] = p
}

func (f *fakePluginQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	f.getInstanceCallCounts[id]++
	if threshold, ok := f.getInstanceErrAfterN[id]; ok && f.getInstanceCallCounts[id] > threshold {
		return db.PluginInstance{}, errFakeGetInstance
	}
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

func (f *fakePluginQuerier) GetPluginPendingManifest(_ context.Context, pluginID string) (db.PluginPendingManifest, error) {
	row, ok := f.pendingManifests[pluginID]
	if !ok {
		return db.PluginPendingManifest{}, sql.ErrNoRows
	}
	return row, nil
}

func (f *fakePluginQuerier) DeletePluginPendingManifest(_ context.Context, pluginID string) error {
	delete(f.pendingManifests, pluginID)
	return nil
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

func (f *fakePluginQuerier) ListPlugins(_ context.Context) ([]db.Plugin, error) {
	result := make([]db.Plugin, 0, len(f.plugins))
	for _, p := range f.plugins {
		result = append(result, p)
	}
	// Sort by name for deterministic ordering (mirrors the SQL ORDER BY name).
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (f *fakePluginQuerier) UpdatePluginStatus(_ context.Context, arg db.UpdatePluginStatusParams) (int64, error) {
	p, ok := f.plugins[arg.ID]
	if !ok || p.Version != arg.ExpectedVersion {
		return 0, nil // CAS miss or not found
	}
	p.Status = arg.Status
	p.UpdatedAt = arg.UpdatedAt
	p.Version++
	f.plugins[arg.ID] = p
	return 1, nil
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

// testPluginHandlerConfig holds optional dependencies for newTestPluginHandler.
// Zero-value is safe: all fields are nil-able and treated as no-op.
type testPluginHandlerConfig struct {
	store      *db.Store
	procMgr    PluginProcessManager
	trigger    TriggerRestarter
	inflight   InflightCounter
	unreg      ToolUnregistrar
	pluginsDir string
	installer  PluginInstaller
	rssAgg     RSSAggregator
	credSeeder CredentialSeeder
}

// newTestPluginHandler builds InstanceLifecycle + InstanceConfig + PluginHandler
// from a single querier and clock, wiring all optional deps from cfg. It is the
// shared constructor for all ~97 test sites so a future API change touches one
// place, not 97.
func newTestPluginHandler(q PluginQuerier, clock func() time.Time, cfg testPluginHandlerConfig) *PluginHandler {
	lifecycle := NewInstanceLifecycle(InstanceLifecycleDeps{
		Q:          q,
		Store:      cfg.store,
		ProcMgr:    cfg.procMgr,
		Trigger:    cfg.trigger,
		Inflight:   cfg.inflight,
		Unreg:      cfg.unreg,
		PluginsDir: cfg.pluginsDir,
	})
	instanceConfig := NewInstanceConfig(InstanceConfigDeps{
		Q:       q,
		Trigger: cfg.trigger,
		Clock:   clock,
	})
	return NewPluginHandler(PluginHandlerDeps{
		Q:                q,
		Clock:            clock,
		Installer:        cfg.installer,
		RSSAggregator:    cfg.rssAgg,
		ProcessManager:   cfg.procMgr,
		PluginsDir:       cfg.pluginsDir,
		Lifecycle:        lifecycle,
		Config:           instanceConfig,
		CredentialSeeder: cfg.credSeeder,
	})
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
			h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

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

	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})
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

func TestGetInstance_ReturnsInstanceConfigSchema(t *testing.T) {
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
		ConfigJson:   `{"app_level_token":"xapp-real-token"}`,
		HealthState:  "healthy",
		Version:      1,
		UpdatedAt:    "2026-01-01T00:00:00Z",
	})

	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})
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

	// (a) config_schema must be present and expose the instance-level field.
	if resp.ConfigSchema == nil {
		t.Fatal("config_schema is nil, want non-nil instance-level schema")
	}
	// The schema is decoded as map[string]interface{} by yaml → json → Go.
	schemaMap, ok := resp.ConfigSchema.(map[string]interface{})
	if !ok {
		t.Fatalf("config_schema type = %T, want map[string]interface{}", resp.ConfigSchema)
	}
	propsRaw, ok := schemaMap["properties"]
	if !ok {
		t.Fatal("config_schema.properties missing")
	}
	props, ok := propsRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("config_schema.properties type = %T, want map[string]interface{}", propsRaw)
	}
	tokenPropRaw, ok := props["app_level_token"]
	if !ok {
		t.Fatal("config_schema.properties.app_level_token missing")
	}
	tokenProp, ok := tokenPropRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("config_schema.properties.app_level_token type = %T, want map[string]interface{}", tokenPropRaw)
	}
	if tokenProp["x-gleipnir-secret"] != true {
		t.Errorf("x-gleipnir-secret = %v, want true", tokenProp["x-gleipnir-secret"])
	}

	// (b) The VALUE in config_json must still be redacted (ADR-049).
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(resp.ConfigJson), &cfg); err != nil {
		t.Fatalf("unmarshal config_json: %v", err)
	}
	if cfg["app_level_token"] != "***" {
		t.Errorf("config_json app_level_token = %v, want %q (redacted)", cfg["app_level_token"], "***")
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

	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})
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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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
	targetID  string
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
	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})

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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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
	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})

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

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})

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

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

	// triggerManifestWithScopeAndSecret is a TriggerService manifest that
	// has both a subscription_schema and a config_schema with a secret field
	// (app_level_token). Used to verify ADR-049 redaction in PutSubscriptionScope.
	triggerManifestWithScopeAndSecret = "schema_version: v1\nname: trigger-plugin\nversion: 1.0.0\nservices:\n  trigger: v1\nauth:\n  mode: instance_credentials\n  strategy: none\nsubscription_schema:\n  type: object\n  additionalProperties: false\n  required:\n    - channels\n  properties:\n    channels:\n      type: array\n      items:\n        type: string\nconfig_schema:\n  type: object\n  properties:\n    app_level_token:\n      type: string\n      x-gleipnir-secret: true\n  required:\n    - app_level_token\n"
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
		// Seed the pending manifest row as if a material change was previously detected.
		q.seedPendingManifest("plugin-1", []byte(v2ManifestYAML), "1.0.0", "2.0.0")

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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
		q.seedPendingManifest("plugin-2", []byte(v2ManifestWithRequiredField), "1.0.0", "2.0.0")

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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
		q.seedPendingManifest("plugin-4", []byte(v2ManifestYAML), "1.0.0", "2.0.0")
		q.casFailOn = "plugin-4" // trigger CAS miss on UpdatePluginManifest

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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
		q.seedPendingManifest("plugin-5", []byte(v2ManifestYAML), "1.0.0", "2.0.0")

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

	// Verifies that a high volume of unrelated audit events for other plugins
	// does not affect AcceptManifest — it now reads from plugin_pending_manifests
	// (point-read by plugin_id) rather than scanning audit events.
	t.Run("high audit volume for other plugins does not affect result", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-target",
			Name:             "target-plugin",
			ManifestSnapshot: v1ManifestYAML,
			PluginVersion:    "1.0.0",
			Status:           "pending_review",
			Version:          1,
		})
		// Seed many audit events for OTHER plugins (would have buried the target under the old scan).
		for i := range 300 {
			ev := db.PluginAuditEvent{
				EventType:   "plugin_manifest_material_change",
				Severity:    "high",
				PayloadJson: fmt.Sprintf(`{"plugin_id":"other-plugin-%d","candidate_manifest_b64":"..."}`, i),
				CreatedAt:   "2026-05-01T00:00:00Z",
			}
			q.auditEvents = append(q.auditEvents, ev)
		}
		// Only the target plugin has a pending manifest row.
		q.seedPendingManifest("plugin-target", []byte(v2ManifestYAML), "1.0.0", "2.0.0")

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{}`))
		req = withChiParams(req, map[string]string{"id": "plugin-target"})
		rec := httptest.NewRecorder()
		h.AcceptManifest(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}
		data := parseDataResponse(t, rec)
		var resp acceptManifestResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.AcceptedManifestVersion != "2.0.0" {
			t.Errorf("accepted_manifest_version = %q, want 2.0.0", resp.AcceptedManifestVersion)
		}
	})

	t.Run("no pending row returns 409", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-nopending",
			Name:             "no-pending-plugin",
			ManifestSnapshot: v1ManifestYAML,
			Version:          0,
		})
		// No pending manifest row seeded.

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{}`))
		req = withChiParams(req, map[string]string{"id": "plugin-nopending"})
		rec := httptest.NewRecorder()
		h.AcceptManifest(rec, req)

		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 when no pending row", rec.Code)
		}
	})

	t.Run("accept then second accept returns 409", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-twiceaccept",
			Name:             "twice-accept-plugin",
			ManifestSnapshot: v1ManifestYAML,
			PluginVersion:    "1.0.0",
			Status:           "pending_review",
			Version:          1,
		})
		q.seedPendingManifest("plugin-twiceaccept", []byte(v2ManifestYAML), "1.0.0", "2.0.0")

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		// First accept succeeds and deletes the pending row.
		req1 := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{}`))
		req1 = withChiParams(req1, map[string]string{"id": "plugin-twiceaccept"})
		rec1 := httptest.NewRecorder()
		h.AcceptManifest(rec1, req1)
		if rec1.Code != http.StatusOK {
			t.Fatalf("first accept: status = %d, want 200; body: %s", rec1.Code, rec1.Body.String())
		}
		// Second accept finds no pending row and must return 409.
		req2 := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{}`))
		req2 = withChiParams(req2, map[string]string{"id": "plugin-twiceaccept"})
		rec2 := httptest.NewRecorder()
		h.AcceptManifest(rec2, req2)
		if rec2.Code != http.StatusConflict {
			t.Errorf("second accept: status = %d, want 409 (row deleted after first accept)", rec2.Code)
		}
	})
}

// ── PutSubscriptionScope tests ────────────────────────────────────────────────

// fakeTriggerRestarter records Restart calls for assertion in tests.
type fakeTriggerRestarter struct {
	mu         sync.Mutex
	restartIDs []string
}

func (f *fakeTriggerRestarter) Start(_ context.Context, _ string) {}

func (f *fakeTriggerRestarter) Restart(_ context.Context, instanceID string) {
	f.mu.Lock()
	f.restartIDs = append(f.restartIDs, instanceID)
	f.mu.Unlock()
}

func (f *fakeTriggerRestarter) Stop(_ string) {}

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
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{trigger: restarter})

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
		restarter := &fakeTriggerRestarter{}
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{trigger: restarter})

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
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{trigger: restarter})

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
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{trigger: restarter})

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
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{trigger: restarter})

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

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})

		body := `{"scope":{},"expected_version":0}`
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
		req = withChiParams(req, map[string]string{"id": "plugin-5", "iid": "inst-5"})
		rec := httptest.NewRecorder()
		h.PutSubscriptionScope(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 on mismatched plugin", rec.Code)
		}
	})

	// ADR-049: secret config fields must be redacted in the success response.
	t.Run("200 secret config field is redacted in success response", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-sec",
			Name:             "trigger-plugin",
			ManifestSnapshot: triggerManifestWithScopeAndSecret,
			Version:          0,
		})
		q.seed(db.PluginInstance{
			ID:                    "inst-sec",
			PluginID:              "plugin-sec",
			InstanceName:          "prod",
			ConfigJson:            `{"app_level_token":"xoxb-secret-value"}`,
			SubscriptionScopeJson: "{}",
			HealthState:           "healthy",
			Version:               1,
			UpdatedAt:             "2026-05-01T00:00:00Z",
		})

		restarter := &fakeTriggerRestarter{}
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{trigger: restarter})

		body := `{"scope":{"channels":["#alerts"]},"expected_version":1}`
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
		req = withChiParams(req, map[string]string{"id": "plugin-sec", "iid": "inst-sec"})
		rec := httptest.NewRecorder()
		h.PutSubscriptionScope(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		var resp struct {
			Data struct {
				ConfigJson string `json:"config_json"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		var cfg map[string]any
		if err := json.Unmarshal([]byte(resp.Data.ConfigJson), &cfg); err != nil {
			t.Fatalf("unmarshal config_json: %v", err)
		}
		if cfg["app_level_token"] != "***" {
			t.Errorf("app_level_token = %q, want \"***\" (must be redacted per ADR-049)", cfg["app_level_token"])
		}
	})

	// ADR-049: secret config fields must be redacted even on the fallback
	// synthesised response path (when the re-fetch after write fails).
	t.Run("200 secret config field is redacted in fallback synthesised response", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{
			ID:               "plugin-sec2",
			Name:             "trigger-plugin",
			ManifestSnapshot: triggerManifestWithScopeAndSecret,
			Version:          0,
		})
		q.seed(db.PluginInstance{
			ID:                    "inst-sec2",
			PluginID:              "plugin-sec2",
			InstanceName:          "prod",
			ConfigJson:            `{"app_level_token":"xoxb-secret-value"}`,
			SubscriptionScopeJson: "{}",
			HealthState:           "healthy",
			Version:               1,
			UpdatedAt:             "2026-05-01T00:00:00Z",
		})
		// Allow the initial GetPluginInstanceByID (call 1) to succeed, but
		// fail the re-fetch (call 2) to trigger the synthesised-response path.
		q.getInstanceErrAfterN["inst-sec2"] = 1

		restarter := &fakeTriggerRestarter{}
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{trigger: restarter})

		body := `{"scope":{"channels":["#alerts"]},"expected_version":1}`
		req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
		req = withChiParams(req, map[string]string{"id": "plugin-sec2", "iid": "inst-sec2"})
		rec := httptest.NewRecorder()
		h.PutSubscriptionScope(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		var resp struct {
			Data struct {
				ConfigJson string `json:"config_json"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		var cfg map[string]any
		if err := json.Unmarshal([]byte(resp.Data.ConfigJson), &cfg); err != nil {
			t.Fatalf("unmarshal config_json: %v", err)
		}
		if cfg["app_level_token"] != "***" {
			t.Errorf("app_level_token = %q, want \"***\" (must be redacted per ADR-049)", cfg["app_level_token"])
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
		ID:           "inst-1",
		PluginID:     "plugin-1",
		InstanceName: "prod",
		ConfigJson:   "{}",
		HealthState:  "healthy",
		Version:      0,
		UpdatedAt:    "2026-01-01T00:00:00Z",
	})

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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
	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})

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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
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

	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
	body := `{"config":{},"expected_version":0}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"})
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for malformed manifest", rec.Code)
	}
}

// ── Deactivate / Activate tests ──────────────────────────────────────────────

// fakeInflightCounter satisfies InflightCounter for testing in-flight gate logic.
type fakeInflightCounter struct {
	counts map[string]int
}

func (f *fakeInflightCounter) InflightCountByInstance(name string) int {
	if f.counts == nil {
		return 0
	}
	return f.counts[name]
}

// serveDeactivateInstance wires the DeactivateInstance handler into a chi router.
func serveDeactivateInstance(h *PluginHandler, pluginID, instanceID string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Post("/admin/plugins/{id}/instances/{iid}/deactivate", h.DeactivateInstance)
	req := httptest.NewRequest(http.MethodPost,
		"/admin/plugins/"+pluginID+"/instances/"+instanceID+"/deactivate", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// serveActivateInstance wires the ActivateInstance handler into a chi router.
func serveActivateInstance(h *PluginHandler, pluginID, instanceID string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Post("/admin/plugins/{id}/instances/{iid}/activate", h.ActivateInstance)
	req := httptest.NewRequest(http.MethodPost,
		"/admin/plugins/"+pluginID+"/instances/"+instanceID+"/activate", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestPluginHandler_DeactivateInstance(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC) }

	t.Run("404 unknown plugin", func(t *testing.T) {
		q := newFakePluginQuerier()
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		rec := serveDeactivateInstance(h, "nonexistent-plugin", "inst-1")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for unknown plugin", rec.Code)
		}
	})

	t.Run("404 unknown instance", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		rec := serveDeactivateInstance(h, "plugin-1", "nonexistent-inst")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for unknown instance", rec.Code)
		}
	})

	t.Run("404 instance belongs to different plugin", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-other", InstanceName: "prod", HealthState: "healthy"})
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		rec := serveDeactivateInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 on plugin/instance mismatch", rec.Code)
		}
	})

	t.Run("409 already inactive", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "inactive"})
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		rec := serveDeactivateInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 when already inactive", rec.Code)
		}
	})

	t.Run("409 terminal state signature_invalid", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "signature_invalid"})
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		rec := serveDeactivateInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 for terminal state", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "terminal") {
			t.Errorf("detail should mention terminal state, got: %s", rec.Body.String())
		}
	})

	t.Run("409 in-flight calls", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{inflight: &fakeInflightCounter{counts: map[string]int{"prod": 3}}})
		rec := serveDeactivateInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 for in-flight calls", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "3 in-flight") {
			t.Errorf("detail should mention in-flight count, got: %s", rec.Body.String())
		}
	})

	t.Run("200 happy path: transitions to inactive, audit event emitted", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy", Version: 0})
		// Seed real store so SetHealthState (via GetPluginInstanceByID + UpdatePluginInstanceHealth)
		// uses the fakeQuerier path — state machine operates on the fake querier.
		_ = store

		pm := &fakeProcessManager{}
		restarter := &fakeTriggerRestarter{}
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{
			procMgr:  pm,
			trigger:  restarter,
			inflight: &fakeInflightCounter{counts: map[string]int{"prod": 0}},
		})

		rec := serveDeactivateInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		// Response must reflect inactive state.
		if !strings.Contains(rec.Body.String(), `"inactive"`) {
			t.Errorf("response should contain inactive state, got: %s", rec.Body.String())
		}

		// Audit event must be emitted.
		var found bool
		for _, ev := range q.auditEvents {
			if ev.EventType == auditInstanceDeactivated {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected plugin_instance_deactivated audit event, got none")
		}

		// Subprocess must have been stopped.
		pm.mu.Lock()
		stoppedIDs := pm.stoppedIDs
		pm.mu.Unlock()
		if len(stoppedIDs) != 1 || stoppedIDs[0] != "inst-1" {
			t.Errorf("expected Stop(inst-1), got %v", stoppedIDs)
		}
	})
}

func TestPluginHandler_ActivateInstance(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC) }

	t.Run("404 unknown plugin", func(t *testing.T) {
		q := newFakePluginQuerier()
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		rec := serveActivateInstance(h, "nonexistent-plugin", "inst-1")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for unknown plugin", rec.Code)
		}
	})

	t.Run("404 unknown instance", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		rec := serveActivateInstance(h, "plugin-1", "nonexistent-inst")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for unknown instance", rec.Code)
		}
	})

	t.Run("404 instance belongs to different plugin", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-other", InstanceName: "prod", HealthState: "inactive"})
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		rec := serveActivateInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 on plugin/instance mismatch", rec.Code)
		}
	})

	t.Run("409 not inactive (healthy)", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		rec := serveActivateInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 when not inactive", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "healthy") {
			t.Errorf("detail should mention current state, got: %s", rec.Body.String())
		}
	})

	t.Run("200 happy path: transitions to unhealthy, audit event emitted", func(t *testing.T) {
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "inactive", Version: 0})

		pm := &fakeProcessManager{}
		restarter := &fakeTriggerRestarter{}
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{
			procMgr: pm,
			trigger: restarter,
		})

		rec := serveActivateInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
		}

		// Response must reflect unhealthy (subprocess not yet up).
		if !strings.Contains(rec.Body.String(), `"unhealthy"`) {
			t.Errorf("response should contain unhealthy state, got: %s", rec.Body.String())
		}

		// Audit event must be emitted.
		var found bool
		for _, ev := range q.auditEvents {
			if ev.EventType == auditInstanceActivated {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected plugin_instance_activated audit event, got none")
		}

		// StartByPluginID must have been called.
		pm.mu.Lock()
		startedByPlugin := pm.startedByPlugin
		pm.mu.Unlock()
		if len(startedByPlugin) == 0 {
			t.Error("expected StartByPluginID to be called")
		}
	})
}

// ── DeleteInstance tests ──────────────────────────────────────────────────────

// fakeProcessManager is a PluginProcessManager stub that records Start and Stop calls.
type fakeProcessManager struct {
	mu              sync.Mutex
	stoppedIDs      []string
	stopErr         error
	stoppedByPlugin []string
	startedByPlugin []string
	startErr        error
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

func (f *fakeProcessManager) StartByPluginID(_ context.Context, pluginID string) error {
	f.mu.Lock()
	f.startedByPlugin = append(f.startedByPlugin, pluginID)
	f.mu.Unlock()
	return f.startErr
}

// fakeUnreg is a ToolUnregistrar stub that records instance names passed to
// UnregisterInstance. Used by instance_lifecycle_test.go to assert the correct
// name is forwarded (the regression guard for key-mismatch bugs).
type fakeUnreg struct {
	names []string
}

func (f *fakeUnreg) UnregisterInstance(_ context.Context, instanceName string) {
	f.names = append(f.names, instanceName)
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
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		// store not wired: DeleteInstance must return 503.
		rec := serveDeleteInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503 when store is nil", rec.Code)
		}
	})

	t.Run("404 unknown plugin", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{store: store})
		rec := serveDeleteInstance(h, "nonexistent-plugin", "inst-1")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for unknown plugin", rec.Code)
		}
	})

	t.Run("404 unknown instance", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{store: store})
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
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{store: store})
		rec := serveDeleteInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 on plugin/instance mismatch", rec.Code)
		}
	})

	t.Run("409 in-flight calls", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", InstanceName: "prod", HealthState: "healthy"})
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{
			store:    store,
			inflight: &fakeInflightCounter{counts: map[string]int{"prod": 3}},
		})
		rec := serveDeleteInstance(h, "plugin-1", "inst-1")
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 for in-flight calls", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "3 in-flight") {
			t.Errorf("detail should mention in-flight count, got: %s", rec.Body.String())
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
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{store: store})
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
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{store: store})
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
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{
			store:   store,
			procMgr: pm,
		})

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
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{
			store:   store,
			procMgr: pm,
		})

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

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{store: store})
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
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})
		rec := serveUninstall(h, "plugin-1")
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503 when store is nil", rec.Code)
		}
	})

	t.Run("404 unknown plugin", func(t *testing.T) {
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{store: store})
		rec := serveUninstall(h, "nonexistent")
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 for unknown plugin", rec.Code)
		}
	})

	t.Run("409 instances still exist", func(t *testing.T) {
		// Per #243: uninstalling the plugin requires all instances to be removed
		// first. The per-instance DeleteInstance handler enforces policy/audience
		// and in-flight gates; Uninstall only checks that zero instances remain.
		store := newPluginTestStore(t)
		q := newFakePluginQuerier()
		q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "my-plugin", ManifestSnapshot: instanceConfigManifestNoSchema})
		q.seed(db.PluginInstance{ID: "inst-a", PluginID: "plugin-1", InstanceName: "slack-prod", HealthState: "healthy"})
		q.seed(db.PluginInstance{ID: "inst-b", PluginID: "plugin-1", InstanceName: "slack-staging", HealthState: "healthy"})
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{store: store})

		rec := serveUninstall(h, "plugin-1")
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 when instances still exist", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "all instances must be removed") {
			t.Errorf("error must mention 'all instances must be removed', got: %s", body)
		}
		if !strings.Contains(body, "slack-prod") {
			t.Errorf("detail must list instance names, got: %s", body)
		}
		if !strings.Contains(body, "slack-staging") {
			t.Errorf("detail must list instance names, got: %s", body)
		}
	})

	t.Run("204 happy path with zero instances: plugin removed, binary dir removed", func(t *testing.T) {
		// When zero instances remain, uninstall proceeds immediately.
		store := newPluginTestStore(t)
		pluginsDir := t.TempDir()
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
			ID:               "plugin-2",
			Name:             "my-plugin",
			ManifestSnapshot: instanceConfigManifestNoSchema,
			BinaryPath:       &bp,
		})
		// No instances seeded — zero instances triggers the happy path.
		seedStorePlugin(t, store, "plugin-2", "my-plugin", &bp)

		pm := &fakeProcessManager{}
		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{
			store:      store,
			procMgr:    pm,
			pluginsDir: pluginsDir,
		})

		rec := serveUninstall(h, "plugin-2")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
		}

		// Plugin row must be gone.
		if storeHasPlugin(t, store, "plugin-2") {
			t.Error("plugin should be deleted from real store")
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

	// NOTE: The old "204 happy path with instances" test was removed in #243.
	// Uninstall now gates on zero instances; the new "204 happy path with zero
	// instances" subtest (added above in this PR) covers the success path.

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

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{
			store:      store,
			pluginsDir: t.TempDir(),
		})

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

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{
			store:      store,
			pluginsDir: pluginsDir,
		})

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

		h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{store: store})

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

// ─── ApprovePlugin ─────────────────────────────────────────────────────────

func TestApprovePlugin_HappyPath(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		PluginVersion:    "1.0.0",
		ManifestSnapshot: instanceConfigManifestNoSchema,
		Status:           "pending_review",
		Version:          0,
	})
	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-1/approve", nil)
	req = withChiParams(req, map[string]string{"id": "plugin-1"})
	rec := httptest.NewRecorder()
	h.ApprovePlugin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var resp approvePluginResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Status != "active" {
		t.Errorf("status = %q, want %q", resp.Status, "active")
	}
	if resp.ID != "plugin-1" {
		t.Errorf("id = %q, want %q", resp.ID, "plugin-1")
	}

	// Plugin row must now be active.
	updated := q.plugins["plugin-1"]
	if updated.Status != "active" {
		t.Errorf("plugin.Status = %q, want %q", updated.Status, "active")
	}

	// Audit event must have been recorded.
	found := false
	for _, ev := range q.auditEvents {
		if ev.EventType == auditReviewApproved {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected plugin_review_approved audit event, got none")
	}
}

func TestApprovePlugin_NotFound(t *testing.T) {
	q := newFakePluginQuerier()
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = withChiParams(req, map[string]string{"id": "missing-plugin"})
	rec := httptest.NewRecorder()
	h.ApprovePlugin(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestApprovePlugin_AlreadyActive(t *testing.T) {
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		PluginVersion:    "1.0.0",
		ManifestSnapshot: instanceConfigManifestNoSchema,
		Status:           "active",
		Version:          0,
	})
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = withChiParams(req, map[string]string{"id": "plugin-1"})
	rec := httptest.NewRecorder()
	h.ApprovePlugin(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for already-active plugin", rec.Code)
	}
}

// ─── RejectPlugin ──────────────────────────────────────────────────────────

func TestRejectPlugin_HappyPath(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		PluginVersion:    "1.0.0",
		ManifestSnapshot: instanceConfigManifestNoSchema,
		Status:           "pending_review",
		Version:          0,
	})
	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = withChiParams(req, map[string]string{"id": "plugin-1"})
	rec := httptest.NewRecorder()
	h.RejectPlugin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var resp rejectPluginResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Status != "rejected" {
		t.Errorf("status = %q, want %q", resp.Status, "rejected")
	}

	// Plugin row must be gone.
	if _, ok := q.plugins["plugin-1"]; ok {
		t.Error("plugin row still exists after reject; want it deleted")
	}

	// Audit event must have been recorded.
	found := false
	for _, ev := range q.auditEvents {
		if ev.EventType == auditReviewRejected {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected plugin_review_rejected audit event, got none")
	}
}

func TestRejectPlugin_NotFound(t *testing.T) {
	q := newFakePluginQuerier()
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = withChiParams(req, map[string]string{"id": "missing-plugin"})
	rec := httptest.NewRecorder()
	h.RejectPlugin(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRejectPlugin_AlreadyActive(t *testing.T) {
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "test-plugin",
		PluginVersion:    "1.0.0",
		ManifestSnapshot: instanceConfigManifestNoSchema,
		Status:           "active",
		Version:          0,
	})
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = withChiParams(req, map[string]string{"id": "plugin-1"})
	rec := httptest.NewRecorder()
	h.RejectPlugin(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for active plugin reject", rec.Code)
	}
}

// ─── GetPluginDetail ───────────────────────────────────────────────────────

// fullDetailManifest is a manifest with all the fields surfaced by GetPluginDetail.
const fullDetailManifest = `schema_version: v1
name: full-plugin
version: 2.3.1
description: "A complete plugin"
author: "Test Author"
license: "MIT"
services:
  tool: v1
  trigger: v1
auth:
  mode: instance_credentials
  strategy: oauth2_authcode
  oauth_defaults:
    client_id: "test-client"
tier2_capabilities:
  - run_history_read
`

func TestGetPluginDetail_HappyPath(t *testing.T) {
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-1",
		Name:             "full-plugin",
		PluginVersion:    "2.3.1",
		ManifestSnapshot: fullDetailManifest,
		TrustedPubkey:    "", // unsigned for simplicity — fingerprint omitted
		Status:           "pending_review",
		Version:          0,
		CreatedAt:        "2026-01-01T00:00:00Z",
	})
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/plugin-1", nil)
	req = withChiParams(req, map[string]string{"id": "plugin-1"})
	rec := httptest.NewRecorder()
	h.GetPluginDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var resp pluginDetailResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Name != "full-plugin" {
		t.Errorf("name = %q, want %q", resp.Name, "full-plugin")
	}
	if resp.Version != "2.3.1" {
		t.Errorf("version = %q, want %q", resp.Version, "2.3.1")
	}
	if resp.Description != "A complete plugin" {
		t.Errorf("description = %q, want %q", resp.Description, "A complete plugin")
	}
	if resp.Author != "Test Author" {
		t.Errorf("author = %q, want %q", resp.Author, "Test Author")
	}
	if resp.License != "MIT" {
		t.Errorf("license = %q, want %q", resp.License, "MIT")
	}
	if resp.AuthStrategy != "oauth2_authcode" {
		t.Errorf("auth_strategy = %q, want %q", resp.AuthStrategy, "oauth2_authcode")
	}
	if !resp.HasOAuthDefaults {
		t.Error("has_oauth_defaults: want true")
	}
	if resp.PubkeyFingerprint != "" {
		t.Errorf("pubkey_fingerprint = %q, want empty for unsigned plugin", resp.PubkeyFingerprint)
	}
	if resp.HasSBOM {
		t.Error("has_sbom: want false (no sbom field in manifest)")
	}

	wantServices := []string{"tool", "trigger"}
	if len(resp.Services) != len(wantServices) {
		t.Errorf("services = %v, want %v", resp.Services, wantServices)
	}

	if len(resp.Tier2Capabilities) != 1 || resp.Tier2Capabilities[0] != "run_history_read" {
		t.Errorf("tier2_capabilities = %v, want [run_history_read]", resp.Tier2Capabilities)
	}
}

func TestGetPluginDetail_NotFound(t *testing.T) {
	q := newFakePluginQuerier()
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withChiParams(req, map[string]string{"id": "missing"})
	rec := httptest.NewRecorder()
	h.GetPluginDetail(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// ─── ListPlugins ───────────────────────────────────────────────────────────

func TestListPlugins_Empty(t *testing.T) {
	q := newFakePluginQuerier()
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins", nil)
	rec := httptest.NewRecorder()
	h.ListPlugins(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var items []pluginListItemResponse
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("got %d items, want 0", len(items))
	}
}

func TestListPlugins_MixedStatuses(t *testing.T) {
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-pending",
		Name:             "alpha-plugin",
		PluginVersion:    "1.0.0",
		ManifestSnapshot: instanceConfigManifestNoSchema,
		Status:           "pending_review",
		Version:          0,
		CreatedAt:        "2026-01-01T00:00:00Z",
	})
	q.seedPlugin(db.Plugin{
		ID:               "plugin-active",
		Name:             "beta-plugin",
		PluginVersion:    "2.0.0",
		ManifestSnapshot: instanceConfigManifestNoSchema,
		Status:           "active",
		Version:          0,
		CreatedAt:        "2026-01-02T00:00:00Z",
	})
	// Seed an instance for the active plugin so instance_count is non-zero.
	q.seed(db.PluginInstance{
		ID:           "inst-1",
		PluginID:     "plugin-active",
		InstanceName: "prod",
		HealthState:  "healthy",
		Version:      0,
	})
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins", nil)
	rec := httptest.NewRecorder()
	h.ListPlugins(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var items []pluginListItemResponse
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	// Items are sorted by name: alpha-plugin first, beta-plugin second.
	if items[0].Name != "alpha-plugin" {
		t.Errorf("items[0].name = %q, want %q", items[0].Name, "alpha-plugin")
	}
	if items[0].Status != "pending_review" {
		t.Errorf("items[0].status = %q, want %q", items[0].Status, "pending_review")
	}
	if items[0].InstanceCount != 0 {
		t.Errorf("items[0].instance_count = %d, want 0", items[0].InstanceCount)
	}

	if items[1].Name != "beta-plugin" {
		t.Errorf("items[1].name = %q, want %q", items[1].Name, "beta-plugin")
	}
	if items[1].Status != "active" {
		t.Errorf("items[1].status = %q, want %q", items[1].Status, "active")
	}
	if items[1].InstanceCount != 1 {
		t.Errorf("items[1].instance_count = %d, want 1", items[1].InstanceCount)
	}
}

// --- GetPluginRSS tests ---

// stubRSSAggregator implements RSSAggregator for tests without importing the
// process package.
type stubRSSAggregator struct {
	total   uint64
	count   int
	samples []RSSSample
}

func (s *stubRSSAggregator) Aggregate() (uint64, int, []RSSSample) {
	return s.total, s.count, s.samples
}

func TestPluginHandler_GetPluginRSS_NilAggregator(t *testing.T) {
	h := newTestPluginHandler(newFakePluginQuerier(), nil, testPluginHandlerConfig{})
	// No aggregator wired — plugins are disabled.

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/rss", nil)
	rec := httptest.NewRecorder()
	h.GetPluginRSS(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestPluginHandler_GetPluginRSS_WithData(t *testing.T) {
	fixedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	agg := &stubRSSAggregator{
		total: 300 * 1024 * 1024,
		count: 2,
		samples: []RSSSample{
			{
				InstanceID:   "inst-aaa",
				InstanceName: "alpha",
				PluginID:     "plugin-x",
				Bytes:        200 * 1024 * 1024,
				SampledAt:    fixedAt,
			},
			{
				InstanceID:   "inst-bbb",
				InstanceName: "beta",
				PluginID:     "plugin-y",
				Bytes:        100 * 1024 * 1024,
				SampledAt:    fixedAt,
			},
		},
	}

	h := newTestPluginHandler(newFakePluginQuerier(), nil, testPluginHandlerConfig{rssAgg: agg})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/rss", nil)
	rec := httptest.NewRecorder()
	h.GetPluginRSS(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var resp pluginRSSResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.TotalBytes != 300*1024*1024 {
		t.Errorf("total_bytes = %d, want %d", resp.TotalBytes, 300*1024*1024)
	}
	if resp.InstanceCount != 2 {
		t.Errorf("instance_count = %d, want 2", resp.InstanceCount)
	}
	if len(resp.Instances) != 2 {
		t.Fatalf("instances len = %d, want 2", len(resp.Instances))
	}
	if resp.Instances[0].InstanceName != "alpha" {
		t.Errorf("instances[0].instance_name = %q, want %q", resp.Instances[0].InstanceName, "alpha")
	}
	if resp.Instances[0].RSSBytes != 200*1024*1024 {
		t.Errorf("instances[0].rss_bytes = %d, want %d", resp.Instances[0].RSSBytes, 200*1024*1024)
	}
}

func TestPluginHandler_GetPluginRSS_ZeroInstances(t *testing.T) {
	agg := &stubRSSAggregator{total: 0, count: 0, samples: nil}

	h := newTestPluginHandler(newFakePluginQuerier(), nil, testPluginHandlerConfig{rssAgg: agg})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins/rss", nil)
	rec := httptest.NewRecorder()
	h.GetPluginRSS(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	data := parseDataResponse(t, rec)
	var resp pluginRSSResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.TotalBytes != 0 {
		t.Errorf("total_bytes = %d, want 0", resp.TotalBytes)
	}
	if resp.InstanceCount != 0 {
		t.Errorf("instance_count = %d, want 0", resp.InstanceCount)
	}
	// instances must be an empty array, not null, so it marshals to [].
	if resp.Instances == nil {
		t.Error("instances is nil; want empty array []")
	}
	if len(resp.Instances) != 0 {
		t.Errorf("instances len = %d, want 0", len(resp.Instances))
	}
}

// sbomManifest is a manifest that declares an SBOM file.
const sbomManifest = `schema_version: v1
name: sbom-plugin
version: 1.0.0
services:
  tool: v1
auth:
  mode: instance_credentials
  strategy: none
sbom: sbom.cyclonedx.json
`

// sbomManifestSpdx declares an SBOM with an SPDX text extension (not JSON).
const sbomManifestSpdx = `schema_version: v1
name: sbom-plugin
version: 1.0.0
services:
  tool: v1
auth:
  mode: instance_credentials
  strategy: none
sbom: sbom.spdx.txt
`

// sbomManifestTraversal declares an SBOM path that tries to escape the bundle dir.
const sbomManifestTraversal = `schema_version: v1
name: sbom-plugin
version: 1.0.0
services:
  tool: v1
auth:
  mode: instance_credentials
  strategy: none
sbom: ../../etc/passwd
`

// noSbomManifest is a manifest with no sbom field.
const noSbomManifest = `schema_version: v1
name: sbom-plugin
version: 1.0.0
services:
  tool: v1
auth:
  mode: instance_credentials
  strategy: none
`

// serveGetSBOM sets up a chi router with GetPluginSBOM registered and issues
// a GET request for the given pluginID.
func serveGetSBOM(h *PluginHandler, pluginID string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	router.Get("/plugins/{id}/sbom", h.GetPluginSBOM)
	req := httptest.NewRequest(http.MethodGet, "/plugins/"+pluginID+"/sbom", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestGetPluginSBOM_Success(t *testing.T) {
	// Write an SBOM file in a temp bundle directory.
	bundleDir := t.TempDir()
	binaryPath := filepath.Join(bundleDir, "plugin-binary")
	sbomContent := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.4"}`)
	if err := os.WriteFile(filepath.Join(bundleDir, "sbom.cyclonedx.json"), sbomContent, 0o644); err != nil {
		t.Fatalf("write sbom: %v", err)
	}

	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-sbom",
		Name:             "sbom-plugin",
		ManifestSnapshot: sbomManifest,
		BinaryPath:       &binaryPath,
	})

	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})
	rec := serveGetSBOM(h, "plugin-sbom")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.cyclonedx+json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/vnd.cyclonedx+json")
	}
	if body := rec.Body.Bytes(); string(body) != string(sbomContent) {
		t.Errorf("body = %q, want %q", body, sbomContent)
	}
}

func TestGetPluginSBOM_NotFound_NoPlugin(t *testing.T) {
	h := newTestPluginHandler(newFakePluginQuerier(), nil, testPluginHandlerConfig{})
	rec := serveGetSBOM(h, "plugin-missing")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetPluginSBOM_NotFound_NoBinaryPath(t *testing.T) {
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-nobinary",
		Name:             "sbom-plugin",
		ManifestSnapshot: sbomManifest,
		BinaryPath:       nil, // no bundle on disk
	})

	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})
	rec := serveGetSBOM(h, "plugin-nobinary")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetPluginSBOM_NotFound_NoSBOMField(t *testing.T) {
	bundleDir := t.TempDir()
	binaryPath := filepath.Join(bundleDir, "plugin-binary")

	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-nosbom",
		Name:             "sbom-plugin",
		ManifestSnapshot: noSbomManifest,
		BinaryPath:       &binaryPath,
	})

	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})
	rec := serveGetSBOM(h, "plugin-nosbom")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetPluginSBOM_NotFound_FileMissing(t *testing.T) {
	bundleDir := t.TempDir()
	binaryPath := filepath.Join(bundleDir, "plugin-binary")
	// Manifest says sbom.cyclonedx.json but we don't write it.

	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-filemissing",
		Name:             "sbom-plugin",
		ManifestSnapshot: sbomManifest,
		BinaryPath:       &binaryPath,
	})

	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})
	rec := serveGetSBOM(h, "plugin-filemissing")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetPluginSBOM_PathTraversal(t *testing.T) {
	bundleDir := t.TempDir()
	binaryPath := filepath.Join(bundleDir, "plugin-binary")

	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-traversal",
		Name:             "sbom-plugin",
		ManifestSnapshot: sbomManifestTraversal,
		BinaryPath:       &binaryPath,
	})

	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})
	rec := serveGetSBOM(h, "plugin-traversal")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (traversal rejected)", rec.Code)
	}
}

func TestGetPluginSBOM_FallbackContentType(t *testing.T) {
	bundleDir := t.TempDir()
	binaryPath := filepath.Join(bundleDir, "plugin-binary")
	sbomContent := []byte("SPDXVersion: SPDX-2.3\n")
	if err := os.WriteFile(filepath.Join(bundleDir, "sbom.spdx.txt"), sbomContent, 0o644); err != nil {
		t.Fatalf("write sbom: %v", err)
	}

	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID:               "plugin-spdx",
		Name:             "sbom-plugin",
		ManifestSnapshot: sbomManifestSpdx,
		BinaryPath:       &binaryPath,
	})

	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})
	rec := serveGetSBOM(h, "plugin-spdx")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/plain; charset=utf-8")
	}
}
