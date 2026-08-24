package hostclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
)

// fakeRequest is one JSON-RPC request the fake host endpoint received,
// captured for header/body assertions.
type fakeRequest struct {
	Method  string
	Headers http.Header
	Params  map[string]json.RawMessage
}

// fakeHostEndpoint is a hand-rolled httptest server standing in for
// internal/plugin/hostendpoint.Server. This module cannot import internal/*
// (it is a separate Go module — see plugin-sdk/go.mod), so the wire contract
// is reproduced here from the ground-truth handlers
// (internal/plugin/hostendpoint/{server,toolcall}.go) rather than reused.
type fakeHostEndpoint struct {
	t        *testing.T
	requests []fakeRequest

	// wantToken is checked on every request when non-empty.
	wantToken string

	// toolResults maps a tool name to the raw result payload (already
	// JSON-encoded) to return on tools/call, isError=false.
	toolResults map[string]string
	// toolErrors maps a tool name to the "code: message" isError text to
	// return instead of a result.
	toolErrors map[string]string
}

func newFakeHostEndpoint(t *testing.T) *fakeHostEndpoint {
	return &fakeHostEndpoint{
		t:           t,
		toolResults: map[string]string{},
		toolErrors:  map[string]string{},
	}
}

func (f *fakeHostEndpoint) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Fatalf("fake host endpoint: decode request: %v", err)
		}

		if f.wantToken != "" {
			if got := r.Header.Get("Authorization"); got != "Bearer "+f.wantToken {
				f.writeError(w, req.ID, http.StatusUnauthorized, -32000, "unknown or revoked instance token")
				return
			}
		}
		if r.Header.Get("MCP-Protocol-Version") == "" {
			f.writeError(w, req.ID, http.StatusBadRequest, -32020, "Header mismatch: MCP-Protocol-Version header is missing")
			return
		}
		methodHeader := r.Header.Get("Mcp-Method")
		if methodHeader == "" {
			f.writeError(w, req.ID, http.StatusBadRequest, -32020, "Header mismatch: Mcp-Method header is missing")
			return
		}
		if methodHeader != req.Method {
			f.writeError(w, req.ID, http.StatusBadRequest, -32020, "Header mismatch: Mcp-Method header value does not match body method")
			return
		}

		var params map[string]json.RawMessage
		_ = json.Unmarshal(req.Params, &params)
		f.requests = append(f.requests, fakeRequest{Method: req.Method, Headers: r.Header.Clone(), Params: params})

		switch req.Method {
		case "server/discover":
			f.handleDiscover(w, req.ID)
		case "tools/call":
			f.handleToolsCall(w, req.ID, params)
		default:
			f.writeError(w, req.ID, http.StatusNotFound, -32601, "method not found: "+req.Method)
		}
	}
}

func (f *fakeHostEndpoint) handleDiscover(w http.ResponseWriter, id json.RawMessage) {
	f.writeResult(w, id, map[string]any{
		"resultType":        "complete",
		"supportedVersions": []string{"2026-07-28"},
		"capabilities":      map[string]any{"tools": map[string]any{}},
		"_meta": map[string]any{
			"io.modelcontextprotocol/serverInfo": map[string]any{
				"name":    "gleipnir-host-endpoint",
				"version": "0.1.0",
			},
		},
	})
}

func (f *fakeHostEndpoint) handleToolsCall(w http.ResponseWriter, id json.RawMessage, params map[string]json.RawMessage) {
	var name string
	_ = json.Unmarshal(params["name"], &name)

	if errText, ok := f.toolErrors[name]; ok {
		f.writeResult(w, id, map[string]any{
			"content": []map[string]any{{"type": "text", "text": errText}},
			"isError": true,
		})
		return
	}
	resultJSON, ok := f.toolResults[name]
	if !ok {
		f.writeError(w, id, http.StatusBadRequest, -32602, "unknown tool: "+name)
		return
	}
	f.writeResult(w, id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": resultJSON}},
		"isError": false,
	})
}

func (f *fakeHostEndpoint) writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (f *fakeHostEndpoint) writeError(w http.ResponseWriter, id json.RawMessage, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}

// lastRequest returns the most recently captured request, failing the test
// if none was captured.
func (f *fakeHostEndpoint) lastRequest(t *testing.T) fakeRequest {
	t.Helper()
	if len(f.requests) == 0 {
		t.Fatal("fake host endpoint: no request captured")
	}
	return f.requests[len(f.requests)-1]
}

func newTestClient(t *testing.T, f *fakeHostEndpoint, token string) *hostclient.Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	c, err := hostclient.New(hostclient.WithBaseURL(srv.URL), hostclient.WithToken(token))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNew_EnvVarConstruction(t *testing.T) {
	t.Run("reads base URL and token from the environment by default", func(t *testing.T) {
		t.Setenv(hostclient.HostEndpointURLEnvVar, "http://example.invalid")
		t.Setenv(hostclient.InstanceTokenEnvVar, "env-token")
		c, err := hostclient.New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c == nil {
			t.Fatal("New returned a nil client with no error")
		}
	})

	t.Run("missing base URL fails clearly", func(t *testing.T) {
		t.Setenv(hostclient.HostEndpointURLEnvVar, "")
		t.Setenv(hostclient.InstanceTokenEnvVar, "env-token")
		_, err := hostclient.New()
		if err == nil || !strings.Contains(err.Error(), hostclient.HostEndpointURLEnvVar) {
			t.Fatalf("err = %v, want mention of %s", err, hostclient.HostEndpointURLEnvVar)
		}
	})

	t.Run("missing token fails clearly", func(t *testing.T) {
		t.Setenv(hostclient.HostEndpointURLEnvVar, "http://example.invalid")
		t.Setenv(hostclient.InstanceTokenEnvVar, "")
		_, err := hostclient.New()
		if err == nil || !strings.Contains(err.Error(), hostclient.InstanceTokenEnvVar) {
			t.Fatalf("err = %v, want mention of %s", err, hostclient.InstanceTokenEnvVar)
		}
	})

	t.Run("options override the environment", func(t *testing.T) {
		t.Setenv(hostclient.HostEndpointURLEnvVar, "")
		t.Setenv(hostclient.InstanceTokenEnvVar, "")
		_, err := hostclient.New(hostclient.WithBaseURL("http://example.invalid"), hostclient.WithToken("t"))
		if err != nil {
			t.Fatalf("New with options: %v", err)
		}
	})
}

func TestDiscover(t *testing.T) {
	f := newFakeHostEndpoint(t)
	c := newTestClient(t, f, "tok")

	result, err := c.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(result.SupportedVersions) != 1 || result.SupportedVersions[0] != "2026-07-28" {
		t.Errorf("SupportedVersions = %v", result.SupportedVersions)
	}
	if result.ServerName != "gleipnir-host-endpoint" || result.ServerVersion != "0.1.0" {
		t.Errorf("serverInfo = %s/%s", result.ServerName, result.ServerVersion)
	}

	req := f.lastRequest(t)
	if req.Method != "server/discover" {
		t.Errorf("method = %s", req.Method)
	}
	assertRequestHeaders(t, req, "server/discover", "")
	assertTokenHeader(t, req, "tok")

	var meta map[string]json.RawMessage
	if err := json.Unmarshal(req.Params["_meta"], &meta); err != nil {
		t.Fatalf("decode _meta: %v", err)
	}
	var version string
	if err := json.Unmarshal(meta["io.modelcontextprotocol/protocolVersion"], &version); err != nil || version != "2026-07-28" {
		t.Errorf("_meta protocolVersion = %q, err %v", version, err)
	}
	if _, ok := meta["io.modelcontextprotocol/clientCapabilities"]; !ok {
		t.Error("_meta is missing clientCapabilities")
	}
}

// assertRequestHeaders checks the headers every request must carry per spec
// §8: Authorization is checked separately (assertTokenHeader) since its
// value varies by test.
func assertRequestHeaders(t *testing.T, req fakeRequest, wantMethod, wantName string) {
	t.Helper()
	if got := req.Headers.Get("MCP-Protocol-Version"); got != "2026-07-28" {
		t.Errorf("MCP-Protocol-Version = %q, want 2026-07-28", got)
	}
	if got := req.Headers.Get("Mcp-Method"); got != wantMethod {
		t.Errorf("Mcp-Method = %q, want %q", got, wantMethod)
	}
	if wantName != "" {
		if got := req.Headers.Get("Mcp-Name"); got != wantName {
			t.Errorf("Mcp-Name = %q, want %q", got, wantName)
		}
	} else if got := req.Headers.Get("Mcp-Name"); got != "" {
		t.Errorf("Mcp-Name = %q, want unset for %s", got, wantMethod)
	}
}

func assertTokenHeader(t *testing.T, req fakeRequest, wantToken string) {
	t.Helper()
	if got := req.Headers.Get("Authorization"); got != "Bearer "+wantToken {
		t.Errorf("Authorization = %q, want %q", got, "Bearer "+wantToken)
	}
}

func TestTier1_HappyPath(t *testing.T) {
	f := newFakeHostEndpoint(t)
	f.toolResults["host/get_instance_config"] = `{"config_json":"{\"channel\":\"#ops\"}"}`
	f.toolResults["host/get_credentials"] = `{"credentials_json":"secret"}`
	f.toolResults["host/get_run_context"] = `{"run_id":"run-1","policy_id":"pol-1","started_at":"2026-08-21T10:00:00Z","step_index":5}`
	f.toolResults["host/emit_metric"] = `{"ok":true}`
	f.toolResults["host/log"] = `{"ok":true}`
	f.toolResults["host/set_health_state"] = `{"ok":true,"applied":true}`
	c := newTestClient(t, f, "tok")
	ctx := context.Background()

	t.Run("GetInstanceConfig", func(t *testing.T) {
		out, err := c.GetInstanceConfig(ctx)
		if err != nil {
			t.Fatalf("GetInstanceConfig: %v", err)
		}
		if out.ConfigJSON != `{"channel":"#ops"}` {
			t.Errorf("ConfigJSON = %q", out.ConfigJSON)
		}
		assertRequestHeaders(t, f.lastRequest(t), "tools/call", "host/get_instance_config")
	})

	t.Run("GetCredentials", func(t *testing.T) {
		out, err := c.GetCredentials(ctx)
		if err != nil {
			t.Fatalf("GetCredentials: %v", err)
		}
		if out.CredentialsJSON != "secret" {
			t.Errorf("CredentialsJSON = %q", out.CredentialsJSON)
		}
	})

	t.Run("GetRunContext attaches the call id header from WithCallID", func(t *testing.T) {
		out, err := c.GetRunContext(hostclient.WithCallID(ctx, "call-1"))
		if err != nil {
			t.Fatalf("GetRunContext: %v", err)
		}
		if out.RunID != "run-1" || out.PolicyID != "pol-1" || out.StepIndex != 5 {
			t.Errorf("result = %+v", out)
		}
		req := f.lastRequest(t)
		if got := req.Headers.Get(hostclient.CallIDHeader); got != "call-1" {
			t.Errorf("Gleipnir-Call-Id = %q, want call-1", got)
		}
	})

	t.Run("GetRunContext omits the call id header when none is set", func(t *testing.T) {
		if _, err := c.GetRunContext(ctx); err != nil {
			t.Fatalf("GetRunContext: %v", err)
		}
		req := f.lastRequest(t)
		if got := req.Headers.Get(hostclient.CallIDHeader); got != "" {
			t.Errorf("Gleipnir-Call-Id = %q, want unset", got)
		}
	})

	t.Run("EmitMetric sends name/value/labels", func(t *testing.T) {
		out, err := c.EmitMetric(ctx, hostclient.EmitMetricRequest{Name: "probe", Value: 1.5, Labels: map[string]string{"queue": "a"}})
		if err != nil {
			t.Fatalf("EmitMetric: %v", err)
		}
		if !out.OK {
			t.Error("OK = false")
		}
		req := f.lastRequest(t)
		var args struct {
			Name   string            `json:"name"`
			Value  float64           `json:"value"`
			Labels map[string]string `json:"labels"`
		}
		if err := json.Unmarshal(req.Params["arguments"], &args); err != nil {
			t.Fatalf("decode arguments: %v", err)
		}
		if args.Name != "probe" || args.Value != 1.5 || args.Labels["queue"] != "a" {
			t.Errorf("arguments = %+v", args)
		}
	})

	t.Run("Log", func(t *testing.T) {
		out, err := c.Log(ctx, hostclient.LogRequest{Level: hostclient.LogLevelInfo, Msg: "hello"})
		if err != nil {
			t.Fatalf("Log: %v", err)
		}
		if !out.OK {
			t.Error("OK = false")
		}
	})

	t.Run("SetHealthState", func(t *testing.T) {
		out, err := c.SetHealthState(ctx, hostclient.SetHealthStateRequest{
			Profile: hostclient.ProfileEventSource, Capability: "channel_message", State: hostclient.HealthStateUnhealthy,
		})
		if err != nil {
			t.Fatalf("SetHealthState: %v", err)
		}
		if !out.OK || !out.Applied {
			t.Errorf("result = %+v", out)
		}
	})
}

func TestTier2_HappyPath(t *testing.T) {
	f := newFakeHostEndpoint(t)
	f.toolResults["host/run_history_read"] = `{"runs":[{"run_id":"run-1","policy_id":"pol-1","status":"complete","started_at":"a","finished_at":"b"}]}`
	f.toolResults["host/user_directory_read"] = `{"users":[{"user_id":"u1","username":"alice","role":"operator"}]}`
	c := newTestClient(t, f, "tok")
	ctx := context.Background()

	t.Run("RunHistoryRead", func(t *testing.T) {
		out, err := c.RunHistoryRead(ctx, hostclient.RunHistoryReadRequest{Limit: 10})
		if err != nil {
			t.Fatalf("RunHistoryRead: %v", err)
		}
		if len(out.Runs) != 1 || out.Runs[0].RunID != "run-1" {
			t.Errorf("Runs = %+v", out.Runs)
		}
	})

	t.Run("UserDirectoryRead", func(t *testing.T) {
		out, err := c.UserDirectoryRead(ctx, hostclient.UserDirectoryReadRequest{RoleFilter: "operator"})
		if err != nil {
			t.Fatalf("UserDirectoryRead: %v", err)
		}
		if len(out.Users) != 1 || out.Users[0].Username != "alice" {
			t.Errorf("Users = %+v", out.Users)
		}
	})
}

func TestActorAndUserLink_HappyPath(t *testing.T) {
	f := newFakeHostEndpoint(t)
	f.toolResults["host/authorize_actor"] = `{"authorized":true,"user_id":"u1"}`
	f.toolResults["host/submit_identity_proof"] = `{"accepted":true}`
	f.toolResults["host/get_user_config"] = `{"user_config_json":"{\"delivery\":\"direct\"}"}`
	c := newTestClient(t, f, "tok")
	ctx := context.Background()

	t.Run("AuthorizeActor", func(t *testing.T) {
		out, err := c.AuthorizeActor(ctx, hostclient.AuthorizeActorRequest{RequestID: "req-1", ActorExternalID: "U123"})
		if err != nil {
			t.Fatalf("AuthorizeActor: %v", err)
		}
		if !out.Authorized || out.UserID != "u1" {
			t.Errorf("result = %+v", out)
		}
	})

	t.Run("SubmitIdentityProof", func(t *testing.T) {
		out, err := c.SubmitIdentityProof(ctx, hostclient.SubmitIdentityProofRequest{ExternalUserID: "U123", Code: "123456"})
		if err != nil {
			t.Fatalf("SubmitIdentityProof: %v", err)
		}
		if !out.Accepted {
			t.Errorf("result = %+v", out)
		}
	})

	t.Run("GetUserConfig", func(t *testing.T) {
		out, err := c.GetUserConfig(ctx, hostclient.GetUserConfigRequest{ExternalUserID: "U123"})
		if err != nil {
			t.Fatalf("GetUserConfig: %v", err)
		}
		if out.UserConfigJSON != `{"delivery":"direct"}` {
			t.Errorf("UserConfigJSON = %q", out.UserConfigJSON)
		}
	})
}

func TestHostError_CodeParsing(t *testing.T) {
	f := newFakeHostEndpoint(t)
	f.toolErrors["host/get_run_context"] = "failed_precondition: host/get_run_context requires a Gleipnir-Call-Id header"
	c := newTestClient(t, f, "tok")

	_, err := c.GetRunContext(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	var hostErr *hostclient.HostError
	if !asHostError(err, &hostErr) {
		t.Fatalf("err = %v (%T), want *hostclient.HostError", err, err)
	}
	if hostErr.Code != "failed_precondition" {
		t.Errorf("Code = %q, want failed_precondition", hostErr.Code)
	}
	if hostErr.Message != "host/get_run_context requires a Gleipnir-Call-Id header" {
		t.Errorf("Message = %q", hostErr.Message)
	}
}

func TestHostError_MessageWithColon(t *testing.T) {
	f := newFakeHostEndpoint(t)
	f.toolErrors["host/log"] = "invalid_argument: bad ratio 3:4 supplied"
	c := newTestClient(t, f, "tok")

	_, err := c.Log(context.Background(), hostclient.LogRequest{Level: "info", Msg: "m"})
	var hostErr *hostclient.HostError
	if !asHostError(err, &hostErr) {
		t.Fatalf("err = %v, want *hostclient.HostError", err)
	}
	if hostErr.Code != "invalid_argument" || hostErr.Message != "bad ratio 3:4 supplied" {
		t.Errorf("HostError = %+v", hostErr)
	}
}

func TestHostError_UnstructuredTextFallsBackToInternal(t *testing.T) {
	f := newFakeHostEndpoint(t)
	f.toolErrors["host/log"] = "something went wrong with no code separator"
	c := newTestClient(t, f, "tok")

	_, err := c.Log(context.Background(), hostclient.LogRequest{Level: "info", Msg: "m"})
	var hostErr *hostclient.HostError
	if !asHostError(err, &hostErr) {
		t.Fatalf("err = %v, want *hostclient.HostError", err)
	}
	if hostErr.Code != "internal" {
		t.Errorf("Code = %q, want internal", hostErr.Code)
	}
}

func TestJSONRPCError_Mapping(t *testing.T) {
	f := newFakeHostEndpoint(t)
	c := newTestClient(t, f, "tok")

	_, err := c.GetInstanceConfig(context.Background()) // no toolResults entry registered
	var rpcErr *hostclient.JSONRPCError
	if !asJSONRPCError(err, &rpcErr) {
		t.Fatalf("err = %v (%T), want *hostclient.JSONRPCError", err, err)
	}
	if rpcErr.Code != -32602 {
		t.Errorf("Code = %d, want -32602", rpcErr.Code)
	}
	if !strings.Contains(rpcErr.Message, "unknown tool") {
		t.Errorf("Message = %q", rpcErr.Message)
	}
}

func TestJSONRPCError_MissingTokenHeader(t *testing.T) {
	f := newFakeHostEndpoint(t)
	f.wantToken = "expected-token"
	c := newTestClient(t, f, "wrong-token")

	_, err := c.Discover(context.Background())
	var rpcErr *hostclient.JSONRPCError
	if !asJSONRPCError(err, &rpcErr) {
		t.Fatalf("err = %v, want *hostclient.JSONRPCError", err)
	}
}

// asHostError and asJSONRPCError are small errors.As wrappers kept local to
// this test file so each call site reads as a plain assertion.
func asHostError(err error, target **hostclient.HostError) bool {
	he, ok := err.(*hostclient.HostError)
	if !ok {
		return false
	}
	*target = he
	return true
}

func asJSONRPCError(err error, target **hostclient.JSONRPCError) bool {
	je, ok := err.(*hostclient.JSONRPCError)
	if !ok {
		return false
	}
	*target = je
	return true
}
