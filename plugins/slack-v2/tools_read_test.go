package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

const testCredentials = `{"token":{"access_token":"xoxb-test"}}`

// fakeHost stands in for the host endpoint: it serves credentials and records
// health reports.
type fakeHost struct {
	mu          sync.Mutex
	credentials string
	healthCalls []hostclient.SetHealthStateRequest
}

func (f *fakeHost) GetCredentials(context.Context) (*hostclient.GetCredentialsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &hostclient.GetCredentialsResponse{CredentialsJSON: f.credentials}, nil
}

func (f *fakeHost) SetHealthState(_ context.Context, req hostclient.SetHealthStateRequest) (*hostclient.SetHealthStateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthCalls = append(f.healthCalls, req)
	return &hostclient.SetHealthStateResponse{OK: true, Applied: true}, nil
}

func (f *fakeHost) reports() []hostclient.SetHealthStateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hostclient.SetHealthStateRequest(nil), f.healthCalls...)
}

// fakeSlack serves canned Slack Web API responses keyed by method name
// ("conversations.list"), recording the token it was called with.
type fakeSlack struct {
	*httptest.Server
	mu        sync.Mutex
	responses map[string]string
	tokens    []string
	forms     map[string]url.Values // last form each method received
}

func newFakeSlack(t *testing.T, responses map[string]string) *fakeSlack {
	t.Helper()
	f := &fakeSlack{responses: responses, forms: map[string]url.Values{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/api/")
		f.mu.Lock()
		f.tokens = append(f.tokens, r.FormValue("token"))
		f.forms[method] = r.PostForm
		body, ok := f.responses[method]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.Close)
	return f
}

// form returns the last form Slack received for method.
func (f *fakeSlack) form(method string) url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forms[method]
}

type toolResult struct {
	IsError bool
	Text    string
}

// callTool drives tools/call through the real mcpserver handler.
func callTool(t *testing.T, srv http.Handler, name string, args map[string]any) toolResult {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	req.Header.Set("MCP-Protocol-Version", mcpserver.ProtocolVersion)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", name)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var env struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	if env.Error != nil || len(env.Result.Content) == 0 {
		t.Fatalf("transport-level failure: %s", w.Body.String())
	}
	return toolResult{IsError: env.Result.IsError, Text: env.Result.Content[0].Text}
}

func newTestServer(t *testing.T, host *fakeHost, slackURL string) http.Handler {
	t.Helper()
	srv, err := newServer(host, slackURL)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return srv
}

func TestReadToolsHappyPath(t *testing.T) {
	slackAPI := newFakeSlack(t, map[string]string{
		"conversations.list":    `{"ok":true,"channels":[{"id":"C1","name":"general","is_private":false,"num_members":3}],"response_metadata":{"next_cursor":"next"}}`,
		"conversations.history": `{"ok":true,"messages":[{"text":"hi","user":"U1","ts":"1.1"}],"has_more":true,"response_metadata":{"next_cursor":"h2"}}`,
		"conversations.replies": `{"ok":true,"messages":[{"text":"root","user":"U1","ts":"1.1","thread_ts":"1.1"}],"has_more":false}`,
		"users.info":            `{"ok":true,"user":{"id":"U1","name":"ada","real_name":"Ada L","tz":"Europe/London"}}`,
	})
	host := &fakeHost{credentials: testCredentials}
	srv := newTestServer(t, host, slackAPI.URL+"/api")

	tests := []struct {
		tool string
		args map[string]any
		want []string
	}{
		{toolListChannels, map[string]any{}, []string{`"id":"C1"`, `"name":"general"`, `"next_cursor":"next"`}},
		{toolReadHistory, map[string]any{"channel": "C1"}, []string{`"text":"hi"`, `"has_more":true`, `"next_cursor":"h2"`}},
		{toolReadThread, map[string]any{"channel": "C1", "ts": "1.1"}, []string{`"text":"root"`, `"thread_ts":"1.1"`}},
		{toolLookupUser, map[string]any{"user": "U1"}, []string{`"real_name":"Ada L"`, `"tz":"Europe/London"`}},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			res := callTool(t, srv, tc.tool, tc.args)
			if res.IsError {
				t.Fatalf("isError result: %s", res.Text)
			}
			for _, want := range tc.want {
				if !strings.Contains(res.Text, want) {
					t.Errorf("result %s missing %s", res.Text, want)
				}
			}
		})
	}
	if got := host.reports(); len(got) != 0 {
		t.Errorf("healthy calls reported health: %+v", got)
	}
}

func TestSlackAPIErrorIsAToolError(t *testing.T) {
	slackAPI := newFakeSlack(t, map[string]string{
		"conversations.history": `{"ok":false,"error":"channel_not_found"}`,
	})
	host := &fakeHost{credentials: testCredentials}
	srv := newTestServer(t, host, slackAPI.URL+"/api/")

	res := callTool(t, srv, toolReadHistory, map[string]any{"channel": "C404"})
	if !res.IsError || !strings.Contains(res.Text, "channel_not_found") {
		t.Errorf("result = %+v, want isError naming channel_not_found", res)
	}
	if got := host.reports(); len(got) != 0 {
		t.Errorf("a non-scope error reported health: %+v", got)
	}
}

func TestInvalidArgumentsIsAToolError(t *testing.T) {
	host := &fakeHost{credentials: testCredentials}
	srv := newTestServer(t, host, "http://unused.invalid/api/")

	res := callTool(t, srv, toolLookupUser, map[string]any{"user": 42})
	if !res.IsError || !strings.Contains(res.Text, "invalid arguments") {
		t.Errorf("result = %+v, want isError about invalid arguments", res)
	}
}

func TestNoCredentialsIsAToolErrorWithoutHealthReport(t *testing.T) {
	for _, creds := range []string{"", "{}", `{"token":{"access_token":""}}`} {
		host := &fakeHost{credentials: creds}
		srv := newTestServer(t, host, "http://unused.invalid/api/")

		res := callTool(t, srv, toolListChannels, map[string]any{})
		if !res.IsError || !strings.Contains(res.Text, "no Slack credentials configured") {
			t.Errorf("credentials %q: result = %+v, want isError about missing credentials", creds, res)
		}
		if got := host.reports(); len(got) != 0 {
			t.Errorf("credentials %q: reported health: %+v", creds, got)
		}
	}
}

func TestMissingScopeReportsOnlyThatCapability(t *testing.T) {
	tests := []struct {
		name      string
		slackErr  string
		wantScope string
	}{
		{"missing_scope", "missing_scope", "channels:history"},
		{"not_allowed_token_type", "not_allowed_token_type", "channels:history"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slackAPI := newFakeSlack(t, map[string]string{
				"conversations.history": `{"ok":false,"error":"` + tc.slackErr + `"}`,
				"users.info":            `{"ok":true,"user":{"id":"U1","name":"ada"}}`,
			})
			host := &fakeHost{credentials: testCredentials}
			srv := newTestServer(t, host, slackAPI.URL+"/api/")

			res := callTool(t, srv, toolReadHistory, map[string]any{"channel": "C1"})
			if !res.IsError || !strings.Contains(res.Text, tc.wantScope) {
				t.Errorf("result = %+v, want isError naming %s", res, tc.wantScope)
			}

			// A second failing call must not report again.
			callTool(t, srv, toolReadHistory, map[string]any{"channel": "C1"})

			reports := host.reports()
			if len(reports) != 1 {
				t.Fatalf("SetHealthState calls = %d (%+v), want exactly 1", len(reports), reports)
			}
			got := reports[0]
			if got.Profile != hostclient.ProfileToolProvider || got.Capability != toolReadHistory || got.State != hostclient.HealthStateUnhealthy {
				t.Errorf("report = %+v, want tool_provider/%s unhealthy", got, toolReadHistory)
			}
			if !strings.Contains(got.Detail, tc.wantScope) || !strings.Contains(got.Detail, tc.slackErr) {
				t.Errorf("detail %q should name %s and %s", got.Detail, tc.slackErr, tc.wantScope)
			}

			// The other tools stay callable.
			if other := callTool(t, srv, toolLookupUser, map[string]any{"user": "U1"}); other.IsError {
				t.Errorf("lookup_user failed after read_history lost its scope: %s", other.Text)
			}
			if got := host.reports(); len(got) != 1 {
				t.Errorf("SetHealthState calls after healthy tool = %d, want still 1", len(got))
			}
		})
	}
}

func TestRotatedTokenRebuildsTheClient(t *testing.T) {
	slackAPI := newFakeSlack(t, map[string]string{
		"users.info": `{"ok":true,"user":{"id":"U1","name":"ada"}}`,
	})
	host := &fakeHost{credentials: `{"token":{"access_token":"xoxb-one"}}`}
	srv := newTestServer(t, host, slackAPI.URL+"/api/")

	callTool(t, srv, toolLookupUser, map[string]any{"user": "U1"})
	callTool(t, srv, toolLookupUser, map[string]any{"user": "U1"})
	host.mu.Lock()
	host.credentials = `{"token":{"access_token":"xoxb-two"}}`
	host.mu.Unlock()
	callTool(t, srv, toolLookupUser, map[string]any{"user": "U1"})

	slackAPI.mu.Lock()
	defer slackAPI.mu.Unlock()
	want := []string{"xoxb-one", "xoxb-one", "xoxb-two"}
	if strings.Join(slackAPI.tokens, "|") != strings.Join(want, "|") {
		t.Errorf("Slack saw tokens %v, want %v", slackAPI.tokens, want)
	}
}
