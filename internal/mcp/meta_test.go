package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/infra/version"
)

func TestClientCapabilities_WireObject(t *testing.T) {
	tests := []struct {
		name string
		caps ClientCapabilities
		want string
	}{
		{name: "zero value declares nothing", caps: ClientCapabilities{}, want: `{}`},
		{name: "elicitation granted", caps: ClientCapabilities{Elicitation: true}, want: `{"elicitation":{}}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.caps.wireObject())
			if err != nil {
				t.Fatalf("marshal wireObject(): %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("wireObject() = %s, want %s", got, tc.want)
			}
			if strings.Contains(string(got), "sampling") {
				t.Errorf("wireObject() = %s, must never contain \"sampling\"", got)
			}
		})
	}
}

// TestClientCapabilities_HasNoSamplingKnob is the structural enforcement of
// spec §11 / ADR-026 "no API can enable sampling": ClientCapabilities must
// have exactly one field, named "Elicitation", of Kind Bool. This test fails
// if a second field — sampling or otherwise — is ever added.
func TestClientCapabilities_HasNoSamplingKnob(t *testing.T) {
	typ := reflect.TypeOf(ClientCapabilities{})
	if typ.NumField() != 1 {
		t.Fatalf("ClientCapabilities has %d fields, want exactly 1", typ.NumField())
	}
	field := typ.Field(0)
	if field.Name != "Elicitation" {
		t.Errorf("field name = %q, want %q", field.Name, "Elicitation")
	}
	if field.Type.Kind() != reflect.Bool {
		t.Errorf("field kind = %v, want %v", field.Type.Kind(), reflect.Bool)
	}
}

func TestNewRequestMeta_Shape(t *testing.T) {
	meta := newRequestMeta("2026-07-28", ClientCapabilities{})

	if len(meta) != 3 {
		t.Fatalf("len(meta) = %d, want 3: %v", len(meta), meta)
	}
	if got := meta[metaKeyProtocolVersion]; got != "2026-07-28" {
		t.Errorf("protocolVersion = %v, want %q", got, "2026-07-28")
	}

	clientInfo, ok := meta[metaKeyClientInfo].(map[string]any)
	if !ok {
		t.Fatalf("clientInfo = %v (%T), want map[string]any", meta[metaKeyClientInfo], meta[metaKeyClientInfo])
	}
	if clientInfo["name"] != "gleipnir" {
		t.Errorf("clientInfo.name = %v, want %q", clientInfo["name"], "gleipnir")
	}
	if clientInfo["version"] != version.Version {
		t.Errorf("clientInfo.version = %v, want %q", clientInfo["version"], version.Version)
	}

	if _, ok := meta[metaKeyClientCapabilities]; !ok {
		t.Error("meta missing clientCapabilities")
	}
}

func TestClient_RequestMeta_NilOnLegacy(t *testing.T) {
	tests := []struct {
		name    string
		pin     string
		caps    ClientCapabilities
		wantNil bool
	}{
		{name: "unpinned, no caps", pin: "", caps: ClientCapabilities{}, wantNil: true},
		{name: "unpinned, elicitation granted", pin: "", caps: ClientCapabilities{Elicitation: true}, wantNil: true},
		{name: "legacy pin, no caps", pin: ProtocolVersionLegacy, caps: ClientCapabilities{}, wantNil: true},
		{name: "legacy pin, elicitation granted", pin: ProtocolVersionLegacy, caps: ClientCapabilities{Elicitation: true}, wantNil: true},
		{name: "modern pin, no caps", pin: ProtocolVersion20260728, caps: ClientCapabilities{}, wantNil: false},
		{name: "modern pin, elicitation granted", pin: ProtocolVersion20260728, caps: ClientCapabilities{Elicitation: true}, wantNil: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient("http://example.com", WithProtocolVersion(tc.pin))
			got := c.requestMeta(tc.caps)
			if tc.wantNil && got != nil {
				t.Errorf("requestMeta = %v, want nil", got)
			}
			if !tc.wantNil && got == nil {
				t.Error("requestMeta = nil, want non-nil")
			}
		})
	}
}

func TestParseServerInfo(t *testing.T) {
	tests := []struct {
		name    string
		rawMeta json.RawMessage
		want    ServerInfo
	}{
		{
			name:    "nil rawMeta",
			rawMeta: nil,
			want:    ServerInfo{},
		},
		{
			name:    "empty object, absent key",
			rawMeta: json.RawMessage(`{}`),
			want:    ServerInfo{},
		},
		{
			name:    "well-formed serverInfo is captured",
			rawMeta: json.RawMessage(`{"io.modelcontextprotocol/serverInfo":{"name":"acme-mcp","version":"2.3.1"}}`),
			want:    ServerInfo{Name: "acme-mcp", Version: "2.3.1"},
		},
		{
			name:    "non-object serverInfo value yields zero",
			rawMeta: json.RawMessage(`{"io.modelcontextprotocol/serverInfo":"acme-mcp"}`),
			want:    ServerInfo{},
		},
		{
			name:    "invalid JSON inside serverInfo yields zero",
			rawMeta: json.RawMessage(`{"io.modelcontextprotocol/serverInfo":{not json}`),
			want:    ServerInfo{},
		},
		// The following exercise the outer decode — _meta itself is
		// untrusted and optional (#742 cycle 1 Finding 1), so none of these
		// shapes may panic or propagate an error; all must yield the zero
		// ServerInfo.
		{
			name:    "_meta as a JSON string",
			rawMeta: json.RawMessage(`"not-an-object"`),
			want:    ServerInfo{},
		},
		{
			name:    "_meta as a JSON array",
			rawMeta: json.RawMessage(`[1,2,3]`),
			want:    ServerInfo{},
		},
		{
			name:    "_meta as a JSON number",
			rawMeta: json.RawMessage(`42`),
			want:    ServerInfo{},
		},
		{
			name:    "_meta as invalid JSON",
			rawMeta: json.RawMessage(`{not json`),
			want:    ServerInfo{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseServerInfo(tc.rawMeta); got != tc.want {
				t.Errorf("parseServerInfo() = %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("1 MiB name/version fields are bounded", func(t *testing.T) {
		huge := strings.Repeat("x", 1<<20)
		rawMeta := json.RawMessage(`{"io.modelcontextprotocol/serverInfo":{"name":"` + huge + `","version":"` + huge + `"}}`)
		got := parseServerInfo(rawMeta)
		maxLen := maxServerInfoFieldLen + len("…")
		if len(got.Name) > maxLen {
			t.Errorf("len(Name) = %d, want <= %d", len(got.Name), maxLen)
		}
		if len(got.Version) > maxLen {
			t.Errorf("len(Version) = %d, want <= %d", len(got.Version), maxLen)
		}
	})
}
