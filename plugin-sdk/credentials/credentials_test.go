package credentials

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestApply_StaticAPIKey verifies that Apply sets the correct header value.
func TestApply_StaticAPIKey(t *testing.T) {
	tests := []struct {
		name        string
		creds       Credentials
		wantHeader  string
		wantValue   string
	}{
		{
			name: "with scheme",
			creds: Credentials{
				Strategy: StrategyStaticAPIKey,
				StaticAPIKey: &StaticAPIKey{
					HeaderName: "Authorization",
					Scheme:     "Bearer",
					APIKey:     "tok-123",
				},
			},
			wantHeader: "Authorization",
			wantValue:  "Bearer tok-123",
		},
		{
			name: "without scheme — no leading space",
			creds: Credentials{
				Strategy: StrategyStaticAPIKey,
				StaticAPIKey: &StaticAPIKey{
					HeaderName: "X-API-Key",
					APIKey:     "raw-key",
				},
			},
			wantHeader: "X-API-Key",
			wantValue:  "raw-key",
		},
		{
			name: "empty scheme is same as absent scheme",
			creds: Credentials{
				Strategy: StrategyStaticAPIKey,
				StaticAPIKey: &StaticAPIKey{
					HeaderName: "X-Token",
					Scheme:     "",
					APIKey:     "val",
				},
			},
			wantHeader: "X-Token",
			wantValue:  "val",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			tt.creds.Apply(req)
			got := req.Header.Get(tt.wantHeader)
			if got != tt.wantValue {
				t.Errorf("header %q: got %q, want %q", tt.wantHeader, got, tt.wantValue)
			}
		})
	}
}

// TestApply_HeaderSet verifies that every header in the set is injected.
func TestApply_HeaderSet(t *testing.T) {
	creds := Credentials{
		Strategy: StrategyHeaderSet,
		HeaderSet: &HeaderSet{
			Headers: []NamedHeader{
				{Name: "X-Org-ID", Value: "org-123"},
				{Name: "X-Token", Value: "tok-abc"},
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	creds.Apply(req)

	if got := req.Header.Get("X-Org-ID"); got != "org-123" {
		t.Errorf("X-Org-ID: got %q, want org-123", got)
	}
	if got := req.Header.Get("X-Token"); got != "tok-abc" {
		t.Errorf("X-Token: got %q, want tok-abc", got)
	}
}

// TestApply_BasicAuth verifies that basic auth is set via SetBasicAuth.
func TestApply_BasicAuth(t *testing.T) {
	creds := Credentials{
		Strategy:  StrategyBasicAuth,
		BasicAuth: &BasicAuth{Username: "alice", Password: "s3cr3t"},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	creds.Apply(req)

	u, p, ok := req.BasicAuth()
	if !ok {
		t.Fatal("BasicAuth not set")
	}
	if u != "alice" {
		t.Errorf("username: got %q, want alice", u)
	}
	if p != "s3cr3t" {
		t.Errorf("password: got %q, want s3cr3t", p)
	}
}

// TestApply_None verifies that a none strategy is a no-op (no headers set).
func TestApply_None(t *testing.T) {
	creds := Credentials{Strategy: StrategyNone}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	originalHeaders := req.Header.Clone()
	creds.Apply(req)

	for k := range req.Header {
		if _, existed := originalHeaders[k]; !existed {
			t.Errorf("Apply(none) set unexpected header %q", k)
		}
	}
}

// TestApply_OAuth2_NoOp verifies that OAuth2 strategies are no-ops.
func TestApply_OAuth2_NoOp(t *testing.T) {
	for _, strategy := range []string{StrategyOAuth2Authcode, StrategyOAuth2Clientcred} {
		t.Run(strategy, func(t *testing.T) {
			creds := Credentials{Strategy: strategy}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			creds.Apply(req) // must not panic or set headers
		})
	}
}

// TestUnmarshal_RoundTrip verifies Marshal → Unmarshal for each strategy.
func TestUnmarshal_RoundTrip(t *testing.T) {
	inputs := []Credentials{
		{Strategy: StrategyNone},
		{
			Strategy:     StrategyStaticAPIKey,
			StaticAPIKey: &StaticAPIKey{HeaderName: "X-Key", Scheme: "Bearer", APIKey: "val"},
		},
		{
			Strategy:  StrategyHeaderSet,
			HeaderSet: &HeaderSet{Headers: []NamedHeader{{Name: "X-A", Value: "a"}}},
		},
		{
			Strategy:  StrategyBasicAuth,
			BasicAuth: &BasicAuth{Username: "bob", Password: "pw"},
		},
		{Strategy: StrategyOAuth2Authcode},
		{Strategy: StrategyOAuth2Clientcred},
	}

	for _, input := range inputs {
		t.Run(input.Strategy, func(t *testing.T) {
			b, err := json.Marshal(input)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got, err := Unmarshal(b)
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.Strategy != input.Strategy {
				t.Errorf("Strategy: got %q, want %q", got.Strategy, input.Strategy)
			}
			// Re-marshal for deep equality.
			wantJSON, _ := json.Marshal(input)
			gotJSON, _ := json.Marshal(got)
			if string(wantJSON) != string(gotJSON) {
				t.Errorf("round-trip mismatch:\n got:  %s\nwant: %s", gotJSON, wantJSON)
			}
		})
	}
}
