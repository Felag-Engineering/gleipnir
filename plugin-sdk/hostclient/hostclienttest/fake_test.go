package hostclienttest_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient/hostclienttest"
)

func newClient(t *testing.T, srv *hostclienttest.Server) *hostclient.Client {
	t.Helper()
	c, err := hostclient.New(hostclient.WithBaseURL(srv.URL()), hostclient.WithToken(srv.Token()))
	if err != nil {
		t.Fatalf("hostclient.New: %v", err)
	}
	return c
}

func TestDiscover(t *testing.T) {
	srv := hostclienttest.NewServer(t)
	client := newClient(t, srv)

	result, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(result.SupportedVersions) != 1 || result.SupportedVersions[0] != "2026-07-28" {
		t.Errorf("SupportedVersions = %v", result.SupportedVersions)
	}
	if result.ServerName != hostclienttest.ServerName || result.ServerVersion != hostclienttest.Version {
		t.Errorf("serverInfo = %s/%s, want %s/%s", result.ServerName, result.ServerVersion, hostclienttest.ServerName, hostclienttest.Version)
	}
}

func TestNewServer_WiresEnvironment(t *testing.T) {
	srv := hostclienttest.NewServer(t)

	// hostclient.New with no options must pick up the environment NewServer
	// set, the same way a real plugin subprocess does at spawn time.
	client, err := hostclient.New()
	if err != nil {
		t.Fatalf("hostclient.New: %v", err)
	}
	if _, err := client.GetInstanceConfig(context.Background()); err != nil {
		t.Fatalf("GetInstanceConfig: %v", err)
	}
	_ = srv
}

func TestTier1_RoundTrip(t *testing.T) {
	srv := hostclienttest.NewServer(t,
		hostclienttest.WithInstanceConfigJSON(`{"channel":"#ops"}`),
		hostclienttest.WithCredentialsJSON("shh"),
		hostclienttest.WithRunContext("call-1", hostclienttest.RunContext{
			RunID: "run-1", PolicyID: "pol-1", StartedAt: "2026-08-23T10:00:00Z", StepIndex: 3,
		}),
	)
	client := newClient(t, srv)
	ctx := context.Background()

	t.Run("GetInstanceConfig", func(t *testing.T) {
		out, err := client.GetInstanceConfig(ctx)
		if err != nil {
			t.Fatalf("GetInstanceConfig: %v", err)
		}
		if out.ConfigJSON != `{"channel":"#ops"}` {
			t.Errorf("ConfigJSON = %q", out.ConfigJSON)
		}
	})

	t.Run("GetCredentials", func(t *testing.T) {
		out, err := client.GetCredentials(ctx)
		if err != nil {
			t.Fatalf("GetCredentials: %v", err)
		}
		if out.CredentialsJSON != "shh" {
			t.Errorf("CredentialsJSON = %q", out.CredentialsJSON)
		}
	})

	t.Run("GetRunContext resolves a configured call id", func(t *testing.T) {
		out, err := client.GetRunContext(hostclient.WithCallID(ctx, "call-1"))
		if err != nil {
			t.Fatalf("GetRunContext: %v", err)
		}
		if out.RunID != "run-1" || out.PolicyID != "pol-1" || out.StepIndex != 3 {
			t.Errorf("result = %+v", out)
		}
	})

	t.Run("GetRunContext fails precondition with no call id", func(t *testing.T) {
		_, err := client.GetRunContext(ctx)
		var hostErr *hostclient.HostError
		if !errors.As(err, &hostErr) || hostErr.Code != "failed_precondition" {
			t.Fatalf("err = %v, want failed_precondition HostError", err)
		}
	})

	t.Run("GetRunContext fails precondition with an unknown call id", func(t *testing.T) {
		_, err := client.GetRunContext(hostclient.WithCallID(ctx, "no-such-call"))
		var hostErr *hostclient.HostError
		if !errors.As(err, &hostErr) || hostErr.Code != "failed_precondition" {
			t.Fatalf("err = %v, want failed_precondition HostError", err)
		}
	})

	t.Run("EmitMetric", func(t *testing.T) {
		out, err := client.EmitMetric(ctx, hostclient.EmitMetricRequest{Name: "probe", Value: 1.5, Labels: map[string]string{"queue": "a"}})
		if err != nil {
			t.Fatalf("EmitMetric: %v", err)
		}
		if !out.OK {
			t.Error("OK = false")
		}
		metrics := srv.Metrics()
		if len(metrics) != 1 || metrics[0].Name != "probe" || metrics[0].Value != 1.5 || metrics[0].Labels["queue"] != "a" {
			t.Errorf("Metrics() = %+v", metrics)
		}
	})

	t.Run("EmitMetric rejects an empty or prefixed name with invalid_metric_name", func(t *testing.T) {
		for _, name := range []string{"", "gleipnir_plugin_probe"} {
			_, err := client.EmitMetric(ctx, hostclient.EmitMetricRequest{Name: name, Value: 1})
			var hostErr *hostclient.HostError
			if !errors.As(err, &hostErr) || hostErr.Code != "invalid_metric_name" {
				t.Errorf("name %q: err = %v, want invalid_metric_name HostError", name, err)
			}
		}
	})

	t.Run("Log", func(t *testing.T) {
		out, err := client.Log(ctx, hostclient.LogRequest{Level: hostclient.LogLevelInfo, Msg: "hello"})
		if err != nil {
			t.Fatalf("Log: %v", err)
		}
		if !out.OK {
			t.Error("OK = false")
		}
		logs := srv.Logs()
		if len(logs) != 1 || logs[0].Msg != "hello" {
			t.Errorf("Logs() = %+v", logs)
		}
	})

	t.Run("SetHealthState applies the worsen-only rule", func(t *testing.T) {
		out, err := client.SetHealthState(ctx, hostclient.SetHealthStateRequest{
			Profile: hostclient.ProfileToolProvider, State: hostclient.HealthStateUnhealthy,
		})
		if err != nil || !out.OK || !out.Applied {
			t.Fatalf("first report: out=%+v err=%v", out, err)
		}
		out, err = client.SetHealthState(ctx, hostclient.SetHealthStateRequest{
			Profile: hostclient.ProfileToolProvider, State: hostclient.HealthStateHealthy,
		})
		if err != nil || !out.OK || out.Applied {
			t.Fatalf("improvement report: out=%+v err=%v, want applied=false", out, err)
		}
		reports := srv.HealthReports()
		if len(reports) != 2 {
			t.Fatalf("HealthReports() = %+v", reports)
		}
	})
}

func TestTier2_RoundTrip(t *testing.T) {
	srv := hostclienttest.NewServer(t,
		hostclienttest.WithRunHistory([]hostclienttest.RunSummary{
			{RunID: "run-1", PolicyID: "pol-1", Status: "complete", StartedAt: "a", FinishedAt: "b"},
			{RunID: "run-2", PolicyID: "pol-2", Status: "failed", StartedAt: "c", FinishedAt: "d"},
		}),
		hostclienttest.WithUserDirectory([]hostclienttest.UserEntry{
			{UserID: "u1", Username: "alice", Role: "operator"},
			{UserID: "u2", Username: "bob", Role: "admin"},
		}),
	)
	client := newClient(t, srv)
	ctx := context.Background()

	t.Run("RunHistoryRead narrows to the requested policy", func(t *testing.T) {
		out, err := client.RunHistoryRead(ctx, hostclient.RunHistoryReadRequest{PolicyID: "pol-2"})
		if err != nil {
			t.Fatalf("RunHistoryRead: %v", err)
		}
		if len(out.Runs) != 1 || out.Runs[0].RunID != "run-2" {
			t.Errorf("Runs = %+v", out.Runs)
		}
	})

	t.Run("UserDirectoryRead narrows to the requested role", func(t *testing.T) {
		out, err := client.UserDirectoryRead(ctx, hostclient.UserDirectoryReadRequest{RoleFilter: "admin"})
		if err != nil {
			t.Fatalf("UserDirectoryRead: %v", err)
		}
		if len(out.Users) != 1 || out.Users[0].Username != "bob" {
			t.Errorf("Users = %+v", out.Users)
		}
	})

	t.Run("UserDirectoryRead rejects an unknown role", func(t *testing.T) {
		_, err := client.UserDirectoryRead(ctx, hostclient.UserDirectoryReadRequest{RoleFilter: "bogus"})
		var hostErr *hostclient.HostError
		if !errors.As(err, &hostErr) || hostErr.Code != "invalid_argument" {
			t.Fatalf("err = %v, want invalid_argument HostError", err)
		}
	})
}

func TestAuthorizeActor_RoundTrip(t *testing.T) {
	srv := hostclienttest.NewServer(t, hostclienttest.WithAuthorizedActor("U123", "user-1"))
	client := newClient(t, srv)
	ctx := context.Background()

	t.Run("authorized actor", func(t *testing.T) {
		out, err := client.AuthorizeActor(ctx, hostclient.AuthorizeActorRequest{RequestID: "req-1", ActorExternalID: "U123"})
		if err != nil {
			t.Fatalf("AuthorizeActor: %v", err)
		}
		if !out.Authorized || out.UserID != "user-1" {
			t.Errorf("result = %+v", out)
		}
	})

	t.Run("unauthorized actor is a non-error result", func(t *testing.T) {
		out, err := client.AuthorizeActor(ctx, hostclient.AuthorizeActorRequest{RequestID: "req-1", ActorExternalID: "U999"})
		if err != nil {
			t.Fatalf("AuthorizeActor: %v", err)
		}
		if out.Authorized {
			t.Errorf("result = %+v, want authorized=false", out)
		}
	})
}

func TestAuthorizeActor_CustomPolicy(t *testing.T) {
	srv := hostclienttest.NewServer(t, hostclienttest.WithAuthorizePolicy(
		func(requestID, actorExternalID string) (bool, string) {
			return actorExternalID == "U1", "policy-user"
		},
	))
	client := newClient(t, srv)

	out, err := client.AuthorizeActor(context.Background(), hostclient.AuthorizeActorRequest{RequestID: "r", ActorExternalID: "U1"})
	if err != nil || !out.Authorized || out.UserID != "policy-user" {
		t.Fatalf("result = %+v, err = %v", out, err)
	}
}

func TestSubmitIdentityProof_RoundTrip(t *testing.T) {
	t.Run("no binder configured rejects with ReasonNoPendingLink", func(t *testing.T) {
		srv := hostclienttest.NewServer(t)
		client := newClient(t, srv)
		out, err := client.SubmitIdentityProof(context.Background(), hostclient.SubmitIdentityProofRequest{ExternalUserID: "U1", Code: "123456"})
		if err != nil {
			t.Fatalf("SubmitIdentityProof: %v", err)
		}
		if out.Accepted || out.Reason != hostclient.ReasonNoPendingLink {
			t.Errorf("result = %+v", out)
		}
	})

	t.Run("configured binder accepts", func(t *testing.T) {
		srv := hostclienttest.NewServer(t, hostclienttest.WithIdentityBinder(
			func(externalUserID, code string) (bool, string) {
				return code == "123456", ""
			},
		))
		client := newClient(t, srv)
		out, err := client.SubmitIdentityProof(context.Background(), hostclient.SubmitIdentityProofRequest{ExternalUserID: "U1", Code: "123456"})
		if err != nil {
			t.Fatalf("SubmitIdentityProof: %v", err)
		}
		if !out.Accepted {
			t.Errorf("result = %+v", out)
		}
	})
}

func TestGetUserConfig_RoundTrip(t *testing.T) {
	srv := hostclienttest.NewServer(t, hostclienttest.WithUserConfig("U1", `{"delivery":"direct"}`))
	client := newClient(t, srv)
	ctx := context.Background()

	t.Run("configured user", func(t *testing.T) {
		out, err := client.GetUserConfig(ctx, hostclient.GetUserConfigRequest{ExternalUserID: "U1"})
		if err != nil {
			t.Fatalf("GetUserConfig: %v", err)
		}
		if out.UserConfigJSON != `{"delivery":"direct"}` {
			t.Errorf("UserConfigJSON = %q", out.UserConfigJSON)
		}
	})

	t.Run("unconfigured user defaults to an empty object", func(t *testing.T) {
		out, err := client.GetUserConfig(ctx, hostclient.GetUserConfigRequest{ExternalUserID: "U-unknown"})
		if err != nil {
			t.Fatalf("GetUserConfig: %v", err)
		}
		if out.UserConfigJSON != "{}" {
			t.Errorf("UserConfigJSON = %q", out.UserConfigJSON)
		}
	})
}

// TestGetRunContext_AmbiguousCallIDIsTreatedAsAbsent proves a multi-valued
// Gleipnir-Call-Id header is treated as absent, matching
// hostendpoint.WithCallID exactly (a single hostclient call can never
// produce this shape, so it is driven with a raw request).
func TestGetRunContext_AmbiguousCallIDIsTreatedAsAbsent(t *testing.T) {
	srv := hostclienttest.NewServer(t, hostclienttest.WithRunContext("call-1", hostclienttest.RunContext{RunID: "run-1"}))

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"host/get_run_context","arguments":{}}}`
	req, err := http.NewRequest(http.MethodPost, srv.URL(), strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+srv.Token())
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "host/get_run_context")
	req.Header.Add("Gleipnir-Call-Id", "call-1")
	req.Header.Add("Gleipnir-Call-Id", "call-2")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	var env struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.Result.IsError || len(env.Result.Content) == 0 || !strings.HasPrefix(env.Result.Content[0].Text, "failed_precondition:") {
		t.Errorf("result = %+v, want a failed_precondition isError (ambiguous call id treated as absent)", env.Result)
	}
}

// TestUnknownMethod_MethodNotFound is the first DoD assertion: a JSON-RPC
// method outside server/discover, tools/list, and tools/call is -32601, the
// same rejection a legacy `initialize` handshake gets.
func TestUnknownMethod_MethodNotFound(t *testing.T) {
	srv := hostclienttest.NewServer(t)

	req, err := http.NewRequest(http.MethodPost, srv.URL(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+srv.Token())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	var env struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32601 {
		t.Errorf("error = %+v, want code -32601", env.Error)
	}
}

// TestAuth_MissingOrBadToken is the second DoD assertion: a missing or bad
// token is a §3 auth error (HTTP 401) before any handler runs.
func TestAuth_MissingOrBadToken(t *testing.T) {
	srv := hostclienttest.NewServer(t)

	t.Run("missing token", func(t *testing.T) {
		client, err := hostclient.New(hostclient.WithBaseURL(srv.URL()), hostclient.WithToken("placeholder"))
		if err != nil {
			t.Fatalf("hostclient.New: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, srv.URL(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		// Deliberately no Authorization header.
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
		_ = client
	})

	t.Run("bad token", func(t *testing.T) {
		client, err := hostclient.New(hostclient.WithBaseURL(srv.URL()), hostclient.WithToken("not-the-real-token"))
		if err != nil {
			t.Fatalf("hostclient.New: %v", err)
		}
		_, err = client.Discover(context.Background())
		if err == nil {
			t.Fatal("expected an error for an unknown token")
		}
	})
}

func TestWaitForCall(t *testing.T) {
	srv := hostclienttest.NewServer(t)
	client := newClient(t, srv)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := client.Log(context.Background(), hostclient.LogRequest{Level: "info", Msg: "async"}); err != nil {
			t.Errorf("Log: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	call, err := srv.WaitForCall(ctx, "host/log")
	if err != nil {
		t.Fatalf("WaitForCall: %v", err)
	}
	if call.Method != "host/log" {
		t.Errorf("Method = %q", call.Method)
	}
	<-done
}

func TestCalls_RecordsDiscoverAndToolsList(t *testing.T) {
	srv := hostclienttest.NewServer(t)
	client := newClient(t, srv)
	ctx := context.Background()

	if _, err := client.Discover(ctx); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	calls := srv.Calls()
	if len(calls) != 1 || calls[0].Method != "server/discover" {
		t.Errorf("Calls() = %+v", calls)
	}
}
