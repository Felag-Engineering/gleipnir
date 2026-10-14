package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

const fullV2DetailManifest = `schema_version: "2"
name: v2-plugin
version: 1.2.0
description: A containerized plugin
package:
  registry_type: oci
  identifier: ghcr.io/acme/v2-plugin@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  version: 1.2.0
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
    event_source: {}
    human_channel:
      assurance: weak
    identity_provider:
      link_methods: [inbound_code]
  egress:
    - domain: api.example.com
      reason: calls the API
  resources:
    memory_mb: 256
    cpu_millicores: 500
  auth:
    strategy: oauth2_authcode
    oauth_defaults:
      authorization_url: https://example.com/auth
      token_url: https://example.com/token
      scopes: [read, write]
  tier2_capabilities:
    - run_history_read
  tools:
    - name: delete_all
      elicitation_kind: permission
  event_kinds:
    - kind: message_posted
      description: A message was posted
  config_schema:
    type: object
    properties:
      workspace:
        type: string
`

// TestApprovePlugin_ManifestVersionGate pins the rule that a v2 manifest is
// approvable only where the v2 substrate is wired. Read does not error on a
// fully valid v2 manifest, so the guard must check the version, not just err.
func TestApprovePlugin_ManifestVersionGate(t *testing.T) {
	tests := []struct {
		name         string
		manifest     string
		status       string
		approveV2    bool
		wantCode     int
		wantStatus   string
		wantAudit    bool
		wantBodyPart string
	}{
		{name: "v1 approved without the v2 flag", manifest: instanceConfigManifestNoSchema, status: "pending_review",
			wantCode: http.StatusOK, wantStatus: "active", wantAudit: true},
		{name: "v1 approved with the v2 flag", manifest: instanceConfigManifestNoSchema, status: "pending_review", approveV2: true,
			wantCode: http.StatusOK, wantStatus: "active", wantAudit: true},
		{name: "v2 refused without the v2 flag", manifest: validSchemaVersion2ManifestYAML, status: "pending_review",
			wantCode: http.StatusConflict, wantStatus: "pending_review", wantBodyPart: "v2 substrate"},
		{name: "v2 approved with the v2 flag", manifest: validSchemaVersion2ManifestYAML, status: "pending_review", approveV2: true,
			wantCode: http.StatusOK, wantStatus: "active", wantAudit: true},
		{name: "invalid v2 refused even with the v2 flag", manifest: "schema_version: \"2\"\nname: x\nversion: 1.0.0\n", status: "pending_review", approveV2: true,
			wantCode: http.StatusConflict, wantStatus: "pending_review", wantBodyPart: "v2 substrate"},
		{name: "v2 already active is 409", manifest: validSchemaVersion2ManifestYAML, status: "active", approveV2: true,
			wantCode: http.StatusConflict, wantStatus: "active", wantBodyPart: "pending_review"},
		{name: "v2 removed is 409", manifest: validSchemaVersion2ManifestYAML, status: "removed", approveV2: true,
			wantCode: http.StatusConflict, wantStatus: "removed", wantBodyPart: "pending_review"},
		{name: "v2 pending_key_approval is 409", manifest: validSchemaVersion2ManifestYAML, status: "pending_key_approval", approveV2: true,
			wantCode: http.StatusConflict, wantStatus: "pending_key_approval", wantBodyPart: "pending_review"},
		{name: "v1 already active is 409", manifest: instanceConfigManifestNoSchema, status: "active",
			wantCode: http.StatusConflict, wantStatus: "active", wantBodyPart: "pending_review"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixedClock := func() time.Time { return time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC) }
			q := newFakePluginQuerier()
			q.seedPlugin(db.Plugin{
				ID: "plugin-1", Name: "test-plugin", PluginVersion: "1.0.0",
				ManifestSnapshot: tc.manifest, Status: tc.status, Version: 0,
			})
			h := newTestPluginHandler(q, fixedClock, testPluginHandlerConfig{approveV2: tc.approveV2})

			req := withChiParams(httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/plugin-1/approve", nil),
				map[string]string{"id": "plugin-1"})
			rec := httptest.NewRecorder()
			h.ApprovePlugin(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantBodyPart != "" && !strings.Contains(rec.Body.String(), tc.wantBodyPart) {
				t.Errorf("body = %s, want it to contain %q", rec.Body.String(), tc.wantBodyPart)
			}
			if got := q.plugins["plugin-1"].Status; got != tc.wantStatus {
				t.Errorf("plugin.Status = %q, want %q", got, tc.wantStatus)
			}
			gotAudit := false
			for _, ev := range q.auditEvents {
				if ev.EventType == auditReviewApproved {
					gotAudit = true
				}
			}
			if gotAudit != tc.wantAudit {
				t.Errorf("plugin_review_approved emitted = %v, want %v", gotAudit, tc.wantAudit)
			}
		})
	}
}

// TestApprovePlugin_AuditRecordsV2Image checks that a v2 approval's audit event
// names the pinned image, and that a v1 approval's payload gains no new keys.
func TestApprovePlugin_AuditRecordsV2Image(t *testing.T) {
	tests := []struct {
		name      string
		manifest  string
		wantImage bool
	}{
		{"v2", validSchemaVersion2ManifestYAML, true},
		{"v1", instanceConfigManifestNoSchema, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := newFakePluginQuerier()
			q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "p", PluginVersion: "1.0.0",
				ManifestSnapshot: tc.manifest, Status: "pending_review"})
			h := newTestPluginHandler(q, nil, testPluginHandlerConfig{approveV2: true})

			req := withChiParams(httptest.NewRequest(http.MethodPost, "/", nil), map[string]string{"id": "plugin-1"})
			rec := httptest.NewRecorder()
			h.ApprovePlugin(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
			}
			for _, ev := range q.auditEvents {
				if ev.EventType != auditReviewApproved {
					continue
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(ev.PayloadJson), &payload); err != nil {
					t.Fatalf("audit payload: %v", err)
				}
				if _, hasImage := payload["image"]; hasImage != tc.wantImage {
					t.Errorf("payload has image = %v, want %v (payload %v)", hasImage, tc.wantImage, payload)
				}
				return
			}
			t.Fatal("no plugin_review_approved event")
		})
	}
}

func TestGetPluginDetail_V2Consent(t *testing.T) {
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{
		ID: "plugin-v2", Name: "v2-plugin", PluginVersion: "1.2.0",
		ManifestSnapshot: fullV2DetailManifest, Status: "pending_review",
		CreatedAt: "2026-01-01T00:00:00Z",
	})
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

	req := withChiParams(httptest.NewRequest(http.MethodGet, "/", nil), map[string]string{"id": "plugin-v2"})
	rec := httptest.NewRecorder()
	h.GetPluginDetail(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp pluginDetailResponse
	if err := json.Unmarshal(parseDataResponse(t, rec), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.ManifestVersion != 2 {
		t.Errorf("manifest_version = %d, want 2", resp.ManifestVersion)
	}
	if resp.AuthStrategy != "oauth2_authcode" || len(resp.Tier2Capabilities) != 1 {
		t.Errorf("top-level auth/tier2 = %q/%v", resp.AuthStrategy, resp.Tier2Capabilities)
	}
	v2 := resp.V2
	if v2 == nil {
		t.Fatal("v2 consent block missing for a v2 row")
	}
	wantDigest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if v2.Image.Repository != "ghcr.io/acme/v2-plugin" || v2.Image.Digest != wantDigest ||
		v2.Image.Identifier != v2.Image.Repository+"@"+wantDigest {
		t.Errorf("image = %+v", v2.Image)
	}
	if v2.Image.TransportType != "streamable-http" || v2.Image.TransportPort != 8080 {
		t.Errorf("transport = %q:%d", v2.Image.TransportType, v2.Image.TransportPort)
	}
	if len(v2.Egress) != 1 || v2.Egress[0].Domain != "api.example.com" || v2.Egress[0].Reason != "calls the API" {
		t.Errorf("egress = %+v", v2.Egress)
	}
	if v2.Resources == nil || v2.Resources.MemoryMB != 256 || v2.Resources.CPUMillicores != 500 {
		t.Errorf("resources = %+v", v2.Resources)
	}
	if got := strings.Join(v2.Profiles.Declared, ","); got != "tool_provider,event_source,human_channel,identity_provider" {
		t.Errorf("profiles = %s", got)
	}
	if v2.Profiles.HumanChannelAssurance != "weak" || len(v2.Profiles.IdentityLinkMethods) != 1 {
		t.Errorf("profile details = %+v", v2.Profiles)
	}
	if len(v2.Tools) != 1 || v2.Tools[0].ElicitationKind != "permission" {
		t.Errorf("tools = %+v", v2.Tools)
	}
	if len(v2.EventKinds) != 1 || v2.EventKinds[0].Kind != "message_posted" {
		t.Errorf("event_kinds = %+v", v2.EventKinds)
	}
	if v2.Auth.Strategy != "oauth2_authcode" || v2.Auth.OAuth == nil ||
		v2.Auth.OAuth.TokenURL != "https://example.com/token" || len(v2.Auth.OAuth.Scopes) != 2 {
		t.Errorf("auth = %+v", v2.Auth)
	}
	schema, ok := v2.ConfigSchema.(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Errorf("config_schema = %#v, want the manifest's object schema", v2.ConfigSchema)
	}
}

func TestGetPluginDetail_V2MinimalHasEmptyArraysNotNull(t *testing.T) {
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{ID: "plugin-v2", Name: "t", PluginVersion: "1.0.0",
		ManifestSnapshot: validSchemaVersion2ManifestYAML, Status: "pending_review"})
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

	req := withChiParams(httptest.NewRequest(http.MethodGet, "/", nil), map[string]string{"id": "plugin-v2"})
	rec := httptest.NewRecorder()
	h.GetPluginDetail(rec, req)

	var raw struct {
		V2 map[string]json.RawMessage `json:"v2"`
	}
	if err := json.Unmarshal(parseDataResponse(t, rec), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"egress", "tools", "event_kinds"} {
		if string(raw.V2[key]) != "[]" {
			t.Errorf("v2.%s = %s, want []", key, raw.V2[key])
		}
	}
	if string(raw.V2["resources"]) != "null" {
		t.Errorf("v2.resources = %s, want null when the manifest declares none", raw.V2["resources"])
	}
}

// TestGetPluginDetail_V1HasNoV2Key keeps v1 responses unchanged: the v2 block
// is an omitempty addition and must not appear at all.
func TestGetPluginDetail_V1HasNoV2Key(t *testing.T) {
	q := newFakePluginQuerier()
	q.seedPlugin(db.Plugin{ID: "plugin-1", Name: "full-plugin", PluginVersion: "2.3.1",
		ManifestSnapshot: fullDetailManifest, Status: "pending_review"})
	h := newTestPluginHandler(q, nil, testPluginHandlerConfig{})

	req := withChiParams(httptest.NewRequest(http.MethodGet, "/", nil), map[string]string{"id": "plugin-1"})
	rec := httptest.NewRecorder()
	h.GetPluginDetail(rec, req)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(parseDataResponse(t, rec), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["v2"]; present {
		t.Error("v1 detail response must not carry a v2 key")
	}
}
