package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/plugin/oauth"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

const attributionAdminID = "admin-user-1"

func withAdminCaller(r *http.Request) *http.Request {
	ctx := auth.WithUserContext(r.Context(), attributionAdminID, "alice", []string{"admin"})
	return r.WithContext(ctx)
}

func TestPluginCredentialsHandler_WritesAreAttributedToCaller(t *testing.T) {
	const secret = "super-secret-value-123"
	params := map[string]string{"id": "plugin-1", "iid": "inst-1"}
	headerParams := map[string]string{"id": "plugin-1", "iid": "inst-1", "name": "X-Custom"}

	tests := []struct {
		name      string
		strategy  string
		seed      *oauth.StoredCredentials
		method    string
		body      any
		params    map[string]string
		call      func(h *PluginCredentialsHandler) http.HandlerFunc
		eventType string
	}{
		{
			name: "static api key", strategy: sdkmanifest.AuthStrategyStaticAPIKey,
			method: http.MethodPut, params: params,
			body:      map[string]string{"header_name": "X-Key", "api_key": secret},
			call:      func(h *PluginCredentialsHandler) http.HandlerFunc { return h.SetStaticAPIKey },
			eventType: "plugin_credentials_set",
		},
		{
			name: "header set entry", strategy: sdkmanifest.AuthStrategyHeaderSet,
			method: http.MethodPut, params: headerParams,
			body:      map[string]string{"value": secret},
			call:      func(h *PluginCredentialsHandler) http.HandlerFunc { return h.SetHeader },
			eventType: "plugin_credentials_set",
		},
		{
			name: "delete header set entry", strategy: sdkmanifest.AuthStrategyHeaderSet,
			seed: &oauth.StoredCredentials{
				Strategy:  sdkmanifest.AuthStrategyHeaderSet,
				HeaderSet: &oauth.HeaderSetCreds{Headers: []oauth.NamedHeader{{Name: "X-Custom", Value: secret}}},
			},
			method: http.MethodDelete, params: headerParams,
			call:      func(h *PluginCredentialsHandler) http.HandlerFunc { return h.DeleteHeader },
			eventType: "plugin_credentials_deleted",
		},
		{
			name: "basic auth", strategy: sdkmanifest.AuthStrategyBasicAuth,
			method: http.MethodPut, params: params,
			body:      map[string]string{"username": "bob", "password": secret},
			call:      func(h *PluginCredentialsHandler) http.HandlerFunc { return h.SetBasicAuth },
			eventType: "plugin_credentials_set",
		},
		{
			name: "oauth client", strategy: sdkmanifest.AuthStrategyOAuth2Authcode,
			method: http.MethodPut, params: params,
			body:      map[string]string{"client_id": "cid", "client_secret": secret},
			call:      func(h *PluginCredentialsHandler) http.HandlerFunc { return h.SetOAuthClient },
			eventType: "plugin_credentials_set",
		},
		{
			name: "oauth token", strategy: sdkmanifest.AuthStrategyOAuth2Authcode,
			method: http.MethodPut, params: params,
			body:      map[string]string{"access_token": secret},
			call:      func(h *PluginCredentialsHandler) http.HandlerFunc { return h.SetOAuthToken },
			eventType: "plugin_credentials_set",
		},
		{
			name: "clear credentials", strategy: sdkmanifest.AuthStrategyStaticAPIKey,
			seed: &oauth.StoredCredentials{
				Strategy:     sdkmanifest.AuthStrategyStaticAPIKey,
				StaticAPIKey: &oauth.StaticAPIKeyCreds{HeaderName: "X-Key", APIKey: secret},
			},
			method: http.MethodDelete, params: params,
			call:      func(h *PluginCredentialsHandler) http.HandlerFunc { return h.Delete },
			eventType: "plugin_credentials_cleared",
		},
	}

	for _, tc := range tests {
		for _, authenticated := range []bool{true, false} {
			name := tc.name + "/authenticated"
			if !authenticated {
				name = tc.name + "/no caller"
			}
			t.Run(name, func(t *testing.T) {
				pq := buildQuerierWithManifest(t, tc.strategy)
				sq := &fakeCredQuerier{
					instance: db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", HealthState: "healthy"},
				}
				if tc.seed != nil {
					seedCredentials(t, sq, *tc.seed)
				}
				h := buildCredHandler(t, pq, sq)

				r := newCredRequest(tc.method, "/", tc.body, tc.params)
				if authenticated {
					r = withAdminCaller(r)
				}
				w := httptest.NewRecorder()
				tc.call(h).ServeHTTP(w, r)
				if w.Code != http.StatusNoContent {
					t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
				}

				var ev *db.InsertPluginAuditEventParams
				for i := range sq.auditEvents {
					if sq.auditEvents[i].EventType == tc.eventType {
						ev = &sq.auditEvents[i]
					}
				}
				if ev == nil {
					t.Fatalf("no %s event among %v", tc.eventType, sq.auditEvents)
				}
				switch {
				case authenticated && (ev.ActorUserID == nil || *ev.ActorUserID != attributionAdminID):
					t.Errorf("actor = %v, want %q", ev.ActorUserID, attributionAdminID)
				case !authenticated && ev.ActorUserID != nil:
					t.Errorf("actor = %q, want NULL when no caller on context", *ev.ActorUserID)
				}
				if strings.Contains(ev.PayloadJson, secret) {
					t.Errorf("payload leaks secret: %s", ev.PayloadJson)
				}
			})
		}
	}
}

func findAudit(t *testing.T, q *fakePluginQuerier, eventType string) db.PluginAuditEvent {
	t.Helper()
	for _, ev := range q.auditEvents {
		if ev.EventType == eventType {
			return ev
		}
	}
	t.Fatalf("no %s event among %v", eventType, q.auditEvents)
	return db.PluginAuditEvent{}
}

func TestPluginHandler_ConfigWritesEmitAuditEvent(t *testing.T) {
	const secret = "xapp-real-secret-value"
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }

	tests := []struct {
		name        string
		priorConfig string
		body        string
		property    string
		wantChanged []string
		wantSecrets []string
	}{
		{
			name:        "bulk put changes secret",
			priorConfig: "{}",
			body:        `{"config":{"app_level_token":"` + secret + `"},"expected_version":0}`,
			wantChanged: []string{},
			wantSecrets: []string{"app_level_token"},
		},
		{
			name:        "bulk put unchanged secret not reported",
			priorConfig: `{"app_level_token":"` + secret + `"}`,
			body:        `{"config":{"app_level_token":"` + secret + `"},"expected_version":0}`,
			wantChanged: []string{},
			wantSecrets: []string{},
		},
		{
			name:        "per-field secret put",
			priorConfig: "{}",
			body:        `{"value":"` + secret + `","expected_version":0}`,
			property:    "app_level_token",
			wantChanged: []string{},
			wantSecrets: []string{"app_level_token"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := newFakePluginQuerier()
			q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestWithSecret})
			q.seed(db.PluginInstance{
				ID: "inst-1", PluginID: "plugin-1", ConfigJson: tc.priorConfig,
				HealthState: "healthy", UpdatedAt: "2026-01-01T00:00:00Z",
			})
			h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{})

			req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(tc.body))
			params := map[string]string{"id": "plugin-1", "iid": "inst-1"}
			if tc.property != "" {
				params["property"] = tc.property
			}
			req = withAdminCaller(withChiParams(req, params))
			rec := httptest.NewRecorder()
			if tc.property != "" {
				h.PutInstanceConfigProperty(rec, req)
			} else {
				h.PutInstanceConfig(rec, req)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
			}

			ev := findAudit(t, q, "plugin_instance_config_updated")
			if ev.ActorUserID == nil || *ev.ActorUserID != attributionAdminID {
				t.Errorf("actor = %v, want %q", ev.ActorUserID, attributionAdminID)
			}
			if ev.PluginInstanceID == nil || *ev.PluginInstanceID != "inst-1" {
				t.Errorf("plugin_instance_id = %v, want inst-1", ev.PluginInstanceID)
			}
			if strings.Contains(ev.PayloadJson, secret) {
				t.Errorf("payload leaks secret: %s", ev.PayloadJson)
			}
			var payload struct {
				ChangedKeys       []string `json:"changed_keys"`
				ChangedSecretKeys []string `json:"changed_secret_keys"`
			}
			if err := json.Unmarshal([]byte(ev.PayloadJson), &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			sort.Strings(payload.ChangedKeys)
			if strings.Join(payload.ChangedKeys, ",") != strings.Join(tc.wantChanged, ",") {
				t.Errorf("changed_keys = %v, want %v", payload.ChangedKeys, tc.wantChanged)
			}
			if strings.Join(payload.ChangedSecretKeys, ",") != strings.Join(tc.wantSecrets, ",") {
				t.Errorf("changed_secret_keys = %v, want %v", payload.ChangedSecretKeys, tc.wantSecrets)
			}
		})
	}
}

func TestPluginHandler_ConfigWriteConflictEmitsNoAuditEvent(t *testing.T) {
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", ManifestSnapshot: instanceConfigManifestWithSecret})
	q.seed(db.PluginInstance{ID: "inst-1", PluginID: "plugin-1", ConfigJson: "{}", HealthState: "healthy", Version: 5})
	h := newTestPluginHandler(q, time.Now, testPluginHandlerConfig{})

	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(`{"config":{"app_level_token":"x"},"expected_version":0}`))
	req = withAdminCaller(withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"}))
	rec := httptest.NewRecorder()
	h.PutInstanceConfig(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if len(q.auditEvents) != 0 {
		t.Errorf("expected no audit events on CAS conflict, got %v", q.auditEvents)
	}
}

func TestPluginHandler_PutSubscriptionScopeEmitsAuditEvent(t *testing.T) {
	fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "t", ManifestSnapshot: triggerManifestWithScope})
	q.seed(db.PluginInstance{
		ID: "inst-1", PluginID: "plugin-1", SubscriptionScopeJson: `{"channels":["#old"]}`,
		HealthState: "healthy", Version: 2, UpdatedAt: "2026-05-01T00:00:00Z",
	})
	h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{trigger: &fakeTriggerRestarter{}})

	body := `{"scope":{"channels":["#incidents"]},"expected_version":2}`
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req = withAdminCaller(withChiParams(req, map[string]string{"id": "plugin-1", "iid": "inst-1"}))
	rec := httptest.NewRecorder()
	h.PutSubscriptionScope(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}

	ev := findAudit(t, q, "plugin_subscription_scope_updated")
	if ev.ActorUserID == nil || *ev.ActorUserID != attributionAdminID {
		t.Errorf("actor = %v, want %q", ev.ActorUserID, attributionAdminID)
	}
	var payload struct {
		OldScope map[string][]string `json:"old_scope"`
		NewScope map[string][]string `json:"new_scope"`
	}
	if err := json.Unmarshal([]byte(ev.PayloadJson), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got := payload.OldScope["channels"]; len(got) != 1 || got[0] != "#old" {
		t.Errorf("old_scope = %v", payload.OldScope)
	}
	if got := payload.NewScope["channels"]; len(got) != 1 || got[0] != "#incidents" {
		t.Errorf("new_scope = %v", payload.NewScope)
	}
}
