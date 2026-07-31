package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/infra/version"
)

// isZeroProbeResult reports whether res is the zero ProbeResult. ProbeResult
// contains a slice field, so it is not comparable with ==.
func isZeroProbeResult(res ProbeResult) bool {
	return res.Version == "" && res.Era == "" && res.ServerSupported == nil
}

func TestClassifyDiscoverResponse(t *testing.T) {
	tests := []struct {
		name           string
		status         int
		payload        string
		wantOutcome    discoverOutcome
		wantAdvertised []string
		wantErrCode    int // 0 when no ModernErr is expected
	}{
		{
			name:           "200 result with supportedVersions",
			status:         http.StatusOK,
			payload:        `{"jsonrpc":"2.0","id":1,"result":{"supportedVersions":["2026-07-28"]}}`,
			wantOutcome:    discoverModern,
			wantAdvertised: []string{"2026-07-28"},
		},
		{
			name:           "200 result with multiple supportedVersions",
			status:         http.StatusOK,
			payload:        `{"jsonrpc":"2.0","id":1,"result":{"supportedVersions":["2026-07-28","2025-11-25"]}}`,
			wantOutcome:    discoverModern,
			wantAdvertised: []string{"2026-07-28", "2025-11-25"},
		},
		{
			name:           "200 result with resultType absent",
			status:         http.StatusOK,
			payload:        `{"jsonrpc":"2.0","id":1,"result":{"supportedVersions":["2026-07-28"]}}`,
			wantOutcome:    discoverModern,
			wantAdvertised: []string{"2026-07-28"},
		},
		{
			name:           "200 result with resultType complete",
			status:         http.StatusOK,
			payload:        `{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","supportedVersions":["2026-07-28"]}}`,
			wantOutcome:    discoverModern,
			wantAdvertised: []string{"2026-07-28"},
		},
		{
			name:        "200 result shaped like today's dumb tools/list fakes",
			status:      http.StatusOK,
			payload:     `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"t"}]}}`,
			wantOutcome: discoverLegacy,
		},
		{
			name:        "200 empty result",
			status:      http.StatusOK,
			payload:     `{"jsonrpc":"2.0","id":1,"result":{}}`,
			wantOutcome: discoverLegacy,
		},
		{
			name:        "200 error -32601 method not found",
			status:      http.StatusOK,
			payload:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`,
			wantOutcome: discoverLegacy,
		},
		{
			name:        "200 non-JSON body",
			status:      http.StatusOK,
			payload:     `not json`,
			wantOutcome: discoverLegacy,
		},
		{
			name:           "400 error -32022 with data.supported",
			status:         http.StatusBadRequest,
			payload:        `{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"Unsupported protocol version","data":{"supported":["2026-07-28","2025-11-25"],"requested":"1900-01-01"}}}`,
			wantOutcome:    discoverModern,
			wantAdvertised: []string{"2026-07-28", "2025-11-25"},
			wantErrCode:    -32022,
		},
		{
			name:        "400 error -32020 HeaderMismatch",
			status:      http.StatusBadRequest,
			payload:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32020,"message":"Header mismatch"}}`,
			wantOutcome: discoverModern,
			wantErrCode: -32020,
		},
		{
			name:        "400 error -32021 MissingRequiredClientCapability with data.requiredCapabilities",
			status:      http.StatusBadRequest,
			payload:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32021,"message":"Missing required client capability","data":{"requiredCapabilities":["tools"]}}}`,
			wantOutcome: discoverModern,
			wantErrCode: -32021,
		},
		{
			name:        "400 error -32602 Invalid params",
			status:      http.StatusBadRequest,
			payload:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`,
			wantOutcome: discoverLegacy,
		},
		{
			name:        "400 empty body",
			status:      http.StatusBadRequest,
			payload:     ``,
			wantOutcome: discoverLegacy,
		},
		{
			name:        "400 HTML body",
			status:      http.StatusBadRequest,
			payload:     `<html><body>Bad Request</body></html>`,
			wantOutcome: discoverLegacy,
		},
		{
			name:        "404 page not found",
			status:      http.StatusNotFound,
			payload:     `404 page not found`,
			wantOutcome: discoverLegacy,
		},
		{
			name:        "404 error -32601",
			status:      http.StatusNotFound,
			payload:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`,
			wantOutcome: discoverLegacy,
		},
		{
			name:        "405 empty body",
			status:      http.StatusMethodNotAllowed,
			payload:     ``,
			wantOutcome: discoverLegacy,
		},
		{
			name:        "401 any body",
			status:      http.StatusUnauthorized,
			payload:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"Unsupported protocol version"}}`,
			wantOutcome: discoverUnclassified,
		},
		{
			name:        "429 any body",
			status:      http.StatusTooManyRequests,
			payload:     `rate limited`,
			wantOutcome: discoverUnclassified,
		},
		{
			name:        "500 error -32022",
			status:      http.StatusInternalServerError,
			payload:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"Unsupported protocol version"}}`,
			wantOutcome: discoverUnclassified,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDiscoverResponse(tc.status, []byte(tc.payload))
			if got.Outcome != tc.wantOutcome {
				t.Errorf("Outcome = %v, want %v", got.Outcome, tc.wantOutcome)
			}
			if len(got.Advertised) != len(tc.wantAdvertised) {
				t.Errorf("Advertised = %v, want %v", got.Advertised, tc.wantAdvertised)
			} else {
				for i, v := range tc.wantAdvertised {
					if got.Advertised[i] != v {
						t.Errorf("Advertised = %v, want %v", got.Advertised, tc.wantAdvertised)
						break
					}
				}
			}
			if tc.wantErrCode != 0 {
				if got.ModernErr == nil {
					t.Fatalf("ModernErr = nil, want code %d", tc.wantErrCode)
				}
				if got.ModernErr.Code != tc.wantErrCode {
					t.Errorf("ModernErr.Code = %d, want %d", got.ModernErr.Code, tc.wantErrCode)
				}
			} else if got.ModernErr != nil {
				t.Errorf("ModernErr = %v, want nil", got.ModernErr)
			}
		})
	}
}

func TestSelectProtocolVersion(t *testing.T) {
	tests := []struct {
		name       string
		advertised []string
		want       string
	}{
		{
			name:       "exact match",
			advertised: []string{"2026-07-28"},
			want:       "2026-07-28",
		},
		{
			name:       "our preference wins over server order",
			advertised: []string{"2025-11-25", "2026-07-28"},
			want:       "2026-07-28",
		},
		{
			name:       "only unknown versions",
			advertised: []string{"2025-11-25"},
			want:       "",
		},
		{
			name:       "empty",
			advertised: []string{},
			want:       "",
		},
		{
			name:       "nil",
			advertised: nil,
			want:       "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := selectProtocolVersion(tc.advertised); got != tc.want {
				t.Errorf("selectProtocolVersion(%v) = %q, want %q", tc.advertised, got, tc.want)
			}
		})
	}
}

func TestProbeProtocolVersion(t *testing.T) {
	tests := []struct {
		name        string
		opts        []FakeServerOption
		wantVersion string
		wantEra     ProtocolEra
		wantErr     bool
		wantErrIs   error
	}{
		{
			name:        "FakeModern pins 2026-07-28",
			opts:        []FakeServerOption{WithFakeMode(FakeModern)},
			wantVersion: "2026-07-28",
			wantEra:     EraModern,
		},
		{
			name: "FakeModern with our version among several supported",
			opts: []FakeServerOption{
				WithFakeMode(FakeModern),
				WithFakeSupportedVersions("2025-11-25", "2026-07-28"),
			},
			wantVersion: "2026-07-28",
			wantEra:     EraModern,
		},
		{
			name: "modern DiscoverResult advertising only versions we do not speak (non-compliant server) → error, no pin",
			opts: []FakeServerOption{
				WithFakeMode(FakeModern),
				WithFakeSupportedVersions("2025-11-25"),
			},
			wantErr:   true,
			wantErrIs: ErrNoCompatibleProtocolVersion,
		},
		{
			name: "FakeVersionMismatch with only an incompatible version → error, no pin",
			opts: []FakeServerOption{
				WithFakeMode(FakeVersionMismatch),
				WithFakeSupportedVersions("2025-11-25"),
			},
			wantErr:   true,
			wantErrIs: ErrNoCompatibleProtocolVersion,
		},
		{
			name: "FakeVersionMismatch data.supported parsed and overlap selected",
			opts: []FakeServerOption{
				WithFakeMode(FakeVersionMismatch),
				WithFakeSupportedVersions("2026-07-28", "2025-11-25"),
			},
			wantVersion: "2026-07-28",
			wantEra:     EraModern,
		},
		{
			name: "FakeLegacy pins the server's negotiated version",
			opts: []FakeServerOption{
				WithFakeMode(FakeLegacy),
				WithFakeLegacyNegotiatedVersion("2025-03-26"),
			},
			wantVersion: "2025-03-26",
			wantEra:     EraLegacy,
		},
		{
			name:        "FakeLegacy with no negotiated version falls back to the constant",
			opts:        []FakeServerOption{WithFakeMode(FakeLegacy)},
			wantVersion: "2024-11-05",
			wantEra:     EraLegacy,
		},
		{
			name: "FakeLegacy with an inconclusive discover status errors",
			opts: []FakeServerOption{
				WithFakeMode(FakeLegacy),
				WithFakeDiscoverStatus(http.StatusUnauthorized),
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := NewFakeMCPServer(tc.opts...)
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)

			c := NewClient(srv.URL)
			res, err := c.ProbeProtocolVersion(context.Background())

			if tc.wantErr {
				if err == nil {
					t.Fatalf("ProbeProtocolVersion: expected error, got nil (res=%+v)", res)
				}
				if !isZeroProbeResult(res) {
					t.Errorf("ProbeResult = %+v, want zero value on error", res)
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Errorf("errors.Is(err, %v) = false; err = %v", tc.wantErrIs, err)
				}
			} else {
				if err != nil {
					t.Fatalf("ProbeProtocolVersion: unexpected error: %v", err)
				}
				if res.Version != tc.wantVersion {
					t.Errorf("Version = %q, want %q", res.Version, tc.wantVersion)
				}
				if res.Era != tc.wantEra {
					t.Errorf("Era = %q, want %q", res.Era, tc.wantEra)
				}
			}

			if v := fake.Violations(); len(v) != 0 {
				t.Errorf("Violations = %v, want none", v)
			}
		})
	}
}

// TestProbeProtocolVersion_VersionMismatchErrorWrapsJSONRPCError proves the
// -32022 JSONRPCError is reachable via errors.As on the wrapped error.
func TestProbeProtocolVersion_VersionMismatchErrorWrapsJSONRPCError(t *testing.T) {
	fake := NewFakeMCPServer(WithFakeMode(FakeVersionMismatch), WithFakeSupportedVersions("2025-11-25"))
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL)
	_, err := c.ProbeProtocolVersion(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrNoCompatibleProtocolVersion) {
		t.Errorf("errors.Is(err, ErrNoCompatibleProtocolVersion) = false; err = %v", err)
	}
	var rpcErr *JSONRPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("errors.As(err, *JSONRPCError) = false; err = %v", err)
	}
	if rpcErr.Code != -32022 {
		t.Errorf("wrapped JSONRPCError.Code = %d, want -32022", rpcErr.Code)
	}
}

func TestProbeProtocolVersion_TransportError(t *testing.T) {
	// Start and immediately close so the URL is valid but unreachable.
	srv := httptest.NewServer(NewFakeMCPServer())
	url := srv.URL
	srv.Close()

	c := NewClient(url)
	res, err := c.ProbeProtocolVersion(context.Background())
	if err == nil {
		t.Fatal("expected error for unreachable server, got nil")
	}
	if !isZeroProbeResult(res) {
		t.Errorf("ProbeResult = %+v, want zero value on error", res)
	}
}

// TestProbeProtocolVersion_RequestShape asserts the exact wire shape of the
// server/discover request sent by the probe (context.md A1/A4).
func TestProbeProtocolVersion_RequestShape(t *testing.T) {
	fake := NewFakeMCPServer(WithFakeMode(FakeModern))
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL)
	if _, err := c.ProbeProtocolVersion(context.Background()); err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}

	reqs := fake.RequestsFor("server/discover")
	if len(reqs) != 1 {
		t.Fatalf("len(RequestsFor(server/discover)) = %d, want 1", len(reqs))
	}
	req := reqs[0]

	if req.ProtocolHeader != "2026-07-28" {
		t.Errorf("ProtocolHeader = %q, want %q", req.ProtocolHeader, "2026-07-28")
	}
	if req.MethodHeader != "server/discover" {
		t.Errorf("MethodHeader = %q, want %q", req.MethodHeader, "server/discover")
	}
	if req.NameHeader != "" {
		t.Errorf("NameHeader = %q, want empty (not applicable to server/discover)", req.NameHeader)
	}
	if req.SessionHeader != "" {
		t.Errorf("SessionHeader = %q, want empty (sessions are gone in this revision)", req.SessionHeader)
	}
	accept := req.Header.Get("Accept")
	if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
		t.Errorf("Accept = %q, want both application/json and text/event-stream", accept)
	}

	meta := decodeMeta(req.Params)
	if meta == nil {
		t.Fatal("decoded _meta is nil")
	}
	if got := metaString(meta, metaKeyProtocolVersion); got != req.ProtocolHeader {
		t.Errorf("_meta.protocolVersion = %q, want header value %q", got, req.ProtocolHeader)
	}

	var clientInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if raw, ok := meta[metaKeyClientInfo]; !ok {
		t.Error("_meta missing clientInfo")
	} else if err := json.Unmarshal(raw, &clientInfo); err != nil {
		t.Fatalf("unmarshal clientInfo: %v", err)
	} else {
		if clientInfo.Name != "gleipnir" {
			t.Errorf("clientInfo.name = %q, want %q", clientInfo.Name, "gleipnir")
		}
		if clientInfo.Version != version.Version {
			t.Errorf("clientInfo.version = %q, want %q", clientInfo.Version, version.Version)
		}
	}

	if raw, ok := meta[metaKeyClientCapabilities]; !ok {
		t.Error("_meta missing clientCapabilities")
	} else if string(raw) != "{}" {
		t.Errorf("_meta.clientCapabilities = %s, want {}", raw)
	}
}

// TestProbeProtocolVersion_DoesNotReshapeToolCalls is the scope-discipline
// guard: #737 threads the protocol-version pin but must not re-shape any
// tool traffic. #741 inverts this assertion.
func TestProbeProtocolVersion_DoesNotReshapeToolCalls(t *testing.T) {
	fake := NewFakeMCPServer(WithFakeMode(FakeModern))
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL)
	if _, err := c.ProbeProtocolVersion(context.Background()); err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}
	if _, err := c.DiscoverTools(context.Background()); err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}

	reqs := fake.RequestsFor("tools/list")
	if len(reqs) != 1 {
		t.Fatalf("len(RequestsFor(tools/list)) = %d, want 1", len(reqs))
	}
	req := reqs[0]
	if req.ProtocolHeader != "" {
		t.Errorf("tools/list ProtocolHeader = %q, want empty (no 2026-07-28 shaping yet)", req.ProtocolHeader)
	}
	if req.MethodHeader != "" {
		t.Errorf("tools/list MethodHeader = %q, want empty (no 2026-07-28 shaping yet)", req.MethodHeader)
	}
	if req.SessionHeader == "" {
		t.Error("tools/list SessionHeader is empty, want the legacy session id (legacy handshake unchanged)")
	}
}

// TestSanitizedLegacyVersion is the unit-level pin for Finding 1 (security
// review, #737 cycle 2): the sole gate between a legacy server's
// self-reported protocolVersion and persistence in
// mcp_servers.protocol_version.
func TestSanitizedLegacyVersion(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "the legacy constant is accepted verbatim", raw: "2024-11-05", want: "2024-11-05"},
		{name: "another known legacy version is accepted verbatim", raw: "2025-03-26", want: "2025-03-26"},
		{name: "a third known legacy version is accepted verbatim", raw: "2025-06-18", want: "2025-06-18"},
		{name: "empty string is rejected", raw: "", want: ""},
		{name: "the modern version is rejected even though it is well-formed", raw: "2026-07-28", want: ""},
		{name: "an unknown but well-formed date-shaped string is rejected (not on the allowlist)", raw: "1900-01-01", want: ""},
		{name: "a 1 MiB string is rejected by the length bound", raw: strings.Repeat("9", 1<<20), want: ""},
		{name: "a CRLF-bearing string is rejected by the charset bound", raw: "2026-07-28\r\nX-Injected: 1", want: ""},
		{name: "a string one byte over the length bound is rejected", raw: strings.Repeat("1", legacyVersionMaxLen+1), want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizedLegacyVersion(tc.raw); got != tc.want {
				display := tc.raw
				if len(display) > 60 {
					display = display[:60] + "…"
				}
				t.Errorf("sanitizedLegacyVersion(%q) = %q, want %q", display, got, tc.want)
			}
		})
	}
}

// TestLegacyAllowlistDisjointFromModernVersions asserts the structural
// invariant Finding 1 requires: knownLegacyProtocolVersions and
// supportedProtocolVersions must never share an entry, or the discoverLegacy
// branch of ProbeProtocolVersion could emit a value classified modern.
func TestLegacyAllowlistDisjointFromModernVersions(t *testing.T) {
	for _, legacy := range knownLegacyProtocolVersions {
		for _, modern := range supportedProtocolVersions {
			if legacy == modern {
				t.Fatalf("knownLegacyProtocolVersions and supportedProtocolVersions both contain %q; "+
					"the legacy branch would then be capable of emitting a value classified modern", legacy)
			}
		}
	}
}

// TestProbeProtocolVersion_LegacyServerCannotClaimModernVersion is the
// end-to-end Finding 1 reproducer: a server that classifies discoverLegacy
// (fails server/discover) but echoes an untrustworthy protocolVersion on the
// legacy initialize handshake must never have that value pinned verbatim.
// Before the fix, all four of these rows persisted the raw server-chosen
// string, including a value byte-identical to the modern version.
func TestProbeProtocolVersion_LegacyServerCannotClaimModernVersion(t *testing.T) {
	tests := []struct {
		name              string
		negotiatedVersion string
	}{
		{name: "server echoes the exact modern version string", negotiatedVersion: "2026-07-28"},
		{name: "server echoes a 1 MiB string", negotiatedVersion: strings.Repeat("9", 1<<20)},
		{name: "server echoes a CRLF-bearing string", negotiatedVersion: "2026-07-28\r\nX-Injected: 1"},
		{name: "server echoes an unknown but well-formed date-shaped string", negotiatedVersion: "1900-01-01"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := NewFakeMCPServer(WithFakeMode(FakeLegacy), WithFakeLegacyNegotiatedVersion(tc.negotiatedVersion))
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)

			c := NewClient(srv.URL)
			res, err := c.ProbeProtocolVersion(context.Background())
			if err != nil {
				t.Fatalf("ProbeProtocolVersion: %v", err)
			}
			if res.Era != EraLegacy {
				t.Errorf("Era = %q, want %q", res.Era, EraLegacy)
			}
			if res.Version != ProtocolVersionLegacy {
				t.Errorf("Version = %q, want %q (server's self-reported value must be rejected and fall back to the constant)",
					res.Version, ProtocolVersionLegacy)
			}
			for _, modern := range supportedProtocolVersions {
				if res.Version == modern {
					t.Fatalf("Version = %q equals modern version %q; the legacy branch emitted a modern-looking pin", res.Version, modern)
				}
			}
		})
	}
}

// TestSummarizeAdvertised is the Finding 4 unit test (security review, #737
// cycle 2): an untrusted server's advertised protocol-version list must
// never be formatted into an error/log line unbounded.
func TestSummarizeAdvertised(t *testing.T) {
	t.Run("short list is rendered in full, no truncation indicator", func(t *testing.T) {
		got := summarizeAdvertised([]string{"2026-07-28", "2025-11-25"})
		if !strings.Contains(got, "2026-07-28") || !strings.Contains(got, "2025-11-25") {
			t.Errorf("summarizeAdvertised = %q, want both entries present", got)
		}
		if strings.Contains(got, "more)") {
			t.Errorf("summarizeAdvertised = %q, want no truncation indicator for a short list", got)
		}
	})

	t.Run("a 20000-entry, 40-char-each list is capped with a count indicator", func(t *testing.T) {
		advertised := make([]string, 20000)
		for i := range advertised {
			advertised[i] = strings.Repeat("v", 40)
		}
		got := summarizeAdvertised(advertised)
		if len(got) > 10000 {
			t.Errorf("summarizeAdvertised produced %d bytes, want a bounded output (was 820102 bytes before the fix)", len(got))
		}
		if !strings.Contains(got, "more)") {
			t.Errorf("summarizeAdvertised did not include a truncation indicator for 20000 entries")
		}
	})

	t.Run("an individual very long entry is truncated", func(t *testing.T) {
		got := summarizeAdvertised([]string{strings.Repeat("x", 1000)})
		if len(got) > maxAdvertisedVersionLen+20 {
			t.Errorf("summarizeAdvertised produced %d bytes for one long entry, want it truncated to roughly maxAdvertisedVersionLen", len(got))
		}
	})

	t.Run("nil is handled without panicking", func(t *testing.T) {
		if got := summarizeAdvertised(nil); got != "[]" {
			t.Errorf("summarizeAdvertised(nil) = %q, want %q", got, "[]")
		}
	})
}

// TestProbeProtocolVersion_ErrorMessageIsBounded proves summarizeAdvertised
// is actually wired into the error path a hostile server can reach — not
// just unit-tested in isolation.
func TestProbeProtocolVersion_ErrorMessageIsBounded(t *testing.T) {
	advertised := make([]string, 20000)
	for i := range advertised {
		advertised[i] = strings.Repeat("v", 40)
	}
	fake := NewFakeMCPServer(WithFakeMode(FakeModern), WithFakeSupportedVersions(advertised...))
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL)
	_, err := c.ProbeProtocolVersion(context.Background())
	if err == nil {
		t.Fatal("expected error: none of the 20000 advertised versions overlap with what we speak")
	}
	if !errors.Is(err, ErrNoCompatibleProtocolVersion) {
		t.Errorf("errors.Is(err, ErrNoCompatibleProtocolVersion) = false; err = %v", err)
	}
	if n := len(err.Error()); n > 10000 {
		t.Errorf("error message is %d bytes, want a bounded message (was 820102+ bytes before the fix)", n)
	}
}

// TestInconclusiveProbeError is the Finding 6 regression test (security
// review, #737 cycle 2): formatting a nil *HTTPStatusError with %w calls its
// Error() method on a nil receiver and panics. If this guard regresses, this
// test panics and fails the whole package's test binary.
func TestInconclusiveProbeError(t *testing.T) {
	t.Run("nil statusErr does not panic and still reports the status code", func(t *testing.T) {
		err := inconclusiveProbeError(499, nil)
		if err == nil {
			t.Fatal("expected non-nil error")
		}
		if !strings.Contains(err.Error(), "499") {
			t.Errorf("error = %v, want it to mention status 499", err)
		}
	})

	t.Run("non-nil statusErr is wrapped and recoverable via errors.As", func(t *testing.T) {
		statusErr := &HTTPStatusError{StatusCode: 401}
		err := inconclusiveProbeError(401, statusErr)
		var got *HTTPStatusError
		if !errors.As(err, &got) || got != statusErr {
			t.Errorf("errors.As did not recover the wrapped *HTTPStatusError")
		}
	})
}
