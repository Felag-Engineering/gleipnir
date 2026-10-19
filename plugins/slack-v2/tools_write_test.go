package main

import (
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
)

func TestAllNineV1ToolsAreRegistered(t *testing.T) {
	want := []string{
		"list_channels", "read_history", "read_thread", "lookup_user",
		"post_message", "update_message", "delete_message", "react", "set_topic",
	}
	p := &plugin{}
	got := map[string]bool{}
	for _, tool := range p.allTools() {
		got[tool.Name] = true
	}
	if len(got) != len(want) {
		t.Errorf("registered %d tools, want %d", len(got), len(want))
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tool %q is not registered", name)
		}
	}
}

func TestWriteToolsHappyPath(t *testing.T) {
	slackAPI := newFakeSlack(t, map[string]string{
		"chat.postMessage":       `{"ok":true,"channel":"C1","ts":"2.2"}`,
		"chat.update":            `{"ok":true,"channel":"C1","ts":"1.1","text":"edited"}`,
		"chat.delete":            `{"ok":true,"channel":"C1","ts":"1.1"}`,
		"reactions.add":          `{"ok":true}`,
		"conversations.setTopic": `{"ok":true,"channel":{"id":"C1","topic":{"value":"new topic"}}}`,
	})
	host := &fakeHost{credentials: testCredentials}
	srv := newTestServer(t, host, slackAPI.URL+"/api/")

	blocks := []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "hello"}}}
	tests := []struct {
		name       string
		tool       string
		args       map[string]any
		wantResult []string
		method     string
		wantForm   map[string]string
	}{
		{
			name: "post text in thread", tool: toolPostMessage,
			args:       map[string]any{"channel": "C1", "text": "hi", "thread_ts": "1.1"},
			wantResult: []string{`"channel":"C1"`, `"ts":"2.2"`},
			method:     "chat.postMessage",
			wantForm:   map[string]string{"channel": "C1", "text": "hi", "thread_ts": "1.1"},
		},
		{
			name: "post blocks with fallback text", tool: toolPostMessage,
			args:       map[string]any{"channel": "C1", "text": "fallback", "blocks": blocks},
			wantResult: []string{`"ts":"2.2"`},
			method:     "chat.postMessage",
			wantForm:   map[string]string{"channel": "C1", "text": "fallback"},
		},
		{
			name: "update", tool: toolUpdateMessage,
			args:       map[string]any{"channel": "C1", "ts": "1.1", "text": "edited"},
			wantResult: []string{`"text":"edited"`, `"ts":"1.1"`},
			method:     "chat.update",
			wantForm:   map[string]string{"channel": "C1", "ts": "1.1", "text": "edited"},
		},
		{
			name: "delete", tool: toolDeleteMessage,
			args:       map[string]any{"channel": "C1", "ts": "1.1"},
			wantResult: []string{`"channel":"C1"`, `"ts":"1.1"`},
			method:     "chat.delete",
			wantForm:   map[string]string{"channel": "C1", "ts": "1.1"},
		},
		{
			name: "react", tool: toolReact,
			args:       map[string]any{"channel": "C1", "timestamp": "1.1", "name": "thumbsup"},
			wantResult: []string{`"ok":true`},
			method:     "reactions.add",
			wantForm:   map[string]string{"channel": "C1", "timestamp": "1.1", "name": "thumbsup"},
		},
		{
			name: "set topic", tool: toolSetTopic,
			args:       map[string]any{"channel": "C1", "topic": "new topic"},
			wantResult: []string{`"ok":true`, `"topic":"new topic"`},
			method:     "conversations.setTopic",
			wantForm:   map[string]string{"channel": "C1", "topic": "new topic"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := callTool(t, srv, tc.tool, tc.args)
			if res.IsError {
				t.Fatalf("isError result: %s", res.Text)
			}
			for _, want := range tc.wantResult {
				if !strings.Contains(res.Text, want) {
					t.Errorf("result %s missing %s", res.Text, want)
				}
			}
			form := slackAPI.form(tc.method)
			for k, want := range tc.wantForm {
				if got := form.Get(k); got != want {
					t.Errorf("%s form %s = %q, want %q (form %v)", tc.method, k, got, want, form)
				}
			}
		})
	}

	if got := slackAPI.form("chat.postMessage").Get("blocks"); got != "" {
		// The last postMessage call was the blocks one.
		if !strings.Contains(got, `"type":"section"`) || !strings.Contains(got, "hello") {
			t.Errorf("blocks sent to Slack = %s, want the Block Kit passthrough", got)
		}
	} else {
		t.Error("post_message did not send blocks to Slack")
	}
	if got := host.reports(); len(got) != 0 {
		t.Errorf("healthy calls reported health: %+v", got)
	}
}

func TestWriteToolSlackErrorsAreToolErrors(t *testing.T) {
	tests := []struct {
		tool     string
		args     map[string]any
		method   string
		slackErr string
	}{
		{toolPostMessage, map[string]any{"channel": "C404", "text": "x"}, "chat.postMessage", "channel_not_found"},
		{toolUpdateMessage, map[string]any{"channel": "C1", "ts": "1.1", "text": "x"}, "chat.update", "cant_update_message"},
		{toolDeleteMessage, map[string]any{"channel": "C1", "ts": "1.1"}, "chat.delete", "cant_delete_message"},
		{toolReact, map[string]any{"channel": "C1", "timestamp": "1.1", "name": "nope"}, "reactions.add", "invalid_name"},
		{toolSetTopic, map[string]any{"channel": "C404", "topic": "x"}, "conversations.setTopic", "channel_not_found"},
	}
	for _, tc := range tests {
		t.Run(tc.tool+"/"+tc.slackErr, func(t *testing.T) {
			slackAPI := newFakeSlack(t, map[string]string{tc.method: `{"ok":false,"error":"` + tc.slackErr + `"}`})
			host := &fakeHost{credentials: testCredentials}
			srv := newTestServer(t, host, slackAPI.URL+"/api/")

			res := callTool(t, srv, tc.tool, tc.args)
			if !res.IsError || !strings.Contains(res.Text, tc.slackErr) {
				t.Errorf("result = %+v, want isError naming %s", res, tc.slackErr)
			}
			if got := host.reports(); len(got) != 0 {
				t.Errorf("a non-scope error reported health: %+v", got)
			}
		})
	}
}

func TestWriteToolMissingScopeReportsOnlyThatCapability(t *testing.T) {
	tests := []struct {
		tool      string
		args      map[string]any
		method    string
		wantScope string
	}{
		{toolPostMessage, map[string]any{"channel": "C1", "text": "x"}, "chat.postMessage", "chat:write"},
		{toolUpdateMessage, map[string]any{"channel": "C1", "ts": "1.1", "text": "x"}, "chat.update", "chat:write"},
		{toolDeleteMessage, map[string]any{"channel": "C1", "ts": "1.1"}, "chat.delete", "chat:write"},
		{toolReact, map[string]any{"channel": "C1", "timestamp": "1.1", "name": "eyes"}, "reactions.add", "reactions:write"},
		{toolSetTopic, map[string]any{"channel": "C1", "topic": "x"}, "conversations.setTopic", "channels:manage"},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			slackAPI := newFakeSlack(t, map[string]string{
				tc.method:    `{"ok":false,"error":"missing_scope"}`,
				"users.info": `{"ok":true,"user":{"id":"U1","name":"ada"}}`,
			})
			host := &fakeHost{credentials: testCredentials}
			srv := newTestServer(t, host, slackAPI.URL+"/api/")

			res := callTool(t, srv, tc.tool, tc.args)
			if !res.IsError || !strings.Contains(res.Text, tc.wantScope) {
				t.Errorf("result = %+v, want isError naming %s", res, tc.wantScope)
			}
			reports := host.reports()
			if len(reports) != 1 {
				t.Fatalf("SetHealthState calls = %d (%+v), want exactly 1", len(reports), reports)
			}
			got := reports[0]
			if got.Profile != hostclient.ProfileToolProvider || got.Capability != tc.tool || got.State != hostclient.HealthStateUnhealthy {
				t.Errorf("report = %+v, want tool_provider/%s unhealthy", got, tc.tool)
			}
			if !strings.Contains(got.Detail, tc.wantScope) {
				t.Errorf("detail %q should name %s", got.Detail, tc.wantScope)
			}
			if other := callTool(t, srv, toolLookupUser, map[string]any{"user": "U1"}); other.IsError {
				t.Errorf("lookup_user failed after %s lost its scope: %s", tc.tool, other.Text)
			}
			if got := host.reports(); len(got) != 1 {
				t.Errorf("SetHealthState calls after healthy tool = %d, want still 1", len(got))
			}
		})
	}
}

func TestWriteToolInputValidation(t *testing.T) {
	blocksObject := map[string]any{"type": "section"}
	tests := []struct {
		name    string
		tool    string
		args    map[string]any
		wantErr string
	}{
		{"post missing channel", toolPostMessage, map[string]any{"text": "x"}, "channel is required"},
		{"post no text or blocks", toolPostMessage, map[string]any{"channel": "C1"}, "text or blocks is required"},
		{"post blocks is an object", toolPostMessage, map[string]any{"channel": "C1", "blocks": blocksObject}, "blocks must be a JSON array"},
		{"post blocks null and no text", toolPostMessage, map[string]any{"channel": "C1", "blocks": nil}, "text or blocks is required"},
		{"post wrong type", toolPostMessage, map[string]any{"channel": 5, "text": "x"}, "invalid arguments"},
		{"update missing ts", toolUpdateMessage, map[string]any{"channel": "C1", "text": "x"}, "ts is required"},
		{"update no text or blocks", toolUpdateMessage, map[string]any{"channel": "C1", "ts": "1.1"}, "text or blocks is required"},
		{"delete missing channel", toolDeleteMessage, map[string]any{"ts": "1.1"}, "channel is required"},
		{"delete missing ts", toolDeleteMessage, map[string]any{"channel": "C1"}, "ts is required"},
		{"react missing name", toolReact, map[string]any{"channel": "C1", "timestamp": "1.1"}, "name is required"},
		{"react missing timestamp", toolReact, map[string]any{"channel": "C1", "name": "eyes"}, "timestamp is required"},
		{"topic missing channel", toolSetTopic, map[string]any{"topic": "x"}, "channel is required"},
		{"topic too long", toolSetTopic, map[string]any{"channel": "C1", "topic": strings.Repeat("é", 251)}, "at most 250"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// No Slack responses are configured, so any call that got through
			// validation would 404 and fail with a different message.
			slackAPI := newFakeSlack(t, map[string]string{})
			host := &fakeHost{credentials: testCredentials}
			srv := newTestServer(t, host, slackAPI.URL+"/api/")

			res := callTool(t, srv, tc.tool, tc.args)
			if !res.IsError || !strings.Contains(res.Text, tc.wantErr) {
				t.Errorf("result = %+v, want isError containing %q", res, tc.wantErr)
			}
			slackAPI.mu.Lock()
			calls := len(slackAPI.tokens)
			slackAPI.mu.Unlock()
			if calls != 0 {
				t.Errorf("invalid input still reached Slack (%d calls)", calls)
			}
		})
	}
}

func TestTopicAtTheLimitIsAccepted(t *testing.T) {
	slackAPI := newFakeSlack(t, map[string]string{
		"conversations.setTopic": `{"ok":true,"channel":{"id":"C1","topic":{"value":"ok"}}}`,
	})
	srv := newTestServer(t, &fakeHost{credentials: testCredentials}, slackAPI.URL+"/api/")

	res := callTool(t, srv, toolSetTopic, map[string]any{"channel": "C1", "topic": strings.Repeat("é", 250)})
	if res.IsError {
		t.Errorf("250-character topic rejected: %s", res.Text)
	}
}
