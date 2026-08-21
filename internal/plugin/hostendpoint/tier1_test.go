package hostendpoint

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	"github.com/felag-engineering/gleipnir/internal/plugin/pluginmetrics"
)

// fakeTier1Querier is an in-memory Tier1Querier with an audit recorder.
type fakeTier1Querier struct {
	instances map[string]db.PluginInstance
	runs      map[string]db.Run
	steps     map[string]db.RunStep // runID → latest step
	audits    []db.InsertPluginAuditEventParams
}

func (f *fakeTier1Querier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	inst, ok := f.instances[id]
	if !ok {
		return db.PluginInstance{}, sql.ErrNoRows
	}
	return inst, nil
}

func (f *fakeTier1Querier) GetRun(_ context.Context, id string) (db.Run, error) {
	run, ok := f.runs[id]
	if !ok {
		return db.Run{}, sql.ErrNoRows
	}
	return run, nil
}

func (f *fakeTier1Querier) GetLatestRunStep(_ context.Context, runID string) (db.RunStep, error) {
	step, ok := f.steps[runID]
	if !ok {
		return db.RunStep{}, sql.ErrNoRows
	}
	return step, nil
}

func (f *fakeTier1Querier) InsertPluginAuditEvent(_ context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	f.audits = append(f.audits, arg)
	return db.PluginAuditEvent{}, nil
}

// fakeCallResolver maps call ids to CallInfo.
type fakeCallResolver struct{ calls map[string]dispatch.CallInfo }

func (f *fakeCallResolver) LookupCall(callID string) (dispatch.CallInfo, bool) {
	info, ok := f.calls[callID]
	return info, ok
}

// tier1Fixture is a mounted server plus its fakes, exercised over the full
// HTTP dispatch path — DoD: the six are reachable over the host endpoint,
// not merely as Go functions.
type tier1Fixture struct {
	srv    *Server
	q      *fakeTier1Querier
	health *caphealth.Registry
}

func newTier1Fixture(t *testing.T, key []byte) *tier1Fixture {
	t.Helper()
	q := &fakeTier1Querier{
		instances: map[string]db.PluginInstance{},
		runs:      map[string]db.Run{},
		steps:     map[string]db.RunStep{},
	}
	health := caphealth.NewRegistry()
	srv := NewServer()
	srv.Register(Tier1Tools(Tier1Deps{
		Querier:       q,
		EncryptionKey: key,
		Calls: &fakeCallResolver{calls: map[string]dispatch.CallInfo{
			"call-1": {RunID: "run-1", PolicyID: "pol-1", InstanceName: "slack-prod"},
		}},
		Metrics: pluginmetrics.New(),
		Health:  health,
	})...)
	return &tier1Fixture{srv: srv, q: q, health: health}
}

// callTool performs a tools/call over ServeHTTP with the caller's identity
// and optional call id in context, the way the middleware chain provides
// them in production.
func (f *tier1Fixture) callTool(t *testing.T, instanceID, callID, tool string, args any) (statusCode int, isError bool, text string) {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw)))
	req.Header.Set("MCP-Protocol-Version", mcp.ProtocolVersion20260728)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", tool)
	ctx := context.WithValue(req.Context(), identityCtxKey{}, Identity{InstanceID: instanceID})
	if callID != "" {
		ctx = context.WithValue(ctx, callIDCtxKey{}, callID)
	}
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, req.WithContext(ctx))

	var env struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	if env.Error != nil {
		return w.Code, true, fmt.Sprintf("jsonrpc %d: %s", env.Error.Code, env.Error.Message)
	}
	if len(env.Result.Content) > 0 {
		text = env.Result.Content[0].Text
	}
	return w.Code, env.Result.IsError, text
}

func decodeResult(t *testing.T, text string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode tool result %q: %v", text, err)
	}
	return out
}

func TestTier1_ConfigCredentialsRunContext(t *testing.T) {
	key := make([]byte, 32)
	enc, err := crypto.Encrypt(key, `{"strategy":"static_api_key"}`)
	if err != nil {
		t.Fatalf("encrypt fixture: %v", err)
	}
	f := newTier1Fixture(t, key)
	f.q.instances["inst-1"] = db.PluginInstance{
		ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod",
		ConfigJson: `{"channel":"#ops"}`, CredentialsEncrypted: &enc,
	}
	f.q.instances["inst-2"] = db.PluginInstance{ID: "inst-2", PluginID: "plug-2", InstanceName: "other"}
	f.q.runs["run-1"] = db.Run{ID: "run-1", StartedAt: "2026-08-21T10:00:00Z"}
	f.q.steps["run-1"] = db.RunStep{StepNumber: 4}

	t.Run("get_instance_config returns config_json verbatim", func(t *testing.T) {
		_, isErr, text := f.callTool(t, "inst-1", "", ToolGetInstanceConfig, nil)
		if isErr {
			t.Fatalf("error: %s", text)
		}
		if got := decodeResult(t, text)["config_json"]; got != `{"channel":"#ops"}` {
			t.Errorf("config_json = %v", got)
		}
	})

	t.Run("get_credentials decrypts the stored blob", func(t *testing.T) {
		_, isErr, text := f.callTool(t, "inst-1", "", ToolGetCredentials, nil)
		if isErr {
			t.Fatalf("error: %s", text)
		}
		if got := decodeResult(t, text)["credentials_json"]; got != `{"strategy":"static_api_key"}` {
			t.Errorf("credentials_json = %v", got)
		}
	})

	t.Run("get_credentials with none configured is empty, not an error", func(t *testing.T) {
		_, isErr, text := f.callTool(t, "inst-2", "", ToolGetCredentials, nil)
		if isErr {
			t.Fatalf("error: %s", text)
		}
		if got := decodeResult(t, text)["credentials_json"]; got != "" {
			t.Errorf("credentials_json = %v, want empty", got)
		}
	})

	t.Run("get_run_context resolves the caller's own call", func(t *testing.T) {
		_, isErr, text := f.callTool(t, "inst-1", "call-1", ToolGetRunContext, nil)
		if isErr {
			t.Fatalf("error: %s", text)
		}
		res := decodeResult(t, text)
		if res["run_id"] != "run-1" || res["policy_id"] != "pol-1" {
			t.Errorf("run context = %v", res)
		}
		if res["step_index"] != float64(5) {
			t.Errorf("step_index = %v, want 5 (latest step 4 + 1)", res["step_index"])
		}
	})

	t.Run("a foreign call id is refused and audited at high severity", func(t *testing.T) {
		// call-1 belongs to slack-prod (inst-1); inst-2 presents it.
		before := len(f.q.audits)
		_, isErr, text := f.callTool(t, "inst-2", "call-1", ToolGetRunContext, nil)
		if !isErr || !strings.Contains(text, "unauthorized_request_id") {
			t.Fatalf("foreign call id: isError=%v text=%q, want unauthorized_request_id", isErr, text)
		}
		if len(f.q.audits) != before+1 {
			t.Fatalf("audit rows = %d, want one new unauthorized_request_id event", len(f.q.audits)-before)
		}
		audit := f.q.audits[len(f.q.audits)-1]
		if audit.EventType != EventTypeUnauthorizedRequestID || audit.Severity != "high" {
			t.Errorf("audit = %s/%s, want %s/high", audit.EventType, audit.Severity, EventTypeUnauthorizedRequestID)
		}
	})

	t.Run("get_run_context without a call id is a precondition failure", func(t *testing.T) {
		_, isErr, text := f.callTool(t, "inst-1", "", ToolGetRunContext, nil)
		if !isErr || !strings.Contains(text, "failed_precondition") {
			t.Errorf("isError=%v text=%q, want failed_precondition", isErr, text)
		}
	})
}

func TestTier1_EmitMetric(t *testing.T) {
	f := newTier1Fixture(t, nil)
	f.q.instances["inst-m"] = db.PluginInstance{ID: "inst-m", PluginID: "plug-m", InstanceName: "m"}

	t.Run("valid emission succeeds through the endpoint", func(t *testing.T) {
		_, isErr, text := f.callTool(t, "inst-m", "", ToolEmitMetric,
			map[string]any{"name": "endpoint_probe", "value": 1.5, "labels": map[string]string{"queue": "a"}})
		if isErr {
			t.Fatalf("error: %s", text)
		}
	})

	t.Run("inconsistent label keys are rejected with the stable code", func(t *testing.T) {
		_, isErr, text := f.callTool(t, "inst-m", "", ToolEmitMetric,
			map[string]any{"name": "endpoint_probe", "value": 2.0, "labels": map[string]string{"other": "b"}})
		if !isErr || !strings.Contains(text, "inconsistent_label_keys") {
			t.Errorf("isError=%v text=%q, want inconsistent_label_keys", isErr, text)
		}
	})

	t.Run("cardinality cap rejects with the stable code", func(t *testing.T) {
		for i := 0; i < pluginmetrics.CardinalityCap; i++ {
			_, isErr, text := f.callTool(t, "inst-m", "", ToolEmitMetric,
				map[string]any{"name": "endpoint_card", "value": 1, "labels": map[string]string{"shard": fmt.Sprintf("s%04d", i)}})
			if isErr {
				t.Fatalf("value %d: %s", i, text)
			}
		}
		_, isErr, text := f.callTool(t, "inst-m", "", ToolEmitMetric,
			map[string]any{"name": "endpoint_card", "value": 1, "labels": map[string]string{"shard": "overflow"}})
		if !isErr || !strings.Contains(text, "cardinality_cap_exceeded") {
			t.Errorf("isError=%v text=%q, want cardinality_cap_exceeded", isErr, text)
		}
	})

	t.Run("reserved labels are rejected", func(t *testing.T) {
		_, isErr, text := f.callTool(t, "inst-m", "", ToolEmitMetric,
			map[string]any{"name": "endpoint_reserved", "value": 1, "labels": map[string]string{"plugin": "spoof"}})
		if !isErr || !strings.Contains(text, "reserved_label") {
			t.Errorf("isError=%v text=%q, want reserved_label", isErr, text)
		}
	})
}

func TestTier1_SetHealthState_PerCapability(t *testing.T) {
	f := newTier1Fixture(t, nil)
	f.q.instances["inst-h"] = db.PluginInstance{ID: "inst-h", PluginID: "plug-h", InstanceName: "h"}

	// Serves() is liveness-gated: an instance nothing has probed reports NOT
	// live, and a liveness fault overrides every capability answer. These
	// tests are about capability scoping, so the instance is live throughout.
	f.health.SetLiveness("inst-h", caphealth.Liveness{ContainerHealthy: true, DiscoverOK: true})

	t.Run("a fault on one capability leaves the others serving", func(t *testing.T) {
		// The DoD line, and the reason the method changed shape at all: the
		// v1.1 per-instance report turned one missing scope into a total
		// outage.
		_, isErr, text := f.callTool(t, "inst-h", "", ToolSetHealthState, map[string]any{
			"profile": "event_source", "capability": "channel_message",
			"state": "unhealthy", "detail": "missing scope channels:history",
		})
		if isErr {
			t.Fatalf("error: %s", text)
		}
		faulted := caphealth.Capability{Profile: caphealth.ProfileEventSource, Name: "channel_message"}
		if f.health.Serves("inst-h", faulted) {
			t.Error("faulted capability still serves")
		}
		for _, still := range []caphealth.Capability{
			{Profile: caphealth.ProfileToolProvider, Name: "post_message"},
			{Profile: caphealth.ProfileHumanChannel},
			{Profile: caphealth.ProfileEventSource, Name: "reaction_added"},
		} {
			if !f.health.Serves("inst-h", still) {
				t.Errorf("capability %s stopped serving — the fault was not scoped", still)
			}
		}
	})

	t.Run("a plugin cannot improve its own capability (§8.1 worsen-only)", func(t *testing.T) {
		_, isErr, text := f.callTool(t, "inst-h", "", ToolSetHealthState, map[string]any{
			"profile": "event_source", "capability": "channel_message", "state": "healthy",
		})
		if isErr {
			t.Fatalf("error: %s", text)
		}
		res := decodeResult(t, text)
		if res["applied"] != false {
			t.Errorf("applied = %v, want false — an improvement report must be a no-op", res["applied"])
		}
		if f.health.Serves("inst-h", caphealth.Capability{Profile: caphealth.ProfileEventSource, Name: "channel_message"}) {
			t.Error("self-report cleared a fault")
		}
	})

	t.Run("host observation can clear the fault afterwards", func(t *testing.T) {
		f.health.ClearCapability("inst-h", caphealth.Capability{Profile: caphealth.ProfileEventSource, Name: "channel_message"})
		if !f.health.Serves("inst-h", caphealth.Capability{Profile: caphealth.ProfileEventSource, Name: "channel_message"}) {
			t.Error("cleared capability does not serve")
		}
	})

	t.Run("unknown profile and non-reportable state are rejected", func(t *testing.T) {
		_, isErr, text := f.callTool(t, "inst-h", "", ToolSetHealthState,
			map[string]any{"profile": "nonsense", "state": "unhealthy"})
		if !isErr || !strings.Contains(text, "invalid_argument") {
			t.Errorf("unknown profile: isError=%v text=%q", isErr, text)
		}
		_, isErr, text = f.callTool(t, "inst-h", "", ToolSetHealthState,
			map[string]any{"profile": "tool_provider", "state": "circuit_broken"})
		if !isErr || !strings.Contains(text, "not plugin-reportable") {
			t.Errorf("host-only state: isError=%v text=%q — circuit_broken is a host verdict a plugin must not assert", isErr, text)
		}
	})
}

func TestTier1_Log(t *testing.T) {
	f := newTier1Fixture(t, nil)
	f.q.instances["inst-l"] = db.PluginInstance{ID: "inst-l", PluginID: "plug-l", InstanceName: "slack-prod"}

	t.Run("a valid line succeeds", func(t *testing.T) {
		_, isErr, text := f.callTool(t, "inst-l", "call-1", ToolLog,
			map[string]any{"level": "info", "msg": "hello", "attrs": map[string]string{"k": "v"}})
		if isErr {
			t.Fatalf("error: %s", text)
		}
	})

	oversize := strings.Repeat("x", maxLogMsgBytes+1)
	manyAttrs := map[string]string{}
	for i := 0; i <= maxLogAttrs; i++ {
		manyAttrs[fmt.Sprintf("k%02d", i)] = "v"
	}
	rejections := []struct {
		name string
		args map[string]any
	}{
		{name: "oversize msg", args: map[string]any{"msg": oversize}},
		{name: "too many attrs", args: map[string]any{"msg": "m", "attrs": manyAttrs}},
		{name: "oversize attr value", args: map[string]any{"msg": "m", "attrs": map[string]string{"k": strings.Repeat("v", maxLogAttrBytes+1)}}},
	}
	for _, tc := range rejections {
		t.Run(tc.name+" is rejected", func(t *testing.T) {
			_, isErr, text := f.callTool(t, "inst-l", "", ToolLog, tc.args)
			if !isErr || !strings.Contains(text, "invalid_argument") {
				t.Errorf("isError=%v text=%q, want invalid_argument", isErr, text)
			}
		})
	}
}

func TestToolDispatch(t *testing.T) {
	f := newTier1Fixture(t, nil)
	f.q.instances["inst-d"] = db.PluginInstance{ID: "inst-d", PluginID: "plug-d", InstanceName: "d"}

	t.Run("tools/list serves the six in inventory order", func(t *testing.T) {
		body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}}
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw)))
		req.Header.Set("MCP-Protocol-Version", mcp.ProtocolVersion20260728)
		req.Header.Set("Mcp-Method", "tools/list")
		w := httptest.NewRecorder()
		f.srv.ServeHTTP(w, req)

		var env struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		want := []string{ToolGetInstanceConfig, ToolGetCredentials, ToolGetRunContext, ToolEmitMetric, ToolLog, ToolSetHealthState}
		if len(env.Result.Tools) != len(want) {
			t.Fatalf("tools = %d, want %d", len(env.Result.Tools), len(want))
		}
		for i, name := range want {
			if env.Result.Tools[i].Name != name {
				t.Errorf("tools[%d] = %s, want %s", i, env.Result.Tools[i].Name, name)
			}
		}
	})

	t.Run("unknown tool is a JSON-RPC error, not a tool result", func(t *testing.T) {
		code, isErr, text := f.callTool(t, "inst-d", "", "host/no_such_tool", nil)
		if code != http.StatusBadRequest || !isErr || !strings.Contains(text, "unknown tool") {
			t.Errorf("code=%d isErr=%v text=%q", code, isErr, text)
		}
	})

	t.Run("wrong Mcp-Name header is a header mismatch", func(t *testing.T) {
		body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": ToolLog, "arguments": map[string]any{"msg": "m"}}}
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw)))
		req.Header.Set("MCP-Protocol-Version", mcp.ProtocolVersion20260728)
		req.Header.Set("Mcp-Method", "tools/call")
		req.Header.Set("Mcp-Name", ToolEmitMetric) // names a different tool
		w := httptest.NewRecorder()
		f.srv.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Mcp-Name") {
			t.Errorf("code=%d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("registering a tool outside the inventory panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("Register accepted a name the host-plane assertion never guards")
			}
		}()
		NewServer().Register(ToolDef{Name: "host/not_in_inventory", Handler: func(context.Context, json.RawMessage) (any, error) { return nil, nil }})
	})
}
