package hostendpoint

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/mcp"
)

// discoverBody builds a well-formed server/discover request body. Tests
// mutate copies of it to produce each violation.
func discoverBody(version string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "server/discover",
		"params": map[string]any{
			"_meta": map[string]any{
				mcp.MetaKeyProtocolVersion:    version,
				mcp.MetaKeyClientCapabilities: map[string]any{},
			},
		},
	}
}

func postJSON(t *testing.T, handler http.Handler, body map[string]any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

type jsonrpcEnvelope struct {
	Result map[string]any `json:"result"`
	Error  *struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	} `json:"error"`
}

func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) jsonrpcEnvelope {
	t.Helper()
	var env jsonrpcEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return env
}

func modernHeaders() map[string]string {
	return map[string]string{
		"MCP-Protocol-Version": mcp.ProtocolVersion20260728,
		"Mcp-Method":           "server/discover",
	}
}

func TestServerDiscover(t *testing.T) {
	srv := NewServer()

	t.Run("well-formed discover succeeds and declares the endpoint version", func(t *testing.T) {
		w := postJSON(t, srv, discoverBody(mcp.ProtocolVersion20260728), modernHeaders())
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
		}
		env := decodeEnvelope(t, w)
		if env.Error != nil {
			t.Fatalf("unexpected error: %+v", env.Error)
		}
		if got := env.Result["resultType"]; got != "complete" {
			t.Errorf("resultType = %v, want complete", got)
		}
		versions, _ := env.Result["supportedVersions"].([]any)
		if len(versions) != 1 || versions[0] != mcp.ProtocolVersion20260728 {
			t.Errorf("supportedVersions = %v, want [%s]", versions, mcp.ProtocolVersion20260728)
		}
		// The DoD line: the endpoint's declared version is readable. This is
		// the ADR-042 per-service version for the host endpoint, in place of
		// a proto package version.
		meta, _ := env.Result["_meta"].(map[string]any)
		info, _ := meta[mcp.MetaKeyServerInfo].(map[string]any)
		if info["name"] != ServerName {
			t.Errorf("serverInfo.name = %v, want %s", info["name"], ServerName)
		}
		if info["version"] != Version {
			t.Errorf("serverInfo.version = %v, want %s", info["version"], Version)
		}
	})

	// The A4/A1 validation table. Expected codes mirror internal/mcp's
	// registry: header problems are ErrCodeHeaderMismatch, missing _meta
	// body fields are ErrCodeInvalidParams — distinct on purpose, since a
	// client bug must not read as a version problem (errorcodes.go).
	rejections := []struct {
		name     string
		mutate   func(body map[string]any, headers map[string]string)
		wantCode int
	}{
		{
			name: "missing MCP-Protocol-Version header",
			mutate: func(_ map[string]any, h map[string]string) {
				delete(h, "MCP-Protocol-Version")
			},
			wantCode: mcp.ErrCodeHeaderMismatch,
		},
		{
			name: "missing Mcp-Method header",
			mutate: func(_ map[string]any, h map[string]string) {
				delete(h, "Mcp-Method")
			},
			wantCode: mcp.ErrCodeHeaderMismatch,
		},
		{
			name: "Mcp-Method header does not match body method",
			mutate: func(_ map[string]any, h map[string]string) {
				h["Mcp-Method"] = "tools/list"
			},
			wantCode: mcp.ErrCodeHeaderMismatch,
		},
		{
			name: "header version does not match body version",
			mutate: func(b map[string]any, _ map[string]string) {
				meta := b["params"].(map[string]any)["_meta"].(map[string]any)
				meta[mcp.MetaKeyProtocolVersion] = "2025-11-25"
			},
			wantCode: mcp.ErrCodeHeaderMismatch,
		},
		{
			name: "missing _meta protocolVersion field",
			mutate: func(b map[string]any, _ map[string]string) {
				meta := b["params"].(map[string]any)["_meta"].(map[string]any)
				delete(meta, mcp.MetaKeyProtocolVersion)
			},
			wantCode: mcp.ErrCodeInvalidParams,
		},
		{
			name: "missing _meta clientCapabilities field",
			mutate: func(b map[string]any, _ map[string]string) {
				meta := b["params"].(map[string]any)["_meta"].(map[string]any)
				delete(meta, mcp.MetaKeyClientCapabilities)
			},
			wantCode: mcp.ErrCodeInvalidParams,
		},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			body := discoverBody(mcp.ProtocolVersion20260728)
			headers := modernHeaders()
			tc.mutate(body, headers)
			w := postJSON(t, srv, body, headers)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
			}
			env := decodeEnvelope(t, w)
			if env.Error == nil {
				t.Fatal("expected a JSON-RPC error")
			}
			if env.Error.Code != tc.wantCode {
				t.Errorf("error code = %d, want %d (%s)", env.Error.Code, tc.wantCode, env.Error.Message)
			}
		})
	}

	t.Run("unsupported protocol version lists the supported set", func(t *testing.T) {
		// Header and body agree on a version the endpoint does not speak, so
		// this passes the mismatch checks and fails version negotiation.
		body := discoverBody("2025-11-25")
		headers := modernHeaders()
		headers["MCP-Protocol-Version"] = "2025-11-25"
		w := postJSON(t, srv, body, headers)
		env := decodeEnvelope(t, w)
		if env.Error == nil || env.Error.Code != mcp.ErrCodeUnsupportedProtocolVersion {
			t.Fatalf("expected code %d, got %+v", mcp.ErrCodeUnsupportedProtocolVersion, env.Error)
		}
		supported, _ := env.Error.Data["supported"].([]any)
		if len(supported) != 1 || supported[0] != mcp.ProtocolVersion20260728 {
			t.Errorf("data.supported = %v, want [%s]", supported, mcp.ProtocolVersion20260728)
		}
	})

	t.Run("legacy initialize is method-not-found — the endpoint is modern-only", func(t *testing.T) {
		body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}
		w := postJSON(t, srv, body, nil)
		env := decodeEnvelope(t, w)
		if env.Error == nil || env.Error.Code != mcp.ErrCodeMethodNotFound {
			t.Fatalf("expected code %d, got %+v", mcp.ErrCodeMethodNotFound, env.Error)
		}
		// A legacy session header in the response would suggest the endpoint
		// negotiated something; it must not.
		if got := w.Header().Get("Mcp-Session-Id"); got != "" {
			t.Errorf("Mcp-Session-Id = %q, want unset", got)
		}
	})

	t.Run("GET is not MCP traffic", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", w.Code)
		}
	})
}
