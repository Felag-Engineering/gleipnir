package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// headerCapture records the full clone of every request's http.Header, in
// order, for assertions -- the same "full clone" discipline FakeRequest.Header
// uses.
type headerCapture struct {
	mu      sync.Mutex
	headers []http.Header
}

func (c *headerCapture) record(h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.headers = append(c.headers, h.Clone())
}

func (c *headerCapture) all() []http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]http.Header, len(c.headers))
	copy(out, c.headers)
	return out
}

// capturingToolCallServer starts a modern-shaped (single-POST, stateless)
// server that records every request's headers and answers every call with a
// successful tools/call result.
func capturingToolCallServer(t *testing.T) (*httptest.Server, *headerCapture) {
	t.Helper()
	capture := &headerCapture{}
	srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
		capture.record(r.Header)
		successToolCallHandler(w, r)
	})
	return srv, capture
}

// relayNames is the AttributionHeaderNames a Relay-preset config resolves
// to.
var relayNames = AttributionHeaderNames{
	OnBehalfOf:  RelayOnBehalfOfHeader,
	SessionRef:  RelaySessionRefHeader,
	Traceparent: RelayTraceparentHeader,
}

// TestCallTool_RunAttribution_RelayPresetHeaders proves the Relay preset
// carries all three documented header values on the modern transport, and
// that the legacy initialize handshake -- which has no run to attribute --
// carries none of them even though the same Client is configured with names.
func TestCallTool_RunAttribution_RelayPresetHeaders(t *testing.T) {
	attribution := RunAttribution{
		AgentName:   "fleet-responder",
		RunID:       "01J000RUN",
		TriggeredBy: "alice",
		PublicURL:   "https://g.example/",
	}

	t.Run("modern transport", func(t *testing.T) {
		srv, capture := capturingToolCallServer(t)
		c := NewClient(srv.URL, WithProtocolVersion(ProtocolVersion20260728), WithRunAttributionHeaders(relayNames))

		if _, err := c.CallTool(context.Background(), "tool-x", nil, CallOptions{Attribution: attribution}); err != nil {
			t.Fatalf("CallTool: %v", err)
		}

		got := capture.all()
		if len(got) != 1 {
			t.Fatalf("captured %d requests, want 1", len(got))
		}
		assertAttributionHeaders(t, got[0], attribution.RunID)
	})

	t.Run("legacy transport: initialize carries none, tools/call carries all three", func(t *testing.T) {
		capture := &methodHeaderCapture{}
		srv := makeServer(t, legacyCapturingHandler(capture, "tool-x"))

		c := NewClient(srv.URL, WithProtocolVersion(ProtocolVersionLegacy), WithRunAttributionHeaders(relayNames))
		if _, err := c.CallTool(context.Background(), "tool-x", nil, CallOptions{Attribution: attribution}); err != nil {
			t.Fatalf("CallTool: %v", err)
		}

		for _, h := range capture.forMethod("initialize") {
			for _, name := range []string{RelayOnBehalfOfHeader, RelaySessionRefHeader, RelayTraceparentHeader} {
				if h.Get(name) != "" {
					t.Errorf("initialize request carries %s = %q, want absent (no run context yet)", name, h.Get(name))
				}
			}
		}

		callHeaders := capture.forMethod(methodToolsCall)
		if len(callHeaders) != 1 {
			t.Fatalf("captured %d tools/call requests, want 1", len(callHeaders))
		}
		assertAttributionHeaders(t, callHeaders[0], attribution.RunID)
	})
}

// assertAttributionHeaders asserts h carries the documented Relay preset
// values for the "fleet-responder (triggered by alice)" /
// "gleipnir run <runID> (https://g.example/runs/<runID>)" fixture used
// throughout this file.
func assertAttributionHeaders(t *testing.T, h http.Header, runID string) {
	t.Helper()
	if got, want := h.Get(RelayOnBehalfOfHeader), "fleet-responder (triggered by alice)"; got != want {
		t.Errorf("%s = %q, want %q", RelayOnBehalfOfHeader, got, want)
	}
	if got, want := h.Get(RelaySessionRefHeader), "gleipnir run "+runID+" (https://g.example/runs/"+runID+")"; got != want {
		t.Errorf("%s = %q, want %q", RelaySessionRefHeader, got, want)
	}
	tp := h.Get(RelayTraceparentHeader)
	if !traceparentPattern.MatchString(tp) {
		t.Errorf("%s = %q, does not match %s", RelayTraceparentHeader, tp, traceparentPattern)
	}
	if wantTraceID := traceIDFor(runID); tp[3:35] != wantTraceID {
		t.Errorf("%s trace-id = %q, want %q", RelayTraceparentHeader, tp[3:35], wantTraceID)
	}
}

// methodHeaderCapture records request headers keyed by JSON-RPC method, so a
// test can distinguish the legacy handshake's headers from tools/call's.
type methodHeaderCapture struct {
	mu      sync.Mutex
	headers map[string][]http.Header
}

func (c *methodHeaderCapture) record(method string, h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.headers == nil {
		c.headers = make(map[string][]http.Header)
	}
	c.headers[method] = append(c.headers[method], h.Clone())
}

func (c *methodHeaderCapture) forMethod(method string) []http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]http.Header, len(c.headers[method]))
	copy(out, c.headers[method])
	return out
}

// legacyCapturingHandler serves the legacy handshake (initialize,
// notifications/initialized) plus a single tool named toolName, recording
// every request's headers by method into capture.
func legacyCapturingHandler(capture *methodHeaderCapture, toolName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		method, _ := req["method"].(string)
		capture.record(method, r.Header)
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
			successToolCallHandler(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// TestCallTool_RunAttribution_OffSendsNothing covers both halves of "off":
// a Client with no configured names sends nothing regardless of what
// Attribution the caller supplies, and a Client WITH configured names sends
// nothing when the caller supplies the zero Attribution (the poll shape).
func TestCallTool_RunAttribution_OffSendsNothing(t *testing.T) {
	attribution := RunAttribution{AgentName: "fleet-responder", RunID: "r1"}

	t.Run("no configured names", func(t *testing.T) {
		srv, capture := capturingToolCallServer(t)
		c := NewClient(srv.URL, WithProtocolVersion(ProtocolVersion20260728))

		if _, err := c.CallTool(context.Background(), "tool-x", nil, CallOptions{Attribution: attribution}); err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		assertNoAttributionHeaders(t, capture.all()[0])
	})

	t.Run("configured names, zero attribution (poll shape)", func(t *testing.T) {
		srv, capture := capturingToolCallServer(t)
		c := NewClient(srv.URL, WithProtocolVersion(ProtocolVersion20260728), WithRunAttributionHeaders(relayNames))

		if _, err := c.CallTool(context.Background(), "tool-x", nil, CallOptions{}); err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		assertNoAttributionHeaders(t, capture.all()[0])
	})
}

func assertNoAttributionHeaders(t *testing.T, h http.Header) {
	t.Helper()
	for _, name := range []string{RelayOnBehalfOfHeader, RelaySessionRefHeader, RelayTraceparentHeader} {
		if v := h.Get(name); v != "" {
			t.Errorf("%s = %q, want absent", name, v)
		}
	}
}

// TestCallTool_RunAttribution_CustomNames proves custom names are sent
// under exactly those names, and a field left empty is omitted from the
// wire entirely.
func TestCallTool_RunAttribution_CustomNames(t *testing.T) {
	srv, capture := capturingToolCallServer(t)
	names := AttributionHeaderNames{OnBehalfOf: "X-Actor"} // SessionRef and Traceparent left empty on purpose
	c := NewClient(srv.URL, WithProtocolVersion(ProtocolVersion20260728), WithRunAttributionHeaders(names))

	attribution := RunAttribution{AgentName: "fleet-reader", RunID: "r1"}
	if _, err := c.CallTool(context.Background(), "tool-x", nil, CallOptions{Attribution: attribution}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	h := capture.all()[0]
	if got, want := h.Get("X-Actor"), "fleet-reader"; got != want {
		t.Errorf("X-Actor = %q, want %q", got, want)
	}
	for _, name := range []string{RelaySessionRefHeader, RelayTraceparentHeader} {
		if v := h.Get(name); v != "" {
			t.Errorf("%s = %q, want absent (not configured)", name, v)
		}
	}
}

// TestCallTool_RunAttribution_AuthWinsAndXMcpHeaderRefused covers D4: run
// attribution never overrides an operator's ADR-039 auth header, in both
// directions.
func TestCallTool_RunAttribution_AuthWinsAndXMcpHeaderRefused(t *testing.T) {
	t.Run("a stored custom attribution name colliding with an auth header: the auth value wins, attribution is dropped", func(t *testing.T) {
		key := mustTestKey(t)
		reg, _ := newTestRegistryWithKey(t, key)

		srv, capture := capturingToolCallServer(t)

		authHeaders := []AuthHeader{{Name: "X-Custom-Actor", Value: "operator-secret"}}
		encrypted := mustEncryptHeaders(t, key, authHeaders)
		// Hand-edited: this row could never be written through the API (the
		// write path's ValidateRunAttribution would reject the collision),
		// but a row can still reach this shape via direct DB access or a
		// TOCTOU with a later SetAuthHeader. newClientForServer must drop it.
		attributionColumn := `{"mode":"custom","on_behalf_of_header":"X-Custom-Actor"}`
		row := db.McpServer{
			ID:                   "srv-collide",
			Name:                 "collide-server",
			Url:                  srv.URL,
			AuthHeadersEncrypted: encrypted,
			RunAttribution:       &attributionColumn,
		}

		c := reg.newClientForServer(row)
		c.protocolVersion = ProtocolVersion20260728 // force modern shaping for this single-POST fake

		if _, err := c.CallTool(context.Background(), "tool-x", nil, CallOptions{Attribution: RunAttribution{AgentName: "agent", RunID: "r1"}}); err != nil {
			t.Fatalf("CallTool: %v", err)
		}

		h := capture.all()[0]
		if got, want := h.Get("X-Custom-Actor"), "operator-secret"; got != want {
			t.Errorf("X-Custom-Actor = %q, want the auth header's value %q (auth must win)", got, want)
		}
	})

	t.Run("an x-mcp-header annotation naming a configured attribution header is refused before dispatch", func(t *testing.T) {
		var requestCount int
		srv := makeServer(t, func(w http.ResponseWriter, r *http.Request) {
			requestCount++
			successToolCallHandler(w, r)
		})
		c := NewClient(srv.URL, WithProtocolVersion(ProtocolVersion20260728), WithRunAttributionHeaders(relayNames))
		schema := json.RawMessage(`{"properties":{"on_behalf_of":{"type":"string","x-mcp-header":"X-Relay-On-Behalf-Of"}}}`)

		_, err := c.CallTool(context.Background(), "tool-x", map[string]any{"on_behalf_of": "root"}, CallOptions{
			HeaderParamSchema: schema,
			Attribution:       RunAttribution{AgentName: "agent", RunID: "r1"},
		})
		if err == nil {
			t.Fatal("CallTool: want an error, got nil")
		}
		var hpErr *HeaderParamError
		if !errors.As(err, &hpErr) {
			t.Fatalf("CallTool error is not a *HeaderParamError: %v", err)
		}
		if requestCount != 0 {
			t.Errorf("server received %d requests, want 0 (rejection must be pre-dispatch)", requestCount)
		}
	})
}

// TestCallTool_RunAttribution_HostileArgumentCannotInfluence is the
// structural proof of D7: a tool call whose input tries to smuggle
// attribution-shaped values (and a CRLF header-injection attempt) produces
// attribution headers identical to an otherwise-identical benign call --
// byte-identical for on-behalf-of and session-ref, and sharing the same
// trace-id for traceparent (the span-id legitimately differs per call; see
// buildAttributionHeaders). The schema carries no x-mcp-header annotations
// at all, so nothing in input has any path to a header.
func TestCallTool_RunAttribution_HostileArgumentCannotInfluence(t *testing.T) {
	attribution := RunAttribution{AgentName: "fleet-responder", RunID: "r1"}

	runWith := func(t *testing.T, input map[string]any) http.Header {
		t.Helper()
		srv, capture := capturingToolCallServer(t)
		c := NewClient(srv.URL, WithProtocolVersion(ProtocolVersion20260728), WithRunAttributionHeaders(relayNames))
		if _, err := c.CallTool(context.Background(), "tool-x", input, CallOptions{Attribution: attribution}); err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		return capture.all()[0]
	}

	benign := runWith(t, map[string]any{"env": "prod"})
	hostile := runWith(t, map[string]any{
		"on_behalf_of":         "root",
		"X-Relay-On-Behalf-Of": "root",
		"traceparent":          "00-ffffffffffffffffffffffffffffffff-ffffffffffffffff-01",
		"session_ref":          "evil\r\nX-Injected: 1",
		"_meta":                map[string]any{"evil": true},
	})

	for _, name := range []string{RelayOnBehalfOfHeader, RelaySessionRefHeader} {
		if benign.Get(name) != hostile.Get(name) {
			t.Errorf("%s differs between benign and hostile input: benign=%q hostile=%q", name, benign.Get(name), hostile.Get(name))
		}
	}
	benignTP, hostileTP := benign.Get(RelayTraceparentHeader), hostile.Get(RelayTraceparentHeader)
	if !traceparentPattern.MatchString(benignTP) || !traceparentPattern.MatchString(hostileTP) {
		t.Fatalf("traceparent does not match %s: benign=%q hostile=%q", traceparentPattern, benignTP, hostileTP)
	}
	if benignTP[3:35] != hostileTP[3:35] {
		t.Errorf("traceparent trace-id differs between benign and hostile input: benign=%q hostile=%q", benignTP[3:35], hostileTP[3:35])
	}
	if hostile.Get("X-Injected") != "" {
		t.Errorf("X-Injected = %q, want absent", hostile.Get("X-Injected"))
	}
}

// TestCallTool_RunAttribution_MRTRRetryCarriesHeadersWithFreshSpan proves
// that an initial call and its MRTR retry both carry the full set of
// attribution headers, sharing the run's trace-id but drawing a fresh
// span-id per HTTP request (each round is a separate CallTool).
func TestCallTool_RunAttribution_MRTRRetryCarriesHeadersWithFreshSpan(t *testing.T) {
	fake := NewFakeMCPServer(WithFakeMode(FakeModern), WithFakeRejectLegacyHandshake())
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, WithProtocolVersion(ProtocolVersion20260728), WithRunAttributionHeaders(relayNames))
	attribution := RunAttribution{AgentName: "fleet-responder", RunID: "r1"}

	if _, err := c.CallTool(context.Background(), "tool-a", nil, CallOptions{Attribution: attribution}); err != nil {
		t.Fatalf("initial CallTool: %v", err)
	}
	retryOpts := CallOptions{
		Attribution:    attribution,
		InputResponses: []InputResponse{{ID: "q1", Action: "accept", Content: json.RawMessage(`{"confirmed":true}`)}},
		RequestState:   json.RawMessage(`"opaque-state-token"`),
	}
	if _, err := c.CallTool(context.Background(), "tool-a", nil, retryOpts); err != nil {
		t.Fatalf("retry CallTool: %v", err)
	}

	calls := fake.RequestsFor(methodToolsCall)
	if len(calls) != 2 {
		t.Fatalf("len(RequestsFor(tools/call)) = %d, want 2", len(calls))
	}

	wantTraceID := traceIDFor(attribution.RunID)
	spans := make(map[string]bool, 2)
	for i, call := range calls {
		for _, name := range []string{RelayOnBehalfOfHeader, RelaySessionRefHeader} {
			if call.Header.Get(name) == "" {
				t.Errorf("call %d: %s is empty, want a value", i, name)
			}
		}
		tp := call.Header.Get(RelayTraceparentHeader)
		if !traceparentPattern.MatchString(tp) {
			t.Errorf("call %d: traceparent %q does not match %s", i, tp, traceparentPattern)
			continue
		}
		if gotTraceID := tp[3:35]; gotTraceID != wantTraceID {
			t.Errorf("call %d: trace-id = %q, want %q", i, gotTraceID, wantTraceID)
		}
		spans[tp[36:52]] = true
	}
	if len(spans) != 2 {
		t.Errorf("saw %d distinct span-ids across the two calls, want 2 (fresh span per round)", len(spans))
	}
}

// TestNewClientForServer_RunAttribution covers the registry-level wiring:
// an unparseable stored value sends nothing (fail closed), and a relay row
// gives the client the preset names.
func TestNewClientForServer_RunAttribution(t *testing.T) {
	reg, _ := newTestRegistry(t)

	t.Run("unparseable stored value sends nothing and logs a warning", func(t *testing.T) {
		buf := captureLogger(t)
		garbage := "{not json"
		row := db.McpServer{ID: "srv-1", Name: "srv-1", Url: "http://example.invalid", RunAttribution: &garbage}

		c := reg.newClientForServer(row)
		if !c.attributionNames.IsZero() {
			t.Errorf("attributionNames = %+v, want zero for an unparseable stored value", c.attributionNames)
		}

		found := false
		for _, line := range decodeLogLines(t, buf) {
			if msg, _ := line["msg"].(string); msg == "stored run attribution does not parse; sending none" {
				found = true
			}
		}
		if !found {
			t.Error("expected a warning log line for the unparseable run_attribution value")
		}
	})

	t.Run("relay row gives the preset names", func(t *testing.T) {
		relay := `{"mode":"relay"}`
		row := db.McpServer{ID: "srv-2", Name: "srv-2", Url: "http://example.invalid", RunAttribution: &relay}

		c := reg.newClientForServer(row)
		if c.attributionNames != relayNames {
			t.Errorf("attributionNames = %+v, want the relay preset %+v", c.attributionNames, relayNames)
		}
	})
}
