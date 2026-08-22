package hostendpoint

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
)

// fakeTier2Querier is an in-memory Tier2Querier with an audit recorder.
type fakeTier2Querier struct {
	instances   map[string]db.PluginInstance
	plugins     map[string]db.Plugin
	policies    []db.Policy
	runs        []db.ListRunsByPoliciesRow
	allUsers    []db.ListAllActiveUsersWithRolesRow
	usersByRole map[string][]db.ListActiveUsersByRoleRow
	audits      []db.InsertPluginAuditEventParams
}

func (f *fakeTier2Querier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	inst, ok := f.instances[id]
	if !ok {
		return db.PluginInstance{}, sql.ErrNoRows
	}
	return inst, nil
}

func (f *fakeTier2Querier) GetPluginByID(_ context.Context, id string) (db.Plugin, error) {
	p, ok := f.plugins[id]
	if !ok {
		return db.Plugin{}, sql.ErrNoRows
	}
	return p, nil
}

func (f *fakeTier2Querier) ListPolicies(_ context.Context) ([]db.Policy, error) {
	return f.policies, nil
}

// ListRunsByPolicies filters the seeded runs down to the requested policy IDs
// and applies the limit, mirroring what the real SQL query does — this is
// what makes the RunHistoryRead scoping tests self-verifying: a run only
// comes back if its policy ID was actually in the caller-computed scope.
func (f *fakeTier2Querier) ListRunsByPolicies(_ context.Context, arg db.ListRunsByPoliciesParams) ([]db.ListRunsByPoliciesRow, error) {
	inScope := make(map[string]bool, len(arg.PolicyIds))
	for _, id := range arg.PolicyIds {
		inScope[id] = true
	}
	var out []db.ListRunsByPoliciesRow
	for _, r := range f.runs {
		if inScope[r.PolicyID] {
			out = append(out, r)
		}
	}
	if int64(len(out)) > arg.Limit {
		out = out[:arg.Limit]
	}
	return out, nil
}

func (f *fakeTier2Querier) ListAllActiveUsersWithRoles(_ context.Context) ([]db.ListAllActiveUsersWithRolesRow, error) {
	return f.allUsers, nil
}

func (f *fakeTier2Querier) ListActiveUsersByRole(_ context.Context, role string) ([]db.ListActiveUsersByRoleRow, error) {
	return f.usersByRole[role], nil
}

func (f *fakeTier2Querier) InsertPluginAuditEvent(_ context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	f.audits = append(f.audits, arg)
	return db.PluginAuditEvent{}, nil
}

// tier2Fixture is a mounted server plus its fake, exercised over the full
// HTTP dispatch path, same posture as tier1Fixture: the DoD is that the gate
// and the scoping are reachable over the host endpoint, not merely as Go
// functions.
type tier2Fixture struct {
	srv *Server
	q   *fakeTier2Querier
}

func newTier2Fixture(t *testing.T) *tier2Fixture {
	t.Helper()
	q := &fakeTier2Querier{
		instances:   map[string]db.PluginInstance{},
		plugins:     map[string]db.Plugin{},
		usersByRole: map[string][]db.ListActiveUsersByRoleRow{},
	}
	srv := NewServer()
	srv.Register(Tier2Tools(Tier2Deps{Querier: q})...)
	return &tier2Fixture{srv: srv, q: q}
}

// callTool mirrors tier1Fixture.callTool: a tools/call over ServeHTTP with the
// caller's identity in context, the way the middleware chain provides it in
// production.
func (f *tier2Fixture) callTool(t *testing.T, instanceID, tool string, args any) (isError bool, text string) {
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
		t.Fatalf("unexpected transport-level error: %d %s", env.Error.Code, env.Error.Message)
	}
	if len(env.Result.Content) > 0 {
		text = env.Result.Content[0].Text
	}
	return env.Result.IsError, text
}

func decodeTier2Result(t *testing.T, text string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode tool result %q: %v", text, err)
	}
	return out
}

// manifestWithTier2 returns a minimal manifest YAML snapshot declaring the
// given tier2_capabilities entries (empty means none declared).
func manifestWithTier2(caps ...string) string {
	base := `schema_version: v1
name: myplugin
version: 1.0.0
auth:
  mode: instance_credentials
  strategy: none
services:
  tool: v1
`
	if len(caps) == 0 {
		return base
	}
	capYAML := ""
	for _, c := range caps {
		capYAML += "\n  - " + c
	}
	return base + "tier2_capabilities:" + capYAML + "\n"
}

// policyYAMLWithTool returns a minimal policy YAML blob that grants the named
// tool — used to test policyIDsForInstance scoping.
func policyYAMLWithTool(toolName string) string {
	return "task: do something\ncapabilities:\n  tools:\n    - tool: " + toolName + "\n"
}

// ── tests: the capability gate ────────────────────────────────────────────

func TestTier2_CapabilityGate(t *testing.T) {
	cases := []struct {
		name       string
		tool       string
		capability string
		args       any
	}{
		{name: "run_history_read", tool: ToolRunHistoryRead, capability: "run_history_read", args: nil},
		{name: "user_directory_read", tool: ToolUserDirectoryRead, capability: "user_directory_read", args: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTier2Fixture(t)
			f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "myplugin"}
			f.q.plugins["plug-1"] = db.Plugin{ID: "plug-1", ManifestSnapshot: manifestWithTier2()} // no tier2 declared

			isErr, text := f.callTool(t, "inst-1", tc.tool, tc.args)
			if !isErr || !strings.Contains(text, "unauthorized_tier2_call") {
				t.Fatalf("isError=%v text=%q, want unauthorized_tier2_call", isErr, text)
			}
			if !strings.HasPrefix(text, "permission_denied:") {
				t.Errorf("text=%q, want permission_denied code prefix", text)
			}

			// The audit event is not decoration — it is how an operator learns
			// a plugin tried to reach a capability it never declared.
			if len(f.q.audits) != 1 {
				t.Fatalf("audit rows = %d, want 1", len(f.q.audits))
			}
			audit := f.q.audits[0]
			if audit.EventType != EventTypeUnauthorizedTier2Call || audit.Severity != "high" {
				t.Errorf("audit = %s/%s, want %s/high", audit.EventType, audit.Severity, EventTypeUnauthorizedTier2Call)
			}
			if audit.PluginInstanceID == nil || *audit.PluginInstanceID != "inst-1" {
				t.Errorf("audit.PluginInstanceID = %v, want inst-1", audit.PluginInstanceID)
			}
			var payload map[string]string
			if err := json.Unmarshal([]byte(audit.PayloadJson), &payload); err != nil {
				t.Fatalf("payload json parse: %v", err)
			}
			if payload["capability"] != tc.capability {
				t.Errorf("payload.capability = %q, want %q", payload["capability"], tc.capability)
			}
			if payload["tool"] != tc.tool {
				t.Errorf("payload.tool = %q, want %q", payload["tool"], tc.tool)
			}
		})
	}
}

func TestTier2_CapabilityGranted_NoAudit(t *testing.T) {
	f := newTier2Fixture(t)
	f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "myplugin"}
	f.q.plugins["plug-1"] = db.Plugin{ID: "plug-1", ManifestSnapshot: manifestWithTier2("run_history_read", "user_directory_read")}

	isErr, _ := f.callTool(t, "inst-1", ToolRunHistoryRead, nil)
	if isErr {
		t.Fatalf("run_history_read should succeed once declared")
	}
	isErr, _ = f.callTool(t, "inst-1", ToolUserDirectoryRead, nil)
	if isErr {
		t.Fatalf("user_directory_read should succeed once declared")
	}
	if len(f.q.audits) != 0 {
		t.Errorf("audit rows = %d, want 0 for a granted call", len(f.q.audits))
	}
}

func TestTier2_ManifestParseError(t *testing.T) {
	f := newTier2Fixture(t)
	f.q.instances["inst-1"] = db.PluginInstance{ID: "inst-1", PluginID: "plug-1", InstanceName: "myplugin"}
	f.q.plugins["plug-1"] = db.Plugin{ID: "plug-1", ManifestSnapshot: "{{not valid yaml:::"}

	isErr, text := f.callTool(t, "inst-1", ToolRunHistoryRead, nil)
	if !isErr || !strings.HasPrefix(text, "internal:") {
		t.Errorf("isError=%v text=%q, want an internal error on unparseable manifest", isErr, text)
	}
}

// ── tests: RunHistoryRead scoping ─────────────────────────────────────────

// TestRunHistoryRead_ScopedToOwnPolicies is the DoD line: instance A must not
// see a run belonging to a policy that only grants instance B a tool. Without
// this scoping the port becomes "read all run history" — a materially larger
// grant than the declared capability.
func TestRunHistoryRead_ScopedToOwnPolicies(t *testing.T) {
	f := newTier2Fixture(t)
	f.q.instances["inst-a"] = db.PluginInstance{ID: "inst-a", PluginID: "plug-a", InstanceName: "instance-a"}
	f.q.plugins["plug-a"] = db.Plugin{ID: "plug-a", ManifestSnapshot: manifestWithTier2("run_history_read")}
	f.q.policies = []db.Policy{
		{ID: "pol-a", Yaml: policyYAMLWithTool("instance-a.do_thing")},
		{ID: "pol-b", Yaml: policyYAMLWithTool("instance-b.do_thing")},
	}
	f.q.runs = []db.ListRunsByPoliciesRow{
		{ID: "run-a", PolicyID: "pol-a", Status: "complete", StartedAt: "2026-08-01T10:00:00Z", CreatedAt: "2026-08-01T09:55:00Z"},
		{ID: "run-b", PolicyID: "pol-b", Status: "complete", StartedAt: "2026-08-01T11:00:00Z", CreatedAt: "2026-08-01T10:55:00Z"},
	}

	isErr, text := f.callTool(t, "inst-a", ToolRunHistoryRead, nil)
	if isErr {
		t.Fatalf("error: %s", text)
	}
	res := decodeTier2Result(t, text)
	runs, ok := res["runs"].([]any)
	if !ok {
		t.Fatalf("runs is not a list: %v", res["runs"])
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1 (only pol-a's run)", len(runs))
	}
	run := runs[0].(map[string]any)
	if run["run_id"] != "run-a" {
		t.Errorf("run_id = %v, want run-a — instance-a must not see instance-b's run", run["run_id"])
	}
}

func TestRunHistoryRead_NoScopedPolicies_ReturnsEmptyNotError(t *testing.T) {
	f := newTier2Fixture(t)
	f.q.instances["inst-a"] = db.PluginInstance{ID: "inst-a", PluginID: "plug-a", InstanceName: "instance-a"}
	f.q.plugins["plug-a"] = db.Plugin{ID: "plug-a", ManifestSnapshot: manifestWithTier2("run_history_read")}
	f.q.policies = []db.Policy{
		{ID: "pol-b", Yaml: policyYAMLWithTool("instance-b.do_thing")},
	}

	isErr, text := f.callTool(t, "inst-a", ToolRunHistoryRead, nil)
	if isErr {
		t.Fatalf("error: %s", text)
	}
	res := decodeTier2Result(t, text)
	runs, ok := res["runs"].([]any)
	if !ok {
		t.Fatalf("runs is not a list: %v", res["runs"])
	}
	if len(runs) != 0 {
		t.Errorf("runs = %d, want 0", len(runs))
	}
}

// TestRunHistoryRead_SubscribedTriggerScoping asserts the second scoping path
// carried over from hostsvc: a policy with a subscribed trigger sourced from
// the calling instance is in scope even without a tool grant, and a policy
// matching neither path is not.
func TestRunHistoryRead_SubscribedTriggerScoping(t *testing.T) {
	subscribedYAML := "task: do something\ntrigger:\n  type: subscribed\n  source: instance-a\n  event_kind: something_happened\n"

	f := newTier2Fixture(t)
	f.q.instances["inst-a"] = db.PluginInstance{ID: "inst-a", PluginID: "plug-a", InstanceName: "instance-a"}
	f.q.plugins["plug-a"] = db.Plugin{ID: "plug-a", ManifestSnapshot: manifestWithTier2("run_history_read")}
	f.q.policies = []db.Policy{
		{ID: "pol-sub", Yaml: subscribedYAML},
		{ID: "pol-other", Yaml: policyYAMLWithTool("instance-b.do_thing")},
	}
	f.q.runs = []db.ListRunsByPoliciesRow{
		{ID: "run-sub", PolicyID: "pol-sub", Status: "complete", StartedAt: "2026-08-01T10:00:00Z", CreatedAt: "2026-08-01T09:55:00Z"},
		{ID: "run-other", PolicyID: "pol-other", Status: "complete", StartedAt: "2026-08-01T11:00:00Z", CreatedAt: "2026-08-01T10:55:00Z"},
	}

	isErr, text := f.callTool(t, "inst-a", ToolRunHistoryRead, nil)
	if isErr {
		t.Fatalf("error: %s", text)
	}
	res := decodeTier2Result(t, text)
	runs := res["runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["run_id"] != "run-sub" {
		t.Errorf("runs = %v, want exactly run-sub", runs)
	}
}

func TestRunHistoryRead_RequestedPolicyOutOfScope(t *testing.T) {
	f := newTier2Fixture(t)
	f.q.instances["inst-a"] = db.PluginInstance{ID: "inst-a", PluginID: "plug-a", InstanceName: "instance-a"}
	f.q.plugins["plug-a"] = db.Plugin{ID: "plug-a", ManifestSnapshot: manifestWithTier2("run_history_read")}
	f.q.policies = []db.Policy{
		{ID: "pol-a", Yaml: policyYAMLWithTool("instance-a.do_thing")},
		{ID: "pol-b", Yaml: policyYAMLWithTool("instance-b.do_thing")},
	}
	f.q.runs = []db.ListRunsByPoliciesRow{
		{ID: "run-b", PolicyID: "pol-b", Status: "complete", StartedAt: "2026-08-01T11:00:00Z", CreatedAt: "2026-08-01T10:55:00Z"},
	}

	// Instance A explicitly asks for pol-b's history. It is not an error —
	// the response is empty, so the caller cannot use this to learn whether
	// pol-b exists.
	isErr, text := f.callTool(t, "inst-a", ToolRunHistoryRead, map[string]any{"policy_id": "pol-b"})
	if isErr {
		t.Fatalf("error: %s", text)
	}
	res := decodeTier2Result(t, text)
	runs := res["runs"].([]any)
	if len(runs) != 0 {
		t.Errorf("runs = %d, want 0 for an out-of-scope policy_id", len(runs))
	}
}

func TestRunHistoryRead_LimitClamping(t *testing.T) {
	f := newTier2Fixture(t)
	f.q.instances["inst-a"] = db.PluginInstance{ID: "inst-a", PluginID: "plug-a", InstanceName: "instance-a"}
	f.q.plugins["plug-a"] = db.Plugin{ID: "plug-a", ManifestSnapshot: manifestWithTier2("run_history_read")}
	f.q.policies = []db.Policy{{ID: "pol-a", Yaml: policyYAMLWithTool("instance-a.do_thing")}}
	for i := 0; i < 150; i++ {
		f.q.runs = append(f.q.runs, db.ListRunsByPoliciesRow{
			ID: fmt.Sprintf("run-%d", i), PolicyID: "pol-a", Status: "complete",
			StartedAt: "2026-08-01T10:00:00Z", CreatedAt: "2026-08-01T09:55:00Z",
		})
	}

	cases := []struct {
		name  string
		limit any
		want  int
	}{
		{name: "zero defaults to 100", limit: 0, want: 100},
		{name: "negative defaults to 100", limit: -5, want: 100},
		{name: "over cap clamps to 100", limit: 500, want: 100},
		{name: "under cap is respected", limit: 10, want: 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isErr, text := f.callTool(t, "inst-a", ToolRunHistoryRead, map[string]any{"limit": tc.limit})
			if isErr {
				t.Fatalf("error: %s", text)
			}
			res := decodeTier2Result(t, text)
			runs := res["runs"].([]any)
			if len(runs) != tc.want {
				t.Errorf("runs = %d, want %d", len(runs), tc.want)
			}
		})
	}
}

// ── tests: UserDirectoryRead ───────────────────────────────────────────────

// TestUserDirectoryRead_ResponseShapePinned locks the exact key set on both
// the envelope and each entry, so a future field addition (e.g. accidentally
// forwarding a session token) is a deliberate edit here, not a silent leak.
func TestUserDirectoryRead_ResponseShapePinned(t *testing.T) {
	f := newTier2Fixture(t)
	f.q.instances["inst-a"] = db.PluginInstance{ID: "inst-a", PluginID: "plug-a", InstanceName: "instance-a"}
	f.q.plugins["plug-a"] = db.Plugin{ID: "plug-a", ManifestSnapshot: manifestWithTier2("user_directory_read")}
	f.q.allUsers = []db.ListAllActiveUsersWithRolesRow{
		{UserID: "u1", Username: "alice", Role: "operator"},
	}

	isErr, text := f.callTool(t, "inst-a", ToolUserDirectoryRead, nil)
	if isErr {
		t.Fatalf("error: %s", text)
	}
	res := decodeTier2Result(t, text)
	if keys := sortedKeys(res); len(keys) != 1 || keys[0] != "users" {
		t.Fatalf("envelope keys = %v, want exactly [users]", keys)
	}
	users, ok := res["users"].([]any)
	if !ok || len(users) != 1 {
		t.Fatalf("users = %v, want one entry", res["users"])
	}
	entry, ok := users[0].(map[string]any)
	if !ok {
		t.Fatalf("entry is not an object: %v", users[0])
	}
	want := []string{"role", "user_id", "username"}
	if keys := sortedKeys(entry); !equalStrings(keys, want) {
		t.Fatalf("entry keys = %v, want exactly %v — no credentials, no session data", keys, want)
	}
	if entry["user_id"] != "u1" || entry["username"] != "alice" || entry["role"] != "operator" {
		t.Errorf("entry = %v", entry)
	}
}

func TestUserDirectoryRead_RoleFilter(t *testing.T) {
	f := newTier2Fixture(t)
	f.q.instances["inst-a"] = db.PluginInstance{ID: "inst-a", PluginID: "plug-a", InstanceName: "instance-a"}
	f.q.plugins["plug-a"] = db.Plugin{ID: "plug-a", ManifestSnapshot: manifestWithTier2("user_directory_read")}
	f.q.usersByRole["approver"] = []db.ListActiveUsersByRoleRow{
		{UserID: "u2", Username: "bob"},
	}
	f.q.allUsers = []db.ListAllActiveUsersWithRolesRow{
		{UserID: "u1", Username: "alice", Role: "operator"},
		{UserID: "u2", Username: "bob", Role: "approver"},
	}

	isErr, text := f.callTool(t, "inst-a", ToolUserDirectoryRead, map[string]any{"role_filter": "approver"})
	if isErr {
		t.Fatalf("error: %s", text)
	}
	res := decodeTier2Result(t, text)
	users := res["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("users = %d, want 1", len(users))
	}
	entry := users[0].(map[string]any)
	if entry["user_id"] != "u2" || entry["role"] != "approver" {
		t.Errorf("entry = %v", entry)
	}
}

func TestUserDirectoryRead_UnknownRoleIsRejected(t *testing.T) {
	f := newTier2Fixture(t)
	f.q.instances["inst-a"] = db.PluginInstance{ID: "inst-a", PluginID: "plug-a", InstanceName: "instance-a"}
	f.q.plugins["plug-a"] = db.Plugin{ID: "plug-a", ManifestSnapshot: manifestWithTier2("user_directory_read")}

	isErr, text := f.callTool(t, "inst-a", ToolUserDirectoryRead, map[string]any{"role_filter": "superuser"})
	if !isErr || !strings.Contains(text, "invalid_argument") {
		t.Errorf("isError=%v text=%q, want invalid_argument", isErr, text)
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
