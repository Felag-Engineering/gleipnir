package mcp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestToolTraffic_ShapingByPinnedVersion is the DoD table test: the same
// DiscoverTools + CallTool sequence, run against a client pinned to each of
// the three protocol states a Registry-built client can have, must produce
// the right request shape. Only an explicit modern pin (ProtocolVersion20260728)
// gets the 2026-07-28 stateless headers and skips the session handshake
// entirely; "" (never probed) and a legacy pin both keep today's session
// shaping unchanged.
func TestToolTraffic_ShapingByPinnedVersion(t *testing.T) {
	tests := []struct {
		name     string
		pin      string
		modern   bool // whether the fake should reject the legacy handshake
		wantMeta bool // whether tools/list and tools/call params must carry _meta
	}{
		{name: "unpinned client keeps legacy shaping", pin: ""},
		{name: "legacy pin keeps legacy shaping", pin: ProtocolVersionLegacy},
		{name: "modern pin gets 2026-07-28 stateless shaping", pin: ProtocolVersion20260728, modern: true, wantMeta: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := []FakeServerOption{WithFakeMode(FakeModern)}
			if tc.modern {
				opts = append(opts, WithFakeRejectLegacyHandshake())
			}
			fake := NewFakeMCPServer(opts...)
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)

			c := NewClient(srv.URL, WithProtocolVersion(tc.pin))
			if _, err := c.DiscoverTools(context.Background()); err != nil {
				t.Fatalf("DiscoverTools: %v", err)
			}
			if _, err := c.CallTool(context.Background(), "tool-a", nil, CallOptions{}); err != nil {
				t.Fatalf("CallTool: %v", err)
			}

			listReqs := fake.RequestsFor(methodToolsList)
			if len(listReqs) != 1 {
				t.Fatalf("len(RequestsFor(tools/list)) = %d, want 1", len(listReqs))
			}
			callReqs := fake.RequestsFor(methodToolsCall)
			if len(callReqs) != 1 {
				t.Fatalf("len(RequestsFor(tools/call)) = %d, want 1", len(callReqs))
			}

			wantProtocol, wantSession := "", "fake-session"
			wantListMethod, wantCallMethod, wantCallName := "", "", ""
			wantInitCount := 1
			if tc.modern {
				wantProtocol = ProtocolVersion20260728
				wantSession = ""
				wantListMethod = methodToolsList
				wantCallMethod = methodToolsCall
				wantCallName = "tool-a"
				wantInitCount = 0
			}

			assertHeaders := func(t *testing.T, req FakeRequest, wantMethod, wantName string) {
				t.Helper()
				if req.ProtocolHeader != wantProtocol {
					t.Errorf("ProtocolHeader = %q, want %q", req.ProtocolHeader, wantProtocol)
				}
				if req.MethodHeader != wantMethod {
					t.Errorf("MethodHeader = %q, want %q", req.MethodHeader, wantMethod)
				}
				if req.NameHeader != wantName {
					t.Errorf("NameHeader = %q, want %q", req.NameHeader, wantName)
				}
				if req.SessionHeader != wantSession {
					t.Errorf("SessionHeader = %q, want %q", req.SessionHeader, wantSession)
				}
			}
			assertHeaders(t, listReqs[0], wantListMethod, "")
			assertHeaders(t, callReqs[0], wantCallMethod, wantCallName)

			assertMeta := func(t *testing.T, req FakeRequest) {
				t.Helper()
				meta := decodeMeta(req.Params)
				if !tc.wantMeta {
					if meta != nil {
						t.Errorf("params carry a _meta key, want none on the legacy path: %s", req.Params)
					}
					return
				}
				if meta == nil {
					t.Fatalf("params carry no _meta, want one on the modern path: %s", req.Params)
				}
				if got := metaString(meta, MetaKeyProtocolVersion); got != req.ProtocolHeader {
					t.Errorf("_meta.protocolVersion = %q, want header value %q", got, req.ProtocolHeader)
				}
				if got := string(meta[MetaKeyClientCapabilities]); got != "{}" {
					t.Errorf("_meta.clientCapabilities = %s, want {}", got)
				}
			}
			assertMeta(t, listReqs[0])
			assertMeta(t, callReqs[0])

			if got := len(fake.RequestsFor("initialize")); got != wantInitCount {
				t.Errorf("len(RequestsFor(initialize)) = %d, want %d", got, wantInitCount)
			}
			if v := fake.Violations(); len(v) != 0 {
				t.Errorf("Violations = %v, want none", v)
			}
		})
	}
}

// TestCallTool_ResultTypeAcrossEras is the response-side companion to
// TestToolTraffic_ShapingByPinnedVersion (which owns request shaping): it
// proves resultType decoding is correct in every era/fixture combination,
// and that decoding is NOT gated on protocol era the way request shaping is.
// The era-shape assertions in each cell are what make the legacy path
// genuinely exercised rather than asserted in prose — 8 of the 12 cells fail
// if any modern request shaping leaks in, and the legacy × "task" cell is
// the explicit proof that a legacy-pinned client returns whatever the server
// sent, verbatim, with a nil error.
//
// WithFakeMode(FakeModern) is inert in this test: FakeMode only governs
// handleDiscover, and this test never calls DiscoverTools or
// ProbeProtocolVersion, so the "modern" fake is not standing in for a modern
// SERVER here — the era distinction below comes entirely from the client's
// WithProtocolVersion pin plus WithFakeRejectLegacyHandshake. For coverage of
// a genuinely legacy-SHAPED server (FakeLegacy, 404-on-discover) exercised
// end to end, see TestBilingualRegistry_LegacyAndModernServersInOneInstance.
func TestCallTool_ResultTypeAcrossEras(t *testing.T) {
	eras := []struct {
		name   string
		pin    string
		strict bool // whether the fake should reject the legacy handshake
	}{
		{name: "unpinned", pin: "", strict: false},
		{name: "legacy pin", pin: ProtocolVersionLegacy, strict: false},
		{name: "modern pin", pin: ProtocolVersion20260728, strict: true},
	}
	fixtures := []struct {
		name  string
		value string // "" configures WithFakeToolResultType("") — an omitted field
		want  string
	}{
		{name: "absent", value: "", want: ResultTypeComplete},
		{name: "complete", value: "complete", want: "complete"},
		{name: "task", value: "task", want: "task"},
		{name: "input_required", value: "input_required", want: "input_required"},
	}

	// fixtureInputRequests/fixtureRequestState fixture a well-formed
	// input_required payload for the "input_required" row above: since #792,
	// CallTool decodes that resultType (inputrequired.go) instead of treating
	// it as opaque data, so this row now needs a payload that actually
	// decodes -- decode-failure coverage (absent requestState, malformed
	// inputRequests, the size caps) lives in inputrequired_test.go.
	fixtureInputRequests := []map[string]any{
		{"message": "delete the production database?", "requestedSchema": map[string]any{"type": "object"}},
	}
	const fixtureRequestState = "opaque-state-token"

	for _, era := range eras {
		t.Run(era.name, func(t *testing.T) {
			for _, fixture := range fixtures {
				t.Run(fixture.name, func(t *testing.T) {
					opts := []FakeServerOption{WithFakeMode(FakeModern), WithFakeToolResultType(fixture.value)}
					if era.strict {
						opts = append(opts, WithFakeRejectLegacyHandshake())
					}
					if fixture.value == ResultTypeInputRequired {
						opts = append(opts, WithFakeInputRequired(fixtureInputRequests, fixtureRequestState))
					}
					fake := NewFakeMCPServer(opts...)
					srv := httptest.NewServer(fake)
					t.Cleanup(srv.Close)

					c := NewClient(srv.URL, WithProtocolVersion(era.pin))
					res, err := c.CallTool(context.Background(), "tool-a", nil, CallOptions{})
					if err != nil {
						t.Fatalf("CallTool: %v (a non-complete resultType is data, never an error, and this fixture's input_required payload is well-formed)", err)
					}
					if res.ResultType != fixture.want {
						t.Errorf("ResultType = %q, want %q", res.ResultType, fixture.want)
					}
					if res.IsError {
						t.Errorf("IsError = true, want false")
					}
					if !strings.Contains(string(res.Output), "called tool-a") {
						t.Errorf("Output = %s, want it to contain %q", res.Output, "called tool-a")
					}
					if fixture.value == ResultTypeInputRequired {
						if res.InputRequired == nil {
							t.Fatal("InputRequired = nil, want a decoded InputRequiredResult")
						}
						if len(res.InputRequired.InputRequests) != 1 {
							t.Fatalf("len(InputRequired.InputRequests) = %d, want 1", len(res.InputRequired.InputRequests))
						}
						if got := res.InputRequired.InputRequests[0].Message; got != fixtureInputRequests[0]["message"] {
							t.Errorf("InputRequests[0].Message = %q, want %q", got, fixtureInputRequests[0]["message"])
						}
						if got := string(res.InputRequired.RequestState); got != `"`+fixtureRequestState+`"` {
							t.Errorf("RequestState = %s, want %q", got, fixtureRequestState)
						}
					} else if res.InputRequired != nil {
						t.Errorf("InputRequired = %+v, want nil for resultType %q", res.InputRequired, fixture.want)
					}
					if v := fake.Violations(); len(v) != 0 {
						t.Errorf("Violations = %v, want none", v)
					}

					callReqs := fake.RequestsFor(methodToolsCall)
					if len(callReqs) != 1 {
						t.Fatalf("len(RequestsFor(tools/call)) = %d, want 1", len(callReqs))
					}
					req := callReqs[0]
					if era.strict {
						if req.ProtocolHeader != ProtocolVersion20260728 {
							t.Errorf("ProtocolHeader = %q, want %q", req.ProtocolHeader, ProtocolVersion20260728)
						}
						if req.MethodHeader != methodToolsCall {
							t.Errorf("MethodHeader = %q, want %q", req.MethodHeader, methodToolsCall)
						}
						if req.NameHeader != "tool-a" {
							t.Errorf("NameHeader = %q, want %q", req.NameHeader, "tool-a")
						}
						if req.SessionHeader != "" {
							t.Errorf("SessionHeader = %q, want empty", req.SessionHeader)
						}
						return
					}
					if req.SessionHeader != "fake-session" {
						t.Errorf("SessionHeader = %q, want %q", req.SessionHeader, "fake-session")
					}
					if req.ProtocolHeader != "" || req.MethodHeader != "" || req.NameHeader != "" {
						t.Errorf("legacy tools/call carries modern headers: protocol=%q method=%q name=%q",
							req.ProtocolHeader, req.MethodHeader, req.NameHeader)
					}
					if meta := decodeMeta(req.Params); meta != nil {
						t.Errorf("params carry a _meta key, want none on the legacy path: %s", req.Params)
					}
				})
			}
		})
	}
}

// TestNoRequestEverDeclaresSampling is the "any fixture" DoD assertion: sampling
// must never appear on the wire, on any transport, under any capability grant.
// It drives three call paths against three fakes — (a) an unpinned client
// through ProbeProtocolVersion's legacy fallback (initialize +
// notifications/initialized) followed by tools/list and a tool call, (b) a
// client explicitly pinned to the legacy constant, and (c) a modern-pinned
// client against a strict-modern fake, granting Elicitation — the one
// capability this repo can ever declare — to prove that granting the
// capability that DOES exist still never produces the one that must not
// exist. It then inspects every fake's recorded Requests() for the literal
// substring "sampling"; Params is sufficient coverage because "sampling"
// could only ever appear inside a capabilities object, which lives in params
// on both the legacy initialize body and the modern _meta.
func TestNoRequestEverDeclaresSampling(t *testing.T) {
	var fakes []*FakeMCPServer

	// (a) unpinned client: ProbeProtocolVersion's legacy fallback, then
	// tools/list, then a tool call.
	legacyFake := NewFakeMCPServer(WithFakeMode(FakeLegacy))
	legacySrv := httptest.NewServer(legacyFake)
	t.Cleanup(legacySrv.Close)
	fakes = append(fakes, legacyFake)

	unpinned := NewClient(legacySrv.URL)
	if _, err := unpinned.ProbeProtocolVersion(context.Background()); err != nil {
		t.Fatalf("ProbeProtocolVersion (unpinned): %v", err)
	}
	if _, err := unpinned.DiscoverTools(context.Background()); err != nil {
		t.Fatalf("DiscoverTools (unpinned): %v", err)
	}
	if _, err := unpinned.CallTool(context.Background(), "tool-a", nil, CallOptions{}); err != nil {
		t.Fatalf("CallTool (unpinned): %v", err)
	}

	// (b) a client explicitly pinned to the legacy constant.
	legacyPinnedFake := NewFakeMCPServer(WithFakeMode(FakeLegacy))
	legacyPinnedSrv := httptest.NewServer(legacyPinnedFake)
	t.Cleanup(legacyPinnedSrv.Close)
	fakes = append(fakes, legacyPinnedFake)

	legacyPinned := NewClient(legacyPinnedSrv.URL, WithProtocolVersion(ProtocolVersionLegacy))
	if _, err := legacyPinned.CallTool(context.Background(), "tool-a", nil, CallOptions{}); err != nil {
		t.Fatalf("CallTool (legacy pin): %v", err)
	}

	// (c) a modern-pinned client against a strict-modern fake, with
	// Elicitation granted.
	modernFake := NewFakeMCPServer(WithFakeMode(FakeModern), WithFakeRejectLegacyHandshake())
	modernSrv := httptest.NewServer(modernFake)
	t.Cleanup(modernSrv.Close)
	fakes = append(fakes, modernFake)

	modern := NewClient(modernSrv.URL, WithProtocolVersion(ProtocolVersion20260728))
	if _, err := modern.DiscoverTools(context.Background()); err != nil {
		t.Fatalf("DiscoverTools (modern): %v", err)
	}
	if _, err := modern.CallTool(context.Background(), "tool-a", nil, CallOptions{Capabilities: ClientCapabilities{Elicitation: true}}); err != nil {
		t.Fatalf("CallTool (modern): %v", err)
	}

	for i, fake := range fakes {
		for _, req := range fake.Requests() {
			if strings.Contains(string(req.Params), "sampling") {
				t.Errorf("fake[%d] %s Params contains \"sampling\": %s", i, req.Method, req.Params)
			}
		}
		if v := fake.Violations(); len(v) != 0 {
			t.Errorf("fake[%d] Violations = %v, want none", i, v)
		}
	}
}

// TestBilingualRegistry_LegacyAndModernServersInOneInstance is the
// milestone-DoD capstone: a legacy fake and a 2026-07-28 fake both register,
// discover tools, and serve a tools/call through the same Registry, proving
// the two eras coexist without one server's shaping leaking into the
// other's requests.
func TestBilingualRegistry_LegacyAndModernServersInOneInstance(t *testing.T) {
	reg, store := newTestRegistry(t)
	ctx := context.Background()

	legacyFake := NewFakeMCPServer(
		WithFakeMode(FakeLegacy),
		WithFakeTools(Tool{Name: "legacy-tool", Description: "d", InputSchema: []byte(`{"type":"object"}`)}),
	)
	legacySrv := httptest.NewServer(legacyFake)
	t.Cleanup(legacySrv.Close)

	modernFake := NewFakeMCPServer(
		WithFakeMode(FakeModern),
		WithFakeRejectLegacyHandshake(),
		WithFakeTools(Tool{Name: "modern-tool", Description: "d", InputSchema: []byte(`{"type":"object"}`)}),
	)
	modernSrv := httptest.NewServer(modernFake)
	t.Cleanup(modernSrv.Close)

	legacyServerID, err := RegisterServerForTest(ctx, store.Queries(), reg, "legacy-server", legacySrv.URL)
	if err != nil {
		t.Fatalf("register legacy server: %v", err)
	}
	modernServerID, err := RegisterServerForTest(ctx, store.Queries(), reg, "modern-server", modernSrv.URL)
	if err != nil {
		t.Fatalf("register modern server: %v", err)
	}

	legacyRow, err := store.Queries().GetMCPServer(ctx, legacyServerID)
	if err != nil {
		t.Fatalf("GetMCPServer(legacy): %v", err)
	}
	if legacyRow.ProtocolVersion == nil || *legacyRow.ProtocolVersion != ProtocolVersionLegacy {
		t.Errorf("legacy protocol_version = %v, want %q", legacyRow.ProtocolVersion, ProtocolVersionLegacy)
	}

	modernRow, err := store.Queries().GetMCPServer(ctx, modernServerID)
	if err != nil {
		t.Fatalf("GetMCPServer(modern): %v", err)
	}
	if modernRow.ProtocolVersion == nil || *modernRow.ProtocolVersion != ProtocolVersion20260728 {
		t.Errorf("modern protocol_version = %v, want %q", modernRow.ProtocolVersion, ProtocolVersion20260728)
	}

	legacyTools, err := store.Queries().ListMCPToolsByServer(ctx, legacyServerID)
	if err != nil {
		t.Fatalf("ListMCPToolsByServer(legacy): %v", err)
	}
	if len(legacyTools) != 1 || legacyTools[0].Name != "legacy-tool" {
		t.Errorf("legacy server tools = %v, want [legacy-tool]", legacyTools)
	}

	modernTools, err := store.Queries().ListMCPToolsByServer(ctx, modernServerID)
	if err != nil {
		t.Fatalf("ListMCPToolsByServer(modern): %v", err)
	}
	if len(modernTools) != 1 || modernTools[0].Name != "modern-tool" {
		t.Errorf("modern server tools = %v, want [modern-tool]", modernTools)
	}

	legacyClient, legacyToolName, err := reg.ResolveToolByName(ctx, "legacy-server.legacy-tool")
	if err != nil {
		t.Fatalf("ResolveToolByName(legacy): %v", err)
	}
	legacyResult, err := legacyClient.CallTool(ctx, legacyToolName, nil, CallOptions{})
	if err != nil {
		t.Fatalf("CallTool(legacy): %v", err)
	}
	if legacyResult.IsError {
		t.Errorf("legacy CallTool IsError = true, want false")
	}
	if !strings.Contains(string(legacyResult.Output), "legacy-tool") {
		t.Errorf("legacy CallTool Output = %s, want it to contain %q", legacyResult.Output, "legacy-tool")
	}
	if legacyResult.ResultType != ResultTypeComplete {
		t.Errorf("legacy CallTool ResultType = %q, want %q", legacyResult.ResultType, ResultTypeComplete)
	}

	modernClient, modernToolName, err := reg.ResolveToolByName(ctx, "modern-server.modern-tool")
	if err != nil {
		t.Fatalf("ResolveToolByName(modern): %v", err)
	}
	modernResult, err := modernClient.CallTool(ctx, modernToolName, nil, CallOptions{})
	if err != nil {
		t.Fatalf("CallTool(modern): %v", err)
	}
	if modernResult.IsError {
		t.Errorf("modern CallTool IsError = true, want false")
	}
	if !strings.Contains(string(modernResult.Output), "modern-tool") {
		t.Errorf("modern CallTool Output = %s, want it to contain %q", modernResult.Output, "modern-tool")
	}
	if modernResult.ResultType != ResultTypeComplete {
		t.Errorf("modern CallTool ResultType = %q, want %q", modernResult.ResultType, ResultTypeComplete)
	}

	// The legacy fake's protocol probe (which falls back to the legacy
	// initialize handshake) plus RefreshTools's own tools/list, plus the
	// CallTool above, add up to at least one initialize; every tools/list
	// and tools/call it received must carry the session header and none of
	// the modern transport headers.
	if got := len(legacyFake.RequestsFor("initialize")); got < 1 {
		t.Errorf("legacy fake saw %d initialize requests, want >= 1", got)
	}
	for _, method := range []string{methodToolsList, methodToolsCall} {
		for _, req := range legacyFake.RequestsFor(method) {
			if req.SessionHeader != "fake-session" {
				t.Errorf("legacy %s SessionHeader = %q, want %q", method, req.SessionHeader, "fake-session")
			}
			if req.ProtocolHeader != "" || req.MethodHeader != "" || req.NameHeader != "" {
				t.Errorf("legacy %s carries modern headers: protocol=%q method=%q name=%q",
					method, req.ProtocolHeader, req.MethodHeader, req.NameHeader)
			}
		}
	}
	if v := legacyFake.Violations(); len(v) != 0 {
		t.Errorf("legacy fake Violations = %v, want none", v)
	}

	// The modern fake never received an initialize at all — the probe's
	// server/discover classified it modern, and every subsequent request
	// used the stateless transport instead of the session handshake.
	if got := len(modernFake.RequestsFor("initialize")); got != 0 {
		t.Errorf("modern fake saw %d initialize requests, want 0", got)
	}
	modernListReqs := modernFake.RequestsFor(methodToolsList)
	if len(modernListReqs) == 0 {
		t.Fatal("modern fake saw no tools/list requests")
	}
	for _, req := range modernListReqs {
		if req.ProtocolHeader != ProtocolVersion20260728 || req.MethodHeader != methodToolsList {
			t.Errorf("modern tools/list headers = protocol=%q method=%q, want protocol=%q method=%q",
				req.ProtocolHeader, req.MethodHeader, ProtocolVersion20260728, methodToolsList)
		}
		if req.SessionHeader != "" {
			t.Errorf("modern tools/list SessionHeader = %q, want empty", req.SessionHeader)
		}
	}
	modernCallReqs := modernFake.RequestsFor(methodToolsCall)
	if len(modernCallReqs) != 1 {
		t.Fatalf("len(modern RequestsFor(tools/call)) = %d, want 1", len(modernCallReqs))
	}
	callReq := modernCallReqs[0]
	if callReq.ProtocolHeader != ProtocolVersion20260728 || callReq.MethodHeader != methodToolsCall || callReq.NameHeader != "modern-tool" {
		t.Errorf("modern tools/call headers = protocol=%q method=%q name=%q, want protocol=%q method=%q name=%q",
			callReq.ProtocolHeader, callReq.MethodHeader, callReq.NameHeader,
			ProtocolVersion20260728, methodToolsCall, "modern-tool")
	}
	if callReq.SessionHeader != "" {
		t.Errorf("modern tools/call SessionHeader = %q, want empty", callReq.SessionHeader)
	}
	if v := modernFake.Violations(); len(v) != 0 {
		t.Errorf("modern fake Violations = %v, want none", v)
	}
}
