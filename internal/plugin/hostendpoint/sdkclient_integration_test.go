package hostendpoint_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	"github.com/felag-engineering/gleipnir/internal/plugin/pluginmetrics"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
)

// This file is the contract proof #882 asks for: the plugin-sdk client and
// the real hostendpoint.Server, driven through the real Chain middleware
// (identity.Registry-issued token, generation.Controller slot), round-trip
// over an actual HTTP connection. Everything here is a local fixture rather
// than a reuse of hostendpoint's in-package fakes — those are unexported,
// and this is deliberately an external test package (hostendpoint_test) so
// a wire-shape mismatch between client and server shows up the same way it
// would for a real plugin, not through in-package fixture access the SDK
// itself does not have.

// fakeIntegrationQuerier is a minimal in-memory implementation of the sqlc
// surfaces the mounted tool groups need (Tier1Querier + UserLinkQuerier).
// Mirrors tier1_test.go's fakeTier1Querier without importing it.
type fakeIntegrationQuerier struct {
	instances map[string]db.PluginInstance
	runs      map[string]db.Run
	steps     map[string]db.RunStep
	audits    []db.InsertPluginAuditEventParams
}

func (f *fakeIntegrationQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	inst, ok := f.instances[id]
	if !ok {
		return db.PluginInstance{}, sql.ErrNoRows
	}
	return inst, nil
}

func (f *fakeIntegrationQuerier) GetRun(_ context.Context, id string) (db.Run, error) {
	run, ok := f.runs[id]
	if !ok {
		return db.Run{}, sql.ErrNoRows
	}
	return run, nil
}

func (f *fakeIntegrationQuerier) GetLatestRunStep(_ context.Context, runID string) (db.RunStep, error) {
	step, ok := f.steps[runID]
	if !ok {
		return db.RunStep{}, sql.ErrNoRows
	}
	return step, nil
}

func (f *fakeIntegrationQuerier) InsertPluginAuditEvent(_ context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	f.audits = append(f.audits, arg)
	return db.PluginAuditEvent{}, nil
}

// fakeIntegrationCallResolver mirrors tier1_test.go's fakeCallResolver.
type fakeIntegrationCallResolver struct {
	calls map[string]dispatch.CallInfo
}

func (f *fakeIntegrationCallResolver) LookupCall(callID string) (dispatch.CallInfo, bool) {
	info, ok := f.calls[callID]
	return info, ok
}

// integrationFixture wires a real Server, mounted with the real Tier-1 +
// user-link tool groups, behind the real Chain middleware, on an httptest
// server — the same composition main.go builds in production, minus the DB.
type integrationFixture struct {
	baseURL string
	token   string
	querier *fakeIntegrationQuerier
}

func newIntegrationFixture(t *testing.T) *integrationFixture {
	t.Helper()

	q := &fakeIntegrationQuerier{
		instances: map[string]db.PluginInstance{
			"inst-1": {ID: "inst-1", PluginID: "plug-1", InstanceName: "sdk-test", ConfigJson: `{"channel":"#ops"}`},
		},
		runs: map[string]db.Run{
			"run-1": {ID: "run-1", StartedAt: "2026-08-23T10:00:00Z"},
		},
		steps: map[string]db.RunStep{
			"run-1": {StepNumber: 2},
		},
	}

	srv := hostendpoint.NewServer()
	srv.Register(hostendpoint.Tier1Tools(hostendpoint.Tier1Deps{
		Querier: q,
		Calls: &fakeIntegrationCallResolver{calls: map[string]dispatch.CallInfo{
			"call-1": {RunID: "run-1", PolicyID: "pol-1", InstanceName: "sdk-test"},
		}},
		Metrics: pluginmetrics.New(),
		Health:  caphealth.NewRegistry(),
	})...)
	srv.Register(hostendpoint.UserLinkTools(hostendpoint.UserLinkDeps{Querier: q})...)

	registry := identity.New()
	token, err := registry.Issue("inst-1")
	if err != nil {
		t.Fatalf("identity.Registry.Issue: %v", err)
	}
	genController := generation.New()
	genController.RegisterInstance("inst-1")

	chained := hostendpoint.Chain(srv, hostendpoint.RegistryResolver{Registry: registry}, genController)
	httpSrv := httptest.NewServer(chained)
	t.Cleanup(httpSrv.Close)

	return &integrationFixture{baseURL: httpSrv.URL, token: token, querier: q}
}

func (f *integrationFixture) newClient(t *testing.T) *hostclient.Client {
	t.Helper()
	c, err := hostclient.New(hostclient.WithBaseURL(f.baseURL), hostclient.WithToken(f.token))
	if err != nil {
		t.Fatalf("hostclient.New: %v", err)
	}
	return c
}

// TestSDKClient_RoundTrip is the DoD proof: the SDK client and the real
// server agree on the wire contract for server/discover, a Tier-1 tool call,
// and the isError shape, all over one real HTTP connection.
func TestSDKClient_RoundTrip(t *testing.T) {
	fixture := newIntegrationFixture(t)
	client := fixture.newClient(t)

	t.Run("server/discover", func(t *testing.T) {
		result, err := client.Discover(context.Background())
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(result.SupportedVersions) != 1 || result.SupportedVersions[0] != "2026-07-28" {
			t.Errorf("SupportedVersions = %v", result.SupportedVersions)
		}
		if result.ServerName != hostendpoint.ServerName {
			t.Errorf("ServerName = %q, want %q", result.ServerName, hostendpoint.ServerName)
		}
		if result.ServerVersion != hostendpoint.Version {
			t.Errorf("ServerVersion = %q, want %q", result.ServerVersion, hostendpoint.Version)
		}
	})

	t.Run("host/log", func(t *testing.T) {
		out, err := client.Log(context.Background(), hostclient.LogRequest{
			Level: hostclient.LogLevelInfo,
			Msg:   "hello from the SDK client",
		})
		if err != nil {
			t.Fatalf("Log: %v", err)
		}
		if !out.OK {
			t.Error("OK = false")
		}
	})

	t.Run("host/get_instance_config", func(t *testing.T) {
		out, err := client.GetInstanceConfig(context.Background())
		if err != nil {
			t.Fatalf("GetInstanceConfig: %v", err)
		}
		if out.ConfigJSON != `{"channel":"#ops"}` {
			t.Errorf("ConfigJSON = %q", out.ConfigJSON)
		}
	})

	t.Run("host/get_run_context resolves an in-flight call via WithCallID", func(t *testing.T) {
		ctx := hostclient.WithCallID(context.Background(), "call-1")
		out, err := client.GetRunContext(ctx)
		if err != nil {
			t.Fatalf("GetRunContext: %v", err)
		}
		if out.RunID != "run-1" || out.PolicyID != "pol-1" {
			t.Errorf("result = %+v", out)
		}
		if out.StepIndex != 3 {
			t.Errorf("StepIndex = %d, want 3 (latest step 2 + 1)", out.StepIndex)
		}
	})

	t.Run("isError maps to a HostError with the server's stable code", func(t *testing.T) {
		// No call id attached: the server's failed_precondition path.
		_, err := client.GetRunContext(context.Background())
		if err == nil {
			t.Fatal("expected an error with no call id in context")
		}
		var hostErr *hostclient.HostError
		if !errors.As(err, &hostErr) {
			t.Fatalf("err = %v (%T), want *hostclient.HostError", err, err)
		}
		if hostErr.Code != "failed_precondition" {
			t.Errorf("Code = %q, want failed_precondition", hostErr.Code)
		}
	})
}

// TestSDKClient_UnknownToken proves the client surfaces a rejection when
// RequireInstanceToken refuses the request before any handler runs, rather
// than silently treating the 401 as a decode failure with no useful message.
func TestSDKClient_UnknownToken(t *testing.T) {
	fixture := newIntegrationFixture(t)
	client, err := hostclient.New(hostclient.WithBaseURL(fixture.baseURL), hostclient.WithToken("not-a-real-token"))
	if err != nil {
		t.Fatalf("hostclient.New: %v", err)
	}

	_, err = client.Discover(context.Background())
	if err == nil {
		t.Fatal("expected an error for an unknown instance token")
	}
}
