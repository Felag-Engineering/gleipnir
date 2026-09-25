package hostendpoint_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	"github.com/felag-engineering/gleipnir/internal/plugin/pluginmetrics"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient/hostclienttest"
)

// This file is issue #971's DoD proof that plugin-sdk's fake host endpoint
// (hostclient/hostclienttest) is contract-faithful to the real one: the same
// hostclient calls, configured with equivalent fixture data, are driven
// against the real hostendpoint.Server (over its real Chain middleware) and
// against hostclienttest.Server, and the two must answer identically. A
// wire-shape or error-code drift between the two shows up here rather than
// being discovered by a plugin author whose test passed against the fake and
// failed against the real host.

// contractQuerier is the union of every sqlc surface the four mounted tool
// groups need (Tier1Querier, Tier2Querier, AuthorizeActorQuerier,
// UserLinkQuerier) — one fake satisfying all four Deps' Querier fields via Go's
// structural typing, since they intentionally carry independent (but
// overlapping) interfaces (see tier1.go/tier2.go's doc comments on why the
// Deps structs stay separate).
type contractQuerier struct {
	instances   map[string]db.PluginInstance
	plugins     map[string]db.Plugin
	runs        map[string]db.Run
	steps       map[string]db.RunStep
	policies    []db.Policy
	runRows     []db.ListRunsByPoliciesRow
	allUsers    []db.ListAllActiveUsersWithRolesRow
	usersByRole map[string][]db.ListActiveUsersByRoleRow
	audits      []db.InsertPluginAuditEventParams
}

func (q *contractQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	inst, ok := q.instances[id]
	if !ok {
		return db.PluginInstance{}, sql.ErrNoRows
	}
	return inst, nil
}

func (q *contractQuerier) GetRun(_ context.Context, id string) (db.Run, error) {
	run, ok := q.runs[id]
	if !ok {
		return db.Run{}, sql.ErrNoRows
	}
	return run, nil
}

func (q *contractQuerier) GetLatestRunStep(_ context.Context, runID string) (db.RunStep, error) {
	step, ok := q.steps[runID]
	if !ok {
		return db.RunStep{}, sql.ErrNoRows
	}
	return step, nil
}

func (q *contractQuerier) InsertPluginAuditEvent(_ context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	q.audits = append(q.audits, arg)
	return db.PluginAuditEvent{}, nil
}

func (q *contractQuerier) GetPluginByID(_ context.Context, id string) (db.Plugin, error) {
	p, ok := q.plugins[id]
	if !ok {
		return db.Plugin{}, sql.ErrNoRows
	}
	return p, nil
}

func (q *contractQuerier) ListPolicies(_ context.Context) ([]db.Policy, error) {
	return q.policies, nil
}

// ListRunsByPolicies mirrors tier2_test.go's fakeTier2Querier: filter the
// seeded rows to the requested policy IDs and apply the limit, the same
// shape the real SQL query produces.
func (q *contractQuerier) ListRunsByPolicies(_ context.Context, arg db.ListRunsByPoliciesParams) ([]db.ListRunsByPoliciesRow, error) {
	inScope := make(map[string]bool, len(arg.PolicyIds))
	for _, id := range arg.PolicyIds {
		inScope[id] = true
	}
	var out []db.ListRunsByPoliciesRow
	for _, r := range q.runRows {
		if inScope[r.PolicyID] {
			out = append(out, r)
		}
	}
	if int64(len(out)) > arg.Limit {
		out = out[:arg.Limit]
	}
	return out, nil
}

func (q *contractQuerier) ListAllActiveUsersWithRoles(_ context.Context) ([]db.ListAllActiveUsersWithRolesRow, error) {
	return q.allUsers, nil
}

func (q *contractQuerier) ListActiveUsersByRole(_ context.Context, role string) ([]db.ListActiveUsersByRoleRow, error) {
	return q.usersByRole[role], nil
}

// contractCallResolver mirrors sdkclient_integration_test.go's
// fakeIntegrationCallResolver.
type contractCallResolver struct {
	calls map[string]dispatch.CallInfo
}

func (f *contractCallResolver) LookupCall(callID string) (dispatch.CallInfo, bool) {
	info, ok := f.calls[callID]
	return info, ok
}

// contractActorDirectory implements hostendpoint.ActorDirectory with a
// static map, mirroring authorize_test.go's fakeActorDirectory (duplicated
// here since that fake is unexported in an internal test file this external
// test package cannot reach).
type contractActorDirectory struct {
	byExternalID map[string]hostendpoint.ActorResolution
}

func (d contractActorDirectory) Resolve(_ context.Context, actorExternalID string) (hostendpoint.ActorResolution, bool, error) {
	res, ok := d.byExternalID[actorExternalID]
	return res, ok, nil
}

// contractBinder implements hostendpoint.PendingLinkBinder with a fixed
// outcome.
type contractBinder struct {
	accept bool
	reason string
}

func (b contractBinder) BindInboundCode(_ context.Context, _, _, _ string) (hostendpoint.BindResult, error) {
	return hostendpoint.BindResult{Accepted: b.accept, Reason: b.reason}, nil
}

// contractUserConfigReader implements hostendpoint.UserConfigReader with a
// static per-user map.
type contractUserConfigReader struct {
	cfg map[string]string
}

func (r contractUserConfigReader) GetUserConfig(_ context.Context, _, externalUserID string) (json.RawMessage, error) {
	cfg, ok := r.cfg[externalUserID]
	if !ok {
		return json.RawMessage("{}"), nil
	}
	return json.RawMessage(cfg), nil
}

// manifestWithTier2 mirrors tier2_test.go's helper of the same name.
func manifestWithTier2(caps ...string) string {
	base := "schema_version: v1\nname: myplugin\nversion: 1.0.0\n" +
		"auth:\n  mode: instance_credentials\n  strategy: none\nservices:\n  tool: v1\n"
	if len(caps) == 0 {
		return base
	}
	capYAML := ""
	for _, c := range caps {
		capYAML += "\n  - " + c
	}
	return base + "tier2_capabilities:" + capYAML + "\n"
}

// policyYAMLWithTool mirrors tier2_test.go's helper of the same name.
func policyYAMLWithTool(toolName string) string {
	return "task: do something\ncapabilities:\n  tools:\n    - tool: " + toolName + "\n"
}

// newRealContractFixture wires a real Server behind the real Chain
// middleware, with every tool group mounted, matching the fixture data
// newFakeContractFixture configures on the fake.
func newRealContractFixture(t *testing.T) (baseURL, token string) {
	t.Helper()

	q := &contractQuerier{
		instances: map[string]db.PluginInstance{
			"inst-1": {ID: "inst-1", PluginID: "plug-1", InstanceName: "sdk-test", ConfigJson: `{"channel":"#ops"}`},
		},
		plugins: map[string]db.Plugin{
			"plug-1": {ID: "plug-1", ManifestSnapshot: manifestWithTier2("run_history_read", "user_directory_read")},
		},
		runs:  map[string]db.Run{"run-1": {ID: "run-1", StartedAt: "2026-08-23T10:00:00Z"}},
		steps: map[string]db.RunStep{"run-1": {StepNumber: 2}},
		policies: []db.Policy{
			{ID: "pol-1", Yaml: policyYAMLWithTool("sdk-test.something")},
		},
		runRows: []db.ListRunsByPoliciesRow{
			{ID: "run-1", PolicyID: "pol-1", Status: "complete", StartedAt: "a", CompletedAt: strPtr("b")},
		},
		allUsers:    []db.ListAllActiveUsersWithRolesRow{{UserID: "u1", Username: "alice", Role: "operator"}},
		usersByRole: map[string][]db.ListActiveUsersByRoleRow{"operator": {{UserID: "u1", Username: "alice"}}},
	}

	srv := hostendpoint.NewServer()
	srv.Register(hostendpoint.Tier1Tools(hostendpoint.Tier1Deps{
		Querier: q,
		Calls: &contractCallResolver{calls: map[string]dispatch.CallInfo{
			"call-1": {RunID: "run-1", PolicyID: "pol-1", InstanceName: "sdk-test"},
		}},
		Metrics: pluginmetrics.New(),
		Health:  caphealth.NewRegistry(),
	})...)
	srv.Register(hostendpoint.Tier2Tools(hostendpoint.Tier2Deps{Querier: q})...)
	srv.Register(hostendpoint.AuthorizeActorTools(hostendpoint.AuthorizeActorDeps{
		Querier: q,
		Directory: contractActorDirectory{byExternalID: map[string]hostendpoint.ActorResolution{
			"U123": {UserID: "user-1", Roles: []model.Role{model.RoleOperator}},
		}},
	})...)
	srv.Register(hostendpoint.UserLinkTools(hostendpoint.UserLinkDeps{
		Querier:      q,
		Binder:       contractBinder{accept: true},
		ConfigReader: contractUserConfigReader{cfg: map[string]string{"U1": `{"delivery":"direct"}`}},
	})...)

	registry := identity.New()
	tok, err := registry.Issue("inst-1")
	if err != nil {
		t.Fatalf("identity.Registry.Issue: %v", err)
	}
	genController := generation.New()
	genController.RegisterInstance("inst-1")

	chained := hostendpoint.Chain(srv, hostendpoint.RegistryResolver{Registry: registry}, genController)
	httpSrv := httptest.NewServer(chained)
	t.Cleanup(httpSrv.Close)

	return httpSrv.URL, tok
}

// newFakeContractFixture configures hostclienttest.Server with the same
// fixture values newRealContractFixture seeds into the real server's
// querier, so the two sides are asked to answer the same questions.
func newFakeContractFixture(t *testing.T) *hostclienttest.Server {
	t.Helper()
	return hostclienttest.NewServer(t,
		hostclienttest.WithInstanceConfigJSON(`{"channel":"#ops"}`),
		hostclienttest.WithRunContext("call-1", hostclienttest.RunContext{
			RunID: "run-1", PolicyID: "pol-1", StartedAt: "2026-08-23T10:00:00Z", StepIndex: 3,
		}),
		hostclienttest.WithRunHistory([]hostclienttest.RunSummary{
			{RunID: "run-1", PolicyID: "pol-1", Status: "complete", StartedAt: "a", FinishedAt: "b"},
		}),
		hostclienttest.WithUserDirectory([]hostclienttest.UserEntry{
			{UserID: "u1", Username: "alice", Role: "operator"},
		}),
		hostclienttest.WithAuthorizedActor("U123", "user-1"),
		hostclienttest.WithIdentityBinder(func(_, _ string) (bool, string) { return true, "" }),
		hostclienttest.WithUserConfig("U1", `{"delivery":"direct"}`),
	)
}

func strPtr(s string) *string { return &s }

// assertSameOutcome compares one real/fake call pair: both must succeed with
// byte-identical JSON results, or both must fail with the same error code
// (HostError.Code for an isError result, JSONRPCError.Code for a transport
// fault).
func assertSameOutcome(t *testing.T, name string, realResult any, realErr error, fakeResult any, fakeErr error) {
	t.Helper()
	if (realErr == nil) != (fakeErr == nil) {
		t.Fatalf("%s: real err=%v, fake err=%v", name, realErr, fakeErr)
	}
	if realErr != nil {
		var realHost, fakeHost *hostclient.HostError
		if errors.As(realErr, &realHost) && errors.As(fakeErr, &fakeHost) {
			if realHost.Code != fakeHost.Code {
				t.Errorf("%s: HostError.Code mismatch: real=%q fake=%q", name, realHost.Code, fakeHost.Code)
			}
			return
		}
		var realRPC, fakeRPC *hostclient.JSONRPCError
		if errors.As(realErr, &realRPC) && errors.As(fakeErr, &fakeRPC) {
			if realRPC.Code != fakeRPC.Code {
				t.Errorf("%s: JSONRPCError.Code mismatch: real=%d fake=%d", name, realRPC.Code, fakeRPC.Code)
			}
			return
		}
		t.Errorf("%s: error type mismatch: real=%T(%v) fake=%T(%v)", name, realErr, realErr, fakeErr, fakeErr)
		return
	}
	realJSON, err := json.Marshal(realResult)
	if err != nil {
		t.Fatalf("%s: marshal real result: %v", name, err)
	}
	fakeJSON, err := json.Marshal(fakeResult)
	if err != nil {
		t.Fatalf("%s: marshal fake result: %v", name, err)
	}
	if string(realJSON) != string(fakeJSON) {
		t.Errorf("%s: result mismatch:\n real=%s\n fake=%s", name, realJSON, fakeJSON)
	}
}

// TestHostClientTest_MatchesRealServer drives every hostclient call this
// fixture configures against both the real hostendpoint.Server and
// hostclienttest.Server, and asserts identical outcomes.
func TestHostClientTest_MatchesRealServer(t *testing.T) {
	realURL, realToken := newRealContractFixture(t)
	realClient, err := hostclient.New(hostclient.WithBaseURL(realURL), hostclient.WithToken(realToken))
	if err != nil {
		t.Fatalf("hostclient.New (real): %v", err)
	}

	fakeSrv := newFakeContractFixture(t)
	fakeClient, err := hostclient.New(hostclient.WithBaseURL(fakeSrv.URL()), hostclient.WithToken(fakeSrv.Token()))
	if err != nil {
		t.Fatalf("hostclient.New (fake): %v", err)
	}

	ctx := context.Background()

	t.Run("server/discover", func(t *testing.T) {
		realOut, realErr := realClient.Discover(ctx)
		fakeOut, fakeErr := fakeClient.Discover(ctx)
		assertSameOutcome(t, "server/discover", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/get_instance_config", func(t *testing.T) {
		realOut, realErr := realClient.GetInstanceConfig(ctx)
		fakeOut, fakeErr := fakeClient.GetInstanceConfig(ctx)
		assertSameOutcome(t, "host/get_instance_config", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/get_credentials", func(t *testing.T) {
		realOut, realErr := realClient.GetCredentials(ctx)
		fakeOut, fakeErr := fakeClient.GetCredentials(ctx)
		assertSameOutcome(t, "host/get_credentials", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/get_run_context resolves an in-flight call", func(t *testing.T) {
		callCtx := hostclient.WithCallID(ctx, "call-1")
		realOut, realErr := realClient.GetRunContext(callCtx)
		fakeOut, fakeErr := fakeClient.GetRunContext(callCtx)
		assertSameOutcome(t, "host/get_run_context", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/get_run_context fails precondition with no call id", func(t *testing.T) {
		realOut, realErr := realClient.GetRunContext(ctx)
		fakeOut, fakeErr := fakeClient.GetRunContext(ctx)
		assertSameOutcome(t, "host/get_run_context (no call id)", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/emit_metric", func(t *testing.T) {
		req := hostclient.EmitMetricRequest{Name: "probe", Value: 1.5, Labels: map[string]string{"queue": "a"}}
		realOut, realErr := realClient.EmitMetric(ctx, req)
		fakeOut, fakeErr := fakeClient.EmitMetric(ctx, req)
		assertSameOutcome(t, "host/emit_metric", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/emit_metric rejects an empty name identically", func(t *testing.T) {
		req := hostclient.EmitMetricRequest{Name: "", Value: 1}
		realOut, realErr := realClient.EmitMetric(ctx, req)
		fakeOut, fakeErr := fakeClient.EmitMetric(ctx, req)
		assertSameOutcome(t, "host/emit_metric (empty name)", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/emit_metric rejects a gleipnir_plugin_-prefixed name identically", func(t *testing.T) {
		req := hostclient.EmitMetricRequest{Name: "gleipnir_plugin_probe", Value: 1}
		realOut, realErr := realClient.EmitMetric(ctx, req)
		fakeOut, fakeErr := fakeClient.EmitMetric(ctx, req)
		assertSameOutcome(t, "host/emit_metric (invalid name)", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/log", func(t *testing.T) {
		req := hostclient.LogRequest{Level: hostclient.LogLevelInfo, Msg: "hello from the contract test"}
		realOut, realErr := realClient.Log(ctx, req)
		fakeOut, fakeErr := fakeClient.Log(ctx, req)
		assertSameOutcome(t, "host/log", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/log rejects an oversize msg identically", func(t *testing.T) {
		req := hostclient.LogRequest{Level: hostclient.LogLevelInfo, Msg: strings.Repeat("x", 4*1024+1)}
		realOut, realErr := realClient.Log(ctx, req)
		fakeOut, fakeErr := fakeClient.Log(ctx, req)
		assertSameOutcome(t, "host/log (oversize msg)", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/set_health_state applies the worsen-only rule identically", func(t *testing.T) {
		first := hostclient.SetHealthStateRequest{Profile: hostclient.ProfileToolProvider, State: hostclient.HealthStateUnhealthy}
		realFirst, realErr1 := realClient.SetHealthState(ctx, first)
		fakeFirst, fakeErr1 := fakeClient.SetHealthState(ctx, first)
		assertSameOutcome(t, "host/set_health_state (first report)", realFirst, realErr1, fakeFirst, fakeErr1)

		second := hostclient.SetHealthStateRequest{Profile: hostclient.ProfileToolProvider, State: hostclient.HealthStateHealthy}
		realSecond, realErr2 := realClient.SetHealthState(ctx, second)
		fakeSecond, fakeErr2 := fakeClient.SetHealthState(ctx, second)
		assertSameOutcome(t, "host/set_health_state (improvement report)", realSecond, realErr2, fakeSecond, fakeErr2)
	})

	t.Run("host/run_history_read", func(t *testing.T) {
		req := hostclient.RunHistoryReadRequest{PolicyID: "pol-1"}
		realOut, realErr := realClient.RunHistoryRead(ctx, req)
		fakeOut, fakeErr := fakeClient.RunHistoryRead(ctx, req)
		assertSameOutcome(t, "host/run_history_read", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/user_directory_read", func(t *testing.T) {
		req := hostclient.UserDirectoryReadRequest{RoleFilter: "operator"}
		realOut, realErr := realClient.UserDirectoryRead(ctx, req)
		fakeOut, fakeErr := fakeClient.UserDirectoryRead(ctx, req)
		assertSameOutcome(t, "host/user_directory_read", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/user_directory_read rejects an unknown role identically", func(t *testing.T) {
		req := hostclient.UserDirectoryReadRequest{RoleFilter: "bogus"}
		realOut, realErr := realClient.UserDirectoryRead(ctx, req)
		fakeOut, fakeErr := fakeClient.UserDirectoryRead(ctx, req)
		assertSameOutcome(t, "host/user_directory_read (bad role)", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/authorize_actor authorized", func(t *testing.T) {
		req := hostclient.AuthorizeActorRequest{RequestID: "req-1", ActorExternalID: "U123"}
		realOut, realErr := realClient.AuthorizeActor(ctx, req)
		fakeOut, fakeErr := fakeClient.AuthorizeActor(ctx, req)
		assertSameOutcome(t, "host/authorize_actor (authorized)", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/authorize_actor unauthorized is a non-error result on both", func(t *testing.T) {
		req := hostclient.AuthorizeActorRequest{RequestID: "req-1", ActorExternalID: "U999"}
		realOut, realErr := realClient.AuthorizeActor(ctx, req)
		fakeOut, fakeErr := fakeClient.AuthorizeActor(ctx, req)
		if realErr != nil || fakeErr != nil {
			t.Fatalf("unexpected error: real=%v fake=%v", realErr, fakeErr)
		}
		if realOut.Authorized || fakeOut.Authorized {
			t.Errorf("real=%+v fake=%+v, want both authorized=false", realOut, fakeOut)
		}
	})

	t.Run("host/submit_identity_proof", func(t *testing.T) {
		req := hostclient.SubmitIdentityProofRequest{ExternalUserID: "U1", Code: "123456"}
		realOut, realErr := realClient.SubmitIdentityProof(ctx, req)
		fakeOut, fakeErr := fakeClient.SubmitIdentityProof(ctx, req)
		assertSameOutcome(t, "host/submit_identity_proof", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/get_user_config", func(t *testing.T) {
		req := hostclient.GetUserConfigRequest{ExternalUserID: "U1"}
		realOut, realErr := realClient.GetUserConfig(ctx, req)
		fakeOut, fakeErr := fakeClient.GetUserConfig(ctx, req)
		assertSameOutcome(t, "host/get_user_config", realOut, realErr, fakeOut, fakeErr)
	})

	t.Run("host/get_user_config defaults an unconfigured user to an empty object on both", func(t *testing.T) {
		req := hostclient.GetUserConfigRequest{ExternalUserID: "U-unknown"}
		realOut, realErr := realClient.GetUserConfig(ctx, req)
		fakeOut, fakeErr := fakeClient.GetUserConfig(ctx, req)
		assertSameOutcome(t, "host/get_user_config (unconfigured)", realOut, realErr, fakeOut, fakeErr)
	})
}

// rawJSONRPCCall posts a bare JSON-RPC request, bypassing hostclient, so the
// two transport-fault DoD assertions (unknown method, bad token) can be
// checked directly against both endpoints without hostclient's typed
// wrappers standing in the way.
func rawJSONRPCCall(t *testing.T, baseURL, token, method string) *http.Response {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{}}`
	req, err := http.NewRequest(http.MethodPost, baseURL, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestHostClientTest_TransportFaultsMatchReal is the DoD's two structural
// assertions (unknown method -32601, missing/bad token a §3 auth error),
// checked identically against the real server and the fake.
func TestHostClientTest_TransportFaultsMatchReal(t *testing.T) {
	realURL, realToken := newRealContractFixture(t)
	fakeSrv := newFakeContractFixture(t)

	t.Run("unknown method is -32601 on both", func(t *testing.T) {
		realResp := rawJSONRPCCall(t, realURL, realToken, "initialize")
		fakeResp := rawJSONRPCCall(t, fakeSrv.URL(), fakeSrv.Token(), "initialize")

		var realEnv, fakeEnv struct {
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.NewDecoder(realResp.Body).Decode(&realEnv); err != nil {
			t.Fatalf("decode real: %v", err)
		}
		if err := json.NewDecoder(fakeResp.Body).Decode(&fakeEnv); err != nil {
			t.Fatalf("decode fake: %v", err)
		}
		if realEnv.Error == nil || fakeEnv.Error == nil || realEnv.Error.Code != fakeEnv.Error.Code || realEnv.Error.Code != -32601 {
			t.Errorf("real=%+v fake=%+v, want both code -32601", realEnv.Error, fakeEnv.Error)
		}
	})

	t.Run("missing token is a 401 on both", func(t *testing.T) {
		realResp := rawJSONRPCCall(t, realURL, "", "server/discover")
		fakeResp := rawJSONRPCCall(t, fakeSrv.URL(), "", "server/discover")
		if realResp.StatusCode != http.StatusUnauthorized || fakeResp.StatusCode != http.StatusUnauthorized {
			t.Errorf("real status=%d fake status=%d, want both 401", realResp.StatusCode, fakeResp.StatusCode)
		}
	})

	t.Run("bad token is a 401 on both", func(t *testing.T) {
		realResp := rawJSONRPCCall(t, realURL, "not-a-real-token", "server/discover")
		fakeResp := rawJSONRPCCall(t, fakeSrv.URL(), "not-a-real-token", "server/discover")
		if realResp.StatusCode != http.StatusUnauthorized || fakeResp.StatusCode != http.StatusUnauthorized {
			t.Errorf("real status=%d fake status=%d, want both 401", realResp.StatusCode, fakeResp.StatusCode)
		}
	})
}
