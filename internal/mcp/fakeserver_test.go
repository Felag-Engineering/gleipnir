package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// discoverBody builds a minimal, valid server/discover JSON-RPC request
// body. Callers mutate the returned map before marshaling to drive specific
// violations (e.g. deleting a _meta key).
func discoverBody(protocolVersion string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "server/discover",
		"params": map[string]any{
			"_meta": map[string]any{
				MetaKeyProtocolVersion:    protocolVersion,
				MetaKeyClientCapabilities: map[string]any{},
			},
		},
	}
}

// postDiscover sends a hand-rolled server/discover POST to srv with exactly
// the given headers, so each test row controls precisely which headers are
// present. Returns the decoded envelope and HTTP status.
func postDiscover(t *testing.T, srv *httptest.Server, body map[string]any, headers map[string]string) (int, jsonrpcResponse) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST server/discover: %v", err)
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.StatusCode, envelope
}

func TestFakeMCPServer_DiscoverModeShapes(t *testing.T) {
	t.Run("FakeModern returns 200 with configured supportedVersions", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeModern), WithFakeSupportedVersions("2026-07-28", "2025-11-25"))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		status, envelope := postDiscover(t, srv, discoverBody("2026-07-28"), map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "server/discover",
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		var result discoverResult
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if len(result.SupportedVersions) != 2 || result.SupportedVersions[0] != "2026-07-28" {
			t.Errorf("SupportedVersions = %v, want [2026-07-28 2025-11-25]", result.SupportedVersions)
		}
	})

	t.Run("WithFakeServerInfo empty/empty omits the discover result's _meta entirely", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeModern), WithFakeServerInfo("", ""))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		status, envelope := postDiscover(t, srv, discoverBody("2026-07-28"), map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "server/discover",
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		var rawResult map[string]json.RawMessage
		if err := json.Unmarshal(envelope.Result, &rawResult); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if _, ok := rawResult["_meta"]; ok {
			t.Errorf("result carries a _meta key, want it omitted entirely: %s", envelope.Result)
		}
	})

	t.Run("FakeVersionMismatch returns 400 with -32022 and data.supported", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeVersionMismatch), WithFakeSupportedVersions("2025-11-25"))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		status, envelope := postDiscover(t, srv, discoverBody("2026-07-28"), map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "server/discover",
		})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
		if envelope.Error == nil {
			t.Fatal("expected error envelope, got nil")
		}
		if envelope.Error.Code != -32022 {
			t.Errorf("Code = %d, want -32022", envelope.Error.Code)
		}
		var data unsupportedVersionData
		if err := json.Unmarshal(envelope.Error.Data, &data); err != nil {
			t.Fatalf("unmarshal data: %v", err)
		}
		if len(data.Supported) != 1 || data.Supported[0] != "2025-11-25" {
			t.Errorf("data.supported = %v, want [2025-11-25]", data.Supported)
		}
	})

	t.Run("FakeLegacy returns 404 with a plain-text body, no JSON-RPC envelope", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeLegacy))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		raw, _ := json.Marshal(discoverBody("2026-07-28"))
		resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
		body := make([]byte, 512)
		n, _ := resp.Body.Read(body)
		if string(body[:n]) != "404 page not found" {
			t.Errorf("body = %q, want %q", string(body[:n]), "404 page not found")
		}
	})

	t.Run("WithFakeDiscoverStatus overrides mode with an empty body", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeModern), WithFakeDiscoverStatus(http.StatusTooManyRequests))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		raw, _ := json.Marshal(discoverBody("2026-07-28"))
		resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", resp.StatusCode)
		}
		body := make([]byte, 1)
		n, _ := resp.Body.Read(body)
		if n != 0 {
			t.Errorf("expected empty body, read %d bytes", n)
		}
	})
}

func TestFakeMCPServer_A4HeaderEnforcement(t *testing.T) {
	t.Run("mismatched MCP-Protocol-Version", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeModern))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		status, envelope := postDiscover(t, srv, discoverBody("2026-07-28"), map[string]string{
			"MCP-Protocol-Version": "1900-01-01", // valid Mcp-Method, valid _meta
			"Mcp-Method":           "server/discover",
		})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
		if envelope.Error == nil || envelope.Error.Code != -32020 {
			t.Fatalf("error = %+v, want code -32020", envelope.Error)
		}
		if v := fake.Violations(); len(v) != 1 {
			t.Errorf("Violations = %v, want exactly one", v)
		}
	})

	t.Run("missing Mcp-Method", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeModern))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		status, envelope := postDiscover(t, srv, discoverBody("2026-07-28"), map[string]string{
			"MCP-Protocol-Version": "2026-07-28", // valid header, valid _meta
			// Mcp-Method deliberately omitted.
		})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
		// Per streamable-http.md §Server Validation, whose rejection table
		// has exactly one code and lists a missing required standard header
		// as a failure condition.
		if envelope.Error == nil || envelope.Error.Code != -32020 {
			t.Fatalf("error = %+v, want code -32020", envelope.Error)
		}
		if v := fake.Violations(); len(v) != 1 {
			t.Errorf("Violations = %v, want exactly one", v)
		}
	})

	t.Run("Mcp-Method present but does not match body method", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeModern))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		status, envelope := postDiscover(t, srv, discoverBody("2026-07-28"), map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "tools/list", // valid everything else
		})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
		if envelope.Error == nil || envelope.Error.Code != -32020 {
			t.Fatalf("error = %+v, want code -32020", envelope.Error)
		}
		if v := fake.Violations(); len(v) != 1 {
			t.Errorf("Violations = %v, want exactly one", v)
		}
	})

	t.Run("both standard headers valid but _meta.clientCapabilities missing", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeModern))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		body := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "server/discover",
			"params": map[string]any{
				"_meta": map[string]any{
					MetaKeyProtocolVersion: "2026-07-28",
					// clientCapabilities deliberately omitted.
				},
			},
		}
		// The row MUST send valid headers, otherwise step-1 header
		// validation short-circuits it into -32020; that ordering is
		// itself the assertion.
		status, envelope := postDiscover(t, srv, body, map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "server/discover",
		})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
		if envelope.Error == nil || envelope.Error.Code != -32602 {
			t.Fatalf("error = %+v, want code -32602", envelope.Error)
		}
		if v := fake.Violations(); len(v) != 1 {
			t.Errorf("Violations = %v, want exactly one", v)
		}
	})

	t.Run("both standard headers valid but _meta.protocolVersion missing", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeModern))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		body := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "server/discover",
			"params": map[string]any{
				"_meta": map[string]any{
					MetaKeyClientCapabilities: map[string]any{},
					// protocolVersion deliberately omitted; the header
					// cannot then be compared against a body value, so the
					// fake treats this as the body-regime failure.
				},
			},
		}
		status, envelope := postDiscover(t, srv, body, map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "server/discover",
		})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
		if envelope.Error == nil || envelope.Error.Code != -32602 {
			t.Fatalf("error = %+v, want code -32602", envelope.Error)
		}
		if v := fake.Violations(); len(v) != 1 {
			t.Errorf("Violations = %v, want exactly one", v)
		}
	})

	t.Run("Mcp-Name sent on server/discover is recorded but does not reject", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeModern))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		status, envelope := postDiscover(t, srv, discoverBody("2026-07-28"), map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "server/discover",
			"Mcp-Name":             "unexpected",
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if envelope.Error != nil {
			t.Errorf("error = %+v, want nil", envelope.Error)
		}
		if v := fake.Violations(); len(v) != 1 {
			t.Errorf("Violations = %v, want exactly one", v)
		}
	})

	t.Run("FakeLegacy mode with no headers at all has no violations", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeLegacy))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		raw, _ := json.Marshal(discoverBody("2026-07-28"))
		resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()

		if v := fake.Violations(); len(v) != 0 {
			t.Errorf("Violations = %v, want none (enforcement is off for legacy)", v)
		}
	})
}

// toolMeta builds a minimal, valid _meta object for tool traffic, mirroring
// discoverBody's inner _meta shape.
func toolMeta(protocolVersion string) map[string]any {
	return map[string]any{
		MetaKeyProtocolVersion:    protocolVersion,
		MetaKeyClientCapabilities: map[string]any{},
	}
}

// postRPC sends a hand-rolled JSON-RPC POST to srv with exactly the given
// headers, mirroring postDiscover for methods other than server/discover.
func postRPC(t *testing.T, srv *httptest.Server, body map[string]any, headers map[string]string) (int, jsonrpcResponse) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var envelope jsonrpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.StatusCode, envelope
}

// TestFakeMCPServer_StrictModern exercises WithFakeRejectLegacyHandshake:
// the legacy handshake methods stop existing, and tool traffic must carry
// the standard transport headers just like server/discover already does.
func TestFakeMCPServer_StrictModern(t *testing.T) {
	newStrictFake := func(t *testing.T) (*FakeMCPServer, *httptest.Server) {
		t.Helper()
		fake := NewFakeMCPServer(
			WithFakeMode(FakeModern),
			WithFakeRejectLegacyHandshake(),
			WithFakeTools(Tool{Name: "tool-a", Description: "tool-a description", InputSchema: json.RawMessage(`{"type":"object"}`)}),
		)
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		return fake, srv
	}

	t.Run("initialize is rejected with -32601, it does not exist in this revision", func(t *testing.T) {
		_, srv := newStrictFake(t)

		status, envelope := postRPC(t, srv, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{},
		}, nil)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}
		if envelope.Error == nil || envelope.Error.Code != -32601 {
			t.Fatalf("error = %+v, want code -32601", envelope.Error)
		}
	})

	t.Run("tools/list with no transport headers is rejected", func(t *testing.T) {
		fake, srv := newStrictFake(t)

		status, envelope := postRPC(t, srv, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
		}, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
		if envelope.Error == nil || envelope.Error.Code != -32020 {
			t.Fatalf("error = %+v, want code -32020", envelope.Error)
		}
		if v := fake.Violations(); len(v) != 1 {
			t.Errorf("Violations = %v, want exactly one", v)
		}
	})

	t.Run("tools/list with valid headers returns the configured tools", func(t *testing.T) {
		_, srv := newStrictFake(t)

		status, envelope := postRPC(t, srv, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{"_meta": toolMeta("2026-07-28")},
		}, map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "tools/list",
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		var result toolsListResult
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if len(result.Tools) != 1 || result.Tools[0].Name != "tool-a" {
			t.Errorf("Tools = %v, want [tool-a]", result.Tools)
		}
	})

	t.Run("strict-modern tools/list whose _meta.protocolVersion disagrees with the header is -32020", func(t *testing.T) {
		fake, srv := newStrictFake(t)

		status, envelope := postRPC(t, srv, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{"_meta": toolMeta("1900-01-01")},
		}, map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "tools/list",
		})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
		if envelope.Error == nil || envelope.Error.Code != -32020 {
			t.Fatalf("error = %+v, want code -32020", envelope.Error)
		}
		if v := fake.Violations(); len(v) != 1 {
			t.Errorf("Violations = %v, want exactly one", v)
		}
	})

	t.Run("tools/call with Mcp-Name mismatching params.name is rejected", func(t *testing.T) {
		fake, srv := newStrictFake(t)

		status, envelope := postRPC(t, srv, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "tool-a", "arguments": map[string]any{}},
		}, map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "tools/call",
			"Mcp-Name":             "wrong-name",
		})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
		if envelope.Error == nil || envelope.Error.Code != -32020 {
			t.Fatalf("error = %+v, want code -32020", envelope.Error)
		}
		if v := fake.Violations(); len(v) != 1 {
			t.Errorf("Violations = %v, want exactly one", v)
		}
	})

	t.Run("tools/call with matching headers echoes the tool name", func(t *testing.T) {
		_, srv := newStrictFake(t)

		status, envelope := postRPC(t, srv, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "tool-a", "arguments": map[string]any{}, "_meta": toolMeta("2026-07-28")},
		}, map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "tools/call",
			"Mcp-Name":             "tool-a",
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		var result toolsCallResult
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if result.IsError {
			t.Errorf("IsError = true, want false")
		}
		if len(result.Content) != 1 || result.Content[0].Text != "called tool-a" {
			t.Errorf("Content = %v, want text \"called tool-a\"", result.Content)
		}
	})

	t.Run("strict-modern tools/call with valid headers but no _meta is rejected with -32602", func(t *testing.T) {
		fake, srv := newStrictFake(t)

		status, envelope := postRPC(t, srv, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "tool-a", "arguments": map[string]any{}},
		}, map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "tools/call",
			"Mcp-Name":             "tool-a",
		})
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
		if envelope.Error == nil || envelope.Error.Code != -32602 {
			t.Fatalf("error = %+v, want code -32602", envelope.Error)
		}
		if v := fake.Violations(); len(v) != 1 {
			t.Errorf("Violations = %v, want exactly one", v)
		}
	})

	t.Run("tools/call with no params.name is accepted because expectedName is empty", func(t *testing.T) {
		fake, srv := newStrictFake(t)

		status, envelope := postRPC(t, srv, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"arguments": map[string]any{}, "_meta": toolMeta("2026-07-28")},
		}, map[string]string{
			"MCP-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "tools/call",
		})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if envelope.Error != nil {
			t.Errorf("error = %+v, want nil", envelope.Error)
		}
		if v := fake.Violations(); len(v) != 0 {
			t.Errorf("Violations = %v, want none (a decoded-empty params.name is treated as \"names no entity\")", v)
		}
	})

	t.Run("default (non-strict) fake still serves initialize and a header-less tools/list", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeMode(FakeModern))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		status, envelope := postRPC(t, srv, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{},
		}, nil)
		if status != http.StatusOK {
			t.Fatalf("initialize status = %d, want 200", status)
		}
		if envelope.Error != nil {
			t.Errorf("initialize error = %+v, want nil", envelope.Error)
		}

		status, envelope = postRPC(t, srv, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
		}, nil)
		if status != http.StatusOK {
			t.Fatalf("tools/list status = %d, want 200", status)
		}
		var result toolsListResult
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if len(result.Tools) == 0 {
			t.Error("Tools is empty, want the default configured tool")
		}
		if v := fake.Violations(); len(v) != 0 {
			t.Errorf("Violations = %v, want none (back-compat: enforcement is off by default)", v)
		}
	})
}

// TestFakeMCPServer_RequestsRaceClean drives two concurrent posts and then
// reads Requests(), so -race can prove the recorder is safe for concurrent
// handler goroutines.
func TestFakeMCPServer_RequestsRaceClean(t *testing.T) {
	fake := NewFakeMCPServer(WithFakeMode(FakeModern))
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	raw, err := json.Marshal(discoverBody("2026-07-28"))
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	// t.Fatalf is not safe to call from a non-test goroutine, so the
	// concurrent requests are fired with plain net/http rather than reusing
	// postDiscover; only the main goroutine makes assertions.
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(raw))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("MCP-Protocol-Version", "2026-07-28")
			req.Header.Set("Mcp-Method", "server/discover")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()

	if got := fake.Requests(); len(got) != 2 {
		t.Errorf("len(Requests()) = %d, want 2", len(got))
	}
}

// TestFakeMCPServer_ToolResultTypeFixture pins the fake's resultType
// fixture behavior: WithFakeToolResultType's default is real field omission,
// not an empty string, and a configured value is echoed verbatim.
func TestFakeMCPServer_ToolResultTypeFixture(t *testing.T) {
	toolsCallBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "tool-a",
		},
	}

	t.Run("default fake omits resultType entirely", func(t *testing.T) {
		fake := NewFakeMCPServer()
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		status, envelope := postRPC(t, srv, toolsCallBody, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}

		// Unmarshal into raw fields, not a struct decode: a struct decode
		// would report "" for both an absent key and an empty string, which
		// is the exact distinction this test exists to catch. If the fake
		// emitted "resultType":"", every client-side "absent ⇒ complete"
		// assertion would still pass while testing nothing.
		var result map[string]json.RawMessage
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if _, present := result["resultType"]; present {
			t.Errorf("result has a resultType key, want it entirely absent: %s", envelope.Result)
		}
	})

	t.Run("WithFakeToolResultType is echoed verbatim", func(t *testing.T) {
		fake := NewFakeMCPServer(WithFakeToolResultType("task"))
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)

		status, envelope := postRPC(t, srv, toolsCallBody, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}

		var result toolsCallResult
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if got := decodeResultType(result.ResultType); got != "task" {
			t.Errorf("ResultType = %q, want %q", got, "task")
		}
	})
}
