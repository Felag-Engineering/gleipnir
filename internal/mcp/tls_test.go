package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// legacyToolHandler is a minimal legacy-shaped MCP handler serving one tool
// named toolName, returned as a bare http.Handler rather than an already
// running httptest.Server — mirrors sessionCountingLegacyServer
// (cache_test.go) but lets the caller front it with
// testutil.StartTLSServer and a caller-chosen certificate, which
// httptest.NewTLSServer's fixed internal certificate does not allow.
func legacyToolHandler(toolName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		method, _ := req["method"].(string)
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-session")
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusOK)
		case methodToolsList:
			writeJSON(w, map[string]any{
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": toolName, "description": "a tool", "inputSchema": map[string]any{"type": "object"}},
					},
				},
			})
		case methodToolsCall:
			writeJSON(w, map[string]any{
				"jsonrpc": "2.0", "id": req["id"],
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "ok"}},
					"isError": false,
				},
			})
		default:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("404 page not found")) //nolint:errcheck
		}
	})
}

// mustPool parses pemText into an *x509.CertPool via ParseCACertBundle,
// failing the test on any parse error.
func mustPool(t *testing.T, pemText string) *x509.CertPool {
	t.Helper()
	_, pool, err := ParseCACertBundle(pemText)
	if err != nil {
		t.Fatalf("ParseCACertBundle: %v", err)
	}
	return pool
}

// TestClient_CACertTrust covers issue #928's core trust behaviors: a client
// pinned to the correct CA succeeds, an unpinned client refuses a
// privately-signed certificate, a client pinned to the WRONG CA refuses even
// though its own pin is valid, and a hostname mismatch is diagnosed as such.
// Every case runs both DiscoverTools and CallTool.
func TestClient_CACertTrust(t *testing.T) {
	correctCA := testutil.NewTestCA(t)
	otherCA := testutil.NewTestCA(t)

	loopback := net.ParseIP("127.0.0.1")
	correctLeaf := correctCA.IssueServerCert(t, nil, []net.IP{loopback})
	wrongHostLeaf := correctCA.IssueServerCert(t, []string{"other.invalid"}, nil)

	tests := []struct {
		name       string
		leaf       tls.Certificate
		clientOpts []ClientOption
		wantErr    bool
		wantReason string // substring of *TLSVerificationError.Reason
	}{
		{
			name:       "correct CA pinned succeeds",
			leaf:       correctLeaf,
			clientOpts: []ClientOption{WithRootCAs(mustPool(t, correctCA.PEM))},
		},
		{
			name:       "no CA configured refuses an untrusted certificate",
			leaf:       correctLeaf,
			clientOpts: nil,
			wantErr:    true,
			wantReason: "unknown authority",
		},
		{
			name:       "a different CA pinned refuses the server's real CA",
			leaf:       correctLeaf,
			clientOpts: []ClientOption{WithRootCAs(mustPool(t, otherCA.PEM))},
			wantErr:    true,
			wantReason: "not signed by this server's configured CA certificate",
		},
		{
			name:       "correct CA but the leaf is issued for a different host",
			leaf:       wrongHostLeaf,
			clientOpts: []ClientOption{WithRootCAs(mustPool(t, correctCA.PEM))},
			wantErr:    true,
			wantReason: "not valid for the host",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.StartTLSServer(t, legacyToolHandler("tool-a"), tc.leaf)
			client := NewClient(srv.URL, tc.clientOpts...)

			_, discoverErr := client.DiscoverTools(context.Background())
			_, callErr := client.CallTool(context.Background(), "tool-a", nil, CallOptions{})

			for label, err := range map[string]error{"DiscoverTools": discoverErr, "CallTool": callErr} {
				if !tc.wantErr {
					if err != nil {
						t.Errorf("%s: unexpected error: %v", label, err)
					}
					continue
				}
				if err == nil {
					t.Fatalf("%s: want error, got nil", label)
				}
				var tlsErr *TLSVerificationError
				if !errors.As(err, &tlsErr) {
					t.Fatalf("%s error = %v (%T), want *TLSVerificationError", label, err, err)
				}
				if !strings.Contains(tlsErr.Error(), "TLS certificate verification failed") {
					t.Errorf("%s: TLSVerificationError.Error() = %q, want it to contain %q",
						label, tlsErr.Error(), "TLS certificate verification failed")
				}
				if !strings.Contains(tlsErr.Reason, tc.wantReason) {
					t.Errorf("%s: reason = %q, want substring %q", label, tlsErr.Reason, tc.wantReason)
				}
			}
		})
	}
}

// TestWithRootCAs_NeverSkipsVerification pins down the hard #928 invariant at
// the transport level, not just behaviorally: InsecureSkipVerify must be
// false and RootCAs must be exactly the pool this option was given.
func TestWithRootCAs_NeverSkipsVerification(t *testing.T) {
	ca := testutil.NewTestCA(t)
	_, pool, err := ParseCACertBundle(ca.PEM)
	if err != nil {
		t.Fatalf("ParseCACertBundle: %v", err)
	}

	client := NewClient("https://example.invalid", WithRootCAs(pool))

	tr, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client.httpClient.Transport = %T, want *http.Transport", client.httpClient.Transport)
	}
	if tr.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig is nil")
	}
	if tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify = true, want false — issue #928 forbids this under any configuration")
	}
	if tr.TLSClientConfig.RootCAs != pool {
		t.Error("RootCAs does not point at the pool WithRootCAs was given")
	}
	if !client.pinnedCA {
		t.Error("pinnedCA = false, want true after WithRootCAs")
	}
}

// TestRegistry_CACertPinEndToEnd is the first acceptance item (issue #928)
// exercised end to end through the real registry path: a row with a pinned
// ca_cert_pem, discovered and resolved like any other server, both discovers
// and calls its tool successfully.
func TestRegistry_CACertPinEndToEnd(t *testing.T) {
	ca := testutil.NewTestCA(t)
	leaf := ca.IssueServerCert(t, nil, []net.IP{net.ParseIP("127.0.0.1")})
	srv := testutil.StartTLSServer(t, legacyToolHandler("tool-a"), leaf)

	reg, store := newTestRegistry(t)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	created, err := store.Queries().CreateMCPServer(context.Background(), db.CreateMCPServerParams{
		ID:        model.NewULID(),
		Name:      "ca-server",
		Url:       srv.URL,
		CreatedAt: now,
		CaCertPem: &ca.PEM,
	})
	if err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}

	if _, err := reg.RefreshTools(context.Background(), created.ID); err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	client, toolName, err := reg.ResolveToolByName(context.Background(), "ca-server.tool-a")
	if err != nil {
		t.Fatalf("ResolveToolByName: %v", err)
	}
	if _, err := client.CallTool(context.Background(), toolName, nil, CallOptions{}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
}
