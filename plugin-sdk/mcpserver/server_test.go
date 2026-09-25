package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeExtension is a minimal Extension for tests: whatever ExtensionID,
// Declaration, and Methods the test wants, in one struct.
type fakeExtension struct {
	id      string
	decl    any
	methods map[string]MethodFunc
}

func (f fakeExtension) ExtensionID() string            { return f.id }
func (f fakeExtension) Declaration() any               { return f.decl }
func (f fakeExtension) Methods() map[string]MethodFunc { return f.methods }

// discoverBody builds a well-formed server/discover request body. Tests
// mutate copies of it to produce each violation.
func discoverBody(version string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "server/discover",
		"params": map[string]any{
			"_meta": map[string]any{
				MetaKeyProtocolVersion:    version,
				MetaKeyClientCapabilities: map[string]any{},
			},
		},
	}
}

func modernHeaders(method string) map[string]string {
	return map[string]string{
		"MCP-Protocol-Version": ProtocolVersion,
		"Mcp-Method":           method,
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

func TestDiscover_ExtensionMerge(t *testing.T) {
	tests := []struct {
		name       string
		extensions []fakeExtension
		wantKeys   []string
	}{
		{name: "zero extensions"},
		{
			name: "one extension",
			extensions: []fakeExtension{
				{id: "io.gleipnir/events", decl: map[string]any{"version": "1.0.0"}},
			},
			wantKeys: []string{"io.gleipnir/events"},
		},
		{
			name: "two extensions",
			extensions: []fakeExtension{
				{id: "io.gleipnir/events", decl: map[string]any{"version": "1.0.0"}},
				{id: "io.gleipnir/channel", decl: map[string]any{"assurance": "authenticated"}},
			},
			wantKeys: []string{"io.gleipnir/events", "io.gleipnir/channel"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer("test-plugin", "0.1.0")
			for _, ext := range tc.extensions {
				if err := srv.Mount(ext); err != nil {
					t.Fatalf("Mount(%s): %v", ext.id, err)
				}
			}

			w := postJSON(t, srv, discoverBody(ProtocolVersion), modernHeaders("server/discover"))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
			}
			env := decodeEnvelope(t, w)
			if env.Error != nil {
				t.Fatalf("unexpected error: %+v", env.Error)
			}

			caps, _ := env.Result["capabilities"].(map[string]any)
			extensions, _ := caps["extensions"].(map[string]any)

			if len(tc.wantKeys) == 0 {
				if _, ok := caps["extensions"]; ok {
					t.Errorf("capabilities.extensions = %v, want absent with no mounted extensions", extensions)
				}
				return
			}
			if len(extensions) != len(tc.wantKeys) {
				t.Fatalf("capabilities.extensions = %v, want exactly %v", extensions, tc.wantKeys)
			}
			for _, key := range tc.wantKeys {
				if _, ok := extensions[key]; !ok {
					t.Errorf("capabilities.extensions missing %q: %v", key, extensions)
				}
			}

			// No tools were registered in this test, so the tools capability
			// must not be claimed.
			if _, ok := caps["tools"]; ok {
				t.Errorf("capabilities.tools present with no registered tools: %v", caps)
			}
		})
	}
}

func TestDiscover_DeclaresToolsCapabilityOnlyWhenToolsExist(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")

	w := postJSON(t, srv, discoverBody(ProtocolVersion), modernHeaders("server/discover"))
	env := decodeEnvelope(t, w)
	caps, _ := env.Result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; ok {
		t.Fatalf("capabilities.tools present before any tool is registered: %v", caps)
	}

	if err := srv.RegisterTool(Tool{
		Name:    "say_hello",
		Handler: func(context.Context, json.RawMessage) (any, error) { return "hi", nil },
	}); err != nil {
		t.Fatalf("RegisterTool: %v", err)
	}

	w = postJSON(t, srv, discoverBody(ProtocolVersion), modernHeaders("server/discover"))
	env = decodeEnvelope(t, w)
	caps, _ = env.Result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("capabilities.tools missing after registering a tool: %v", caps)
	}
}

// TestDiscover_ToolsAndTwoExtensionsTogether is the DoD line made literal:
// one Server serving tools plus two extensions behind one server/discover.
func TestDiscover_ToolsAndTwoExtensionsTogether(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	if err := srv.RegisterTool(Tool{
		Name:    "say_hello",
		Handler: func(context.Context, json.RawMessage) (any, error) { return "hi", nil },
	}); err != nil {
		t.Fatalf("RegisterTool: %v", err)
	}
	if err := srv.Mount(fakeExtension{id: "io.gleipnir/events", decl: map[string]any{"version": "1.0.0"}}); err != nil {
		t.Fatalf("Mount io.gleipnir/events: %v", err)
	}
	if err := srv.Mount(fakeExtension{id: "io.gleipnir/channel", decl: map[string]any{"assurance": "authenticated"}}); err != nil {
		t.Fatalf("Mount io.gleipnir/channel: %v", err)
	}

	w := postJSON(t, srv, discoverBody(ProtocolVersion), modernHeaders("server/discover"))
	env := decodeEnvelope(t, w)
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	caps, _ := env.Result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("capabilities.tools missing: %v", caps)
	}
	extensions, _ := caps["extensions"].(map[string]any)
	if len(extensions) != 2 {
		t.Fatalf("capabilities.extensions = %v, want both io.gleipnir/events and io.gleipnir/channel", extensions)
	}
	for _, id := range []string{"io.gleipnir/events", "io.gleipnir/channel"} {
		if _, ok := extensions[id]; !ok {
			t.Errorf("capabilities.extensions missing %q: %v", id, extensions)
		}
	}
}

func TestMount_DuplicateExtensionIDRejected(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	ext := fakeExtension{id: "io.gleipnir/events", decl: map[string]any{}}
	if err := srv.Mount(ext); err != nil {
		t.Fatalf("first Mount: %v", err)
	}
	if err := srv.Mount(ext); err == nil {
		t.Fatal("second Mount with the same ExtensionID succeeded, want an error")
	}
}

func TestMount_DuplicateMethodRejected(t *testing.T) {
	tests := []struct {
		name string
		seed func(t *testing.T, srv *Server)
	}{
		{
			name: "collides with another mounted extension's method",
			seed: func(t *testing.T, srv *Server) {
				t.Helper()
				noop := func(w http.ResponseWriter, r *http.Request, req Request) { WriteResult(w, req.ID, nil) }
				if err := srv.Mount(fakeExtension{id: "ext.one", decl: map[string]any{}, methods: map[string]MethodFunc{"thing/do": noop}}); err != nil {
					t.Fatalf("Mount ext.one: %v", err)
				}
			},
		},
		{
			name: "collides with a reserved method",
			seed: func(t *testing.T, srv *Server) {},
		},
		{
			name: "collides with a streaming mount",
			seed: func(t *testing.T, srv *Server) {
				t.Helper()
				if err := srv.MountStreaming("thing/do", http.NotFoundHandler()); err != nil {
					t.Fatalf("MountStreaming: %v", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer("test-plugin", "0.1.0")
			tc.seed(t, srv)

			method := "thing/do"
			if tc.name == "collides with a reserved method" {
				method = "tools/list"
			}
			noop := func(w http.ResponseWriter, r *http.Request, req Request) { WriteResult(w, req.ID, nil) }
			err := srv.Mount(fakeExtension{id: "ext.two", decl: map[string]any{}, methods: map[string]MethodFunc{method: noop}})
			if err == nil {
				t.Fatalf("Mount with colliding method %q succeeded, want an error", method)
			}
		})
	}
}

func TestMountStreaming_CollisionRejected(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	if err := srv.MountStreaming("events/listen", http.NotFoundHandler()); err != nil {
		t.Fatalf("first MountStreaming: %v", err)
	}
	if err := srv.MountStreaming("events/listen", http.NotFoundHandler()); err == nil {
		t.Fatal("second MountStreaming for the same method succeeded, want an error")
	}
	if err := srv.MountStreaming("tools/list", http.NotFoundHandler()); err == nil {
		t.Fatal("MountStreaming over a reserved method succeeded, want an error")
	}
}

// TestMountStreaming_HandlerReceivesBodyByteForByte proves the replay
// ServeHTTP does after peeking the method: a mounted streaming handler must
// see the exact bytes the client sent, including request fields (here,
// "cursor") the top-level dispatcher never parses.
func TestMountStreaming_HandlerReceivesBodyByteForByte(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")

	var captured []byte
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("streaming handler: read body: %v", err)
		}
		captured = b
		w.WriteHeader(http.StatusOK)
	})
	if err := srv.MountStreaming("events/listen", handler); err != nil {
		t.Fatalf("MountStreaming: %v", err)
	}

	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "events/listen",
		"params": map[string]any{
			"kinds":  []string{"thing.created"},
			"cursor": "42",
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
	for k, v := range modernHeaders("events/listen") {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !bytes.Equal(captured, raw) {
		t.Fatalf("streaming handler received %s, want the original request body %s byte for byte", captured, raw)
	}
}

func TestServeHTTP_OversizeRequestBodyRefused(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0", WithMaxRequestBytes(16))

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if len(body) <= 16 {
		t.Fatalf("test fixture body is not larger than the 16-byte cap: %d bytes", len(body))
	}

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	for k, v := range modernHeaders("tools/list") {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	env := decodeEnvelope(t, w)
	if env.Error == nil || env.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("error = %+v, want ErrCodeInvalidParams for an oversize body", env.Error)
	}
}

func TestServeHTTP_DefaultMaxRequestBytesAllowsAnOrdinaryRequest(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0") // default cap, no WithMaxRequestBytes
	w := postJSON(t, srv, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	}, modernHeaders("tools/list"))
	env := decodeEnvelope(t, w)
	if env.Error != nil {
		t.Fatalf("unexpected error under the default request-size cap: %+v", env.Error)
	}
}

func toolsCallBody(name string, args map[string]any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
}

func toolsCallHeaders(name string) map[string]string {
	h := modernHeaders("tools/call")
	h["Mcp-Name"] = name
	return h
}

func TestToolsList_SortedByName(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	handler := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	for _, name := range []string{"zebra", "apple", "mango"} {
		if err := srv.RegisterTool(Tool{Name: name, Handler: handler}); err != nil {
			t.Fatalf("RegisterTool(%s): %v", name, err)
		}
	}

	w := postJSON(t, srv, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	}, modernHeaders("tools/list"))
	env := decodeEnvelope(t, w)
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	toolsRaw, _ := env.Result["tools"].([]any)
	if len(toolsRaw) != 3 {
		t.Fatalf("tools/list returned %d tools, want 3: %v", len(toolsRaw), toolsRaw)
	}
	var got []string
	for _, tr := range toolsRaw {
		tm := tr.(map[string]any)
		got = append(got, tm["name"].(string))
	}
	want := []string{"apple", "mango", "zebra"}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("tools/list order = %v, want %v", got, want)
		}
	}
}

func TestToolsCall_UnknownTool(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	w := postJSON(t, srv, toolsCallBody("does_not_exist", nil), toolsCallHeaders("does_not_exist"))
	env := decodeEnvelope(t, w)
	if env.Error == nil || env.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("expected ErrCodeInvalidParams for an unknown tool, got %+v", env.Error)
	}
}

func TestToolsCall_HandlerError(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	wantErr := "boom"
	if err := srv.RegisterTool(Tool{
		Name:    "fails",
		Handler: func(context.Context, json.RawMessage) (any, error) { return nil, errors.New(wantErr) },
	}); err != nil {
		t.Fatalf("RegisterTool: %v", err)
	}

	w := postJSON(t, srv, toolsCallBody("fails", nil), toolsCallHeaders("fails"))
	env := decodeEnvelope(t, w)
	if env.Error != nil {
		t.Fatalf("a handler error must be an isError result, not a JSON-RPC error: %+v", env.Error)
	}
	if isErr, _ := env.Result["isError"].(bool); !isErr {
		t.Fatalf("result.isError = %v, want true", env.Result["isError"])
	}
	content, _ := env.Result["content"].([]any)
	if len(content) != 1 || !strings.Contains(content[0].(map[string]any)["text"].(string), wantErr) {
		t.Fatalf("result.content = %v, want text containing %q", content, wantErr)
	}
}

func TestToolsCall_HandlerPanicRecoveredAsError(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	if err := srv.RegisterTool(Tool{
		Name: "panics",
		Handler: func(context.Context, json.RawMessage) (any, error) {
			panic("something went wrong")
		},
	}); err != nil {
		t.Fatalf("RegisterTool: %v", err)
	}

	w := postJSON(t, srv, toolsCallBody("panics", nil), toolsCallHeaders("panics"))
	env := decodeEnvelope(t, w)
	if env.Error != nil {
		t.Fatalf("a recovered panic must be an isError result, not a JSON-RPC error: %+v", env.Error)
	}
	if isErr, _ := env.Result["isError"].(bool); !isErr {
		t.Fatalf("result.isError = %v, want true", env.Result["isError"])
	}
}

func TestToolsCall_Success(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	if err := srv.RegisterTool(Tool{
		Name: "echo",
		Handler: func(_ context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, err
			}
			return "hello " + in.Name, nil
		},
	}); err != nil {
		t.Fatalf("RegisterTool: %v", err)
	}

	w := postJSON(t, srv, toolsCallBody("echo", map[string]any{"name": "world"}), toolsCallHeaders("echo"))
	env := decodeEnvelope(t, w)
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	if isErr, _ := env.Result["isError"].(bool); isErr {
		t.Fatalf("result.isError = true, want false: %v", env.Result)
	}
}

func TestServeHTTP_LegacyInitializeRefused(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}
	w := postJSON(t, srv, body, nil)
	env := decodeEnvelope(t, w)
	if env.Error == nil || env.Error.Code != ErrCodeMethodNotFound {
		t.Fatalf("expected code %d for legacy initialize, got %+v", ErrCodeMethodNotFound, env.Error)
	}
}

func TestDiscover_BadProtocolHeaderRefused(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(body map[string]any, headers map[string]string)
		wantCode int
	}{
		{
			name: "missing MCP-Protocol-Version header",
			mutate: func(_ map[string]any, h map[string]string) {
				delete(h, "MCP-Protocol-Version")
			},
			wantCode: ErrCodeHeaderMismatch,
		},
		{
			name: "header version does not match body version",
			mutate: func(b map[string]any, _ map[string]string) {
				meta := b["params"].(map[string]any)["_meta"].(map[string]any)
				meta[MetaKeyProtocolVersion] = "2025-11-25"
			},
			wantCode: ErrCodeHeaderMismatch,
		},
		{
			name: "header and body agree on an unsupported version",
			mutate: func(b map[string]any, h map[string]string) {
				meta := b["params"].(map[string]any)["_meta"].(map[string]any)
				meta[MetaKeyProtocolVersion] = "2025-11-25"
				h["MCP-Protocol-Version"] = "2025-11-25"
			},
			wantCode: ErrCodeUnsupportedProtocolVersion,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer("test-plugin", "0.1.0")
			body := discoverBody(ProtocolVersion)
			headers := modernHeaders("server/discover")
			tc.mutate(body, headers)

			w := postJSON(t, srv, body, headers)
			env := decodeEnvelope(t, w)
			if env.Error == nil || env.Error.Code != tc.wantCode {
				t.Fatalf("error = %+v, want code %d", env.Error, tc.wantCode)
			}
		})
	}
}

func TestServeHTTP_GetIsNotMCPTraffic(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestServeHTTP_AnswersOnEveryPath(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	for _, path := range []string{"/", "/anything", "/mcp/v1"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
		for k, v := range modernHeaders("tools/list") {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		env := decodeEnvelope(t, w)
		if env.Error != nil {
			t.Errorf("path %q: unexpected error: %+v", path, env.Error)
		}
	}
}

func TestExtensionMethodDispatch(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	called := false
	ext := fakeExtension{
		id:   "ext.custom",
		decl: map[string]any{},
		methods: map[string]MethodFunc{
			"custom/do": func(w http.ResponseWriter, r *http.Request, req Request) {
				called = true
				WriteResult(w, req.ID, map[string]any{"ok": true})
			},
		},
	}
	if err := srv.Mount(ext); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	w := postJSON(t, srv, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "custom/do", "params": map[string]any{},
	}, modernHeaders("custom/do"))
	env := decodeEnvelope(t, w)
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	if !called {
		t.Fatal("mounted MethodFunc was never invoked")
	}
	if ok, _ := env.Result["ok"].(bool); !ok {
		t.Fatalf("result = %v, want ok:true", env.Result)
	}
}

func TestExtensionMethodDispatch_HeaderMismatchRejectedBeforeHandler(t *testing.T) {
	srv := NewServer("test-plugin", "0.1.0")
	called := false
	ext := fakeExtension{
		id:   "ext.custom",
		decl: map[string]any{},
		methods: map[string]MethodFunc{
			"custom/do": func(w http.ResponseWriter, r *http.Request, req Request) {
				called = true
				WriteResult(w, req.ID, nil)
			},
		},
	}
	if err := srv.Mount(ext); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	headers := modernHeaders("custom/do")
	delete(headers, "Mcp-Method")
	w := postJSON(t, srv, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "custom/do", "params": map[string]any{},
	}, headers)
	env := decodeEnvelope(t, w)
	if env.Error == nil || env.Error.Code != ErrCodeHeaderMismatch {
		t.Fatalf("expected ErrCodeHeaderMismatch, got %+v", env.Error)
	}
	if called {
		t.Fatal("MethodFunc was invoked despite a missing Mcp-Method header")
	}
}
