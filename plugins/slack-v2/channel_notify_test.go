package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

type rpcResult struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// rpc drives one JSON-RPC method through the real mcpserver handler.
func rpc(t *testing.T, srv http.Handler, method string, params any) rpcResult {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	req.Header.Set("MCP-Protocol-Version", mcpserver.ProtocolVersion)
	req.Header.Set("Mcp-Method", method)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var out rpcResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return out
}

func notifyParams(delivery, address, message string) map[string]any {
	return map[string]any{
		"target":  map[string]any{"delivery": delivery, "address": address},
		"message": message,
	}
}

func TestEscapeMrkdwn(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"<!channel> wake up", "&lt;!channel&gt; wake up"},
		{"<@U123ABC> hi", "&lt;@U123ABC&gt; hi"},
		{"<http://evil|click here>", "&lt;http://evil|click here&gt;"},
		{"a & b", "a &amp; b"},
		{"already &lt;escaped&gt;", "already &amp;lt;escaped&amp;gt;"},
	}
	for _, tc := range tests {
		if got := escapeMrkdwn(tc.in); got != tc.want {
			t.Errorf("escapeMrkdwn(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNotifyDelivers(t *testing.T) {
	tests := []struct {
		name        string
		delivery    string
		address     string
		wantOpen    bool
		wantChannel string
	}{
		{"direct opens a DM first", channelext.DeliveryDirect, "U0123ABC", true, "D999"},
		{"shared by ID", channelext.DeliveryShared, "C0123ABC", false, "C0123ABC"},
		{"shared by name", channelext.DeliveryShared, "#ops-alerts", false, "#ops-alerts"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slackAPI := newFakeSlack(t, map[string]string{
				"conversations.open": `{"ok":true,"channel":{"id":"D999"}}`,
				"chat.postMessage":   `{"ok":true,"channel":"D999","ts":"1.1"}`,
			})
			host := &fakeHost{credentials: testCredentials}
			srv := newTestServer(t, host, slackAPI.URL+"/api/")

			res := rpc(t, srv, "channel/notify", notifyParams(tc.delivery, tc.address, "run <!channel> & <@U1> <http://x|y>"))
			if res.Error != nil {
				t.Fatalf("channel/notify error: %+v", res.Error)
			}

			open := slackAPI.form("conversations.open")
			if tc.wantOpen {
				if open.Get("users") != tc.address {
					t.Errorf("conversations.open users = %q, want %q", open.Get("users"), tc.address)
				}
			} else if open != nil {
				t.Errorf("conversations.open called for a shared delivery: %v", open)
			}

			post := slackAPI.form("chat.postMessage")
			if got := post.Get("channel"); got != tc.wantChannel {
				t.Errorf("chat.postMessage channel = %q, want %q", got, tc.wantChannel)
			}
			if got, want := post.Get("text"), "run &lt;!channel&gt; &amp; &lt;@U1&gt; &lt;http://x|y&gt;"; got != want {
				t.Errorf("text = %q, want %q", got, want)
			}
			for key, want := range map[string]string{"unfurl_links": "false", "unfurl_media": "false", "link_names": "false", "parse": "none"} {
				if got := post.Get(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
			if got := host.reports(); len(got) != 0 {
				t.Errorf("healthy delivery reported health: %+v", got)
			}
		})
	}
}

func TestNotifyRefusesMalformedAddressWithoutCallingSlack(t *testing.T) {
	tests := []struct{ delivery, address string }{
		{channelext.DeliveryDirect, "C0123ABC"},
		{channelext.DeliveryDirect, "u0123abc"},
		{channelext.DeliveryDirect, "U1,U2"},
		{channelext.DeliveryDirect, "@ada"},
		{channelext.DeliveryShared, "general chat"},
		{channelext.DeliveryShared, "<!channel>"},
		{channelext.DeliveryShared, "C1\nD2"},
	}
	for _, tc := range tests {
		t.Run(tc.delivery+" "+tc.address, func(t *testing.T) {
			slackAPI := newFakeSlack(t, map[string]string{})
			var calls int
			counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				slackAPI.Config.Handler.ServeHTTP(w, r)
			}))
			t.Cleanup(counting.Close)
			srv := newTestServer(t, &fakeHost{credentials: testCredentials}, counting.URL+"/api/")

			res := rpc(t, srv, "channel/notify", notifyParams(tc.delivery, tc.address, "hi"))
			if res.Error == nil {
				t.Fatal("malformed address was accepted")
			}
			if calls != 0 {
				t.Errorf("Slack API called %d times for a malformed address", calls)
			}
		})
	}
}

func TestNotifySlackErrorIsANotifyError(t *testing.T) {
	slackAPI := newFakeSlack(t, map[string]string{
		"chat.postMessage": `{"ok":false,"error":"channel_not_found"}`,
	})
	host := &fakeHost{credentials: testCredentials}
	srv := newTestServer(t, host, slackAPI.URL+"/api/")

	res := rpc(t, srv, "channel/notify", notifyParams(channelext.DeliveryShared, "C0123ABC", "hi"))
	if res.Error == nil || !strings.Contains(res.Error.Message, "channel_not_found") {
		t.Fatalf("error = %+v, want one naming channel_not_found", res.Error)
	}
	if got := host.reports(); len(got) != 0 {
		t.Errorf("non-scope error reported health: %+v", got)
	}
}

func TestNotifyMissingScopeReportsHumanChannelOnce(t *testing.T) {
	tests := []struct {
		name      string
		delivery  string
		address   string
		responses map[string]string
		wantScope string
	}{
		{
			name: "chat:write", delivery: channelext.DeliveryShared, address: "C0123ABC",
			responses: map[string]string{"chat.postMessage": `{"ok":false,"error":"missing_scope"}`},
			wantScope: "chat:write",
		},
		{
			name: "im:write", delivery: channelext.DeliveryDirect, address: "U0123ABC",
			responses: map[string]string{"conversations.open": `{"ok":false,"error":"missing_scope"}`},
			wantScope: "im:write",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slackAPI := newFakeSlack(t, tc.responses)
			host := &fakeHost{credentials: testCredentials}
			srv := newTestServer(t, host, slackAPI.URL+"/api/")

			for range 2 {
				res := rpc(t, srv, "channel/notify", notifyParams(tc.delivery, tc.address, "hi"))
				if res.Error == nil || !strings.Contains(res.Error.Message, "missing_scope") {
					t.Fatalf("error = %+v, want missing_scope", res.Error)
				}
			}

			reports := host.reports()
			if len(reports) != 1 {
				t.Fatalf("SetHealthState calls = %d (%+v), want exactly 1", len(reports), reports)
			}
			got := reports[0]
			if got.Profile != hostclient.ProfileHumanChannel || got.Capability != "" || got.State != hostclient.HealthStateUnhealthy {
				t.Errorf("report = %+v, want profile-wide human_channel unhealthy", got)
			}
			if !strings.Contains(got.Detail, tc.wantScope) || !strings.Contains(got.Detail, "missing_scope") {
				t.Errorf("detail %q should name missing_scope and %s", got.Detail, tc.wantScope)
			}
		})
	}
}

func TestServerDiscoverDeclaresTheChannelExtension(t *testing.T) {
	srv := newTestServer(t, &fakeHost{credentials: testCredentials}, "")
	res := rpc(t, srv, "server/discover", map[string]any{
		"_meta": map[string]any{
			mcpserver.MetaKeyProtocolVersion:    mcpserver.ProtocolVersion,
			mcpserver.MetaKeyClientCapabilities: map[string]any{},
		},
	})
	if res.Error != nil {
		t.Fatalf("server/discover error: %+v", res.Error)
	}
	var result struct {
		Capabilities struct {
			Extensions map[string]struct {
				Version    string   `json:"version"`
				Assurance  string   `json:"assurance"`
				Deliveries []string `json:"deliveries"`
			} `json:"extensions"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(res.Result, &result); err != nil {
		t.Fatalf("decode %s: %v", res.Result, err)
	}
	decl, ok := result.Capabilities.Extensions[channelext.ExtensionChannel]
	if !ok {
		t.Fatalf("%s not declared: %s", channelext.ExtensionChannel, res.Result)
	}
	if decl.Assurance != "authenticated" {
		t.Errorf("assurance = %q, want authenticated", decl.Assurance)
	}
	if want := []string{"direct", "shared"}; !slices.Equal(decl.Deliveries, want) {
		t.Errorf("deliveries = %v, want %v", decl.Deliveries, want)
	}
}
