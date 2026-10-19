package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
)

func requestParams(delivery, address, message, kind string, options ...map[string]string) map[string]any {
	p := notifyParams(delivery, address, message)
	if len(options) > 0 {
		p["options"] = options
	}
	if kind != "" {
		p["kind"] = kind
	}
	return p
}

func opt(id, label string) map[string]string { return map[string]string{"id": id, "label": label} }

type taskEnvelope struct {
	TaskID string `json:"taskId"`
	Status string `json:"status"`
}

func decodeTask(t *testing.T, res rpcResult) taskEnvelope {
	t.Helper()
	if res.Error != nil {
		t.Fatalf("rpc error: %+v", res.Error)
	}
	var env taskEnvelope
	if err := json.Unmarshal(res.Result, &env); err != nil {
		t.Fatalf("decode %s: %v", res.Result, err)
	}
	return env
}

func requestSlack(t *testing.T) *fakeSlack {
	t.Helper()
	return newFakeSlack(t, map[string]string{
		"conversations.open": `{"ok":true,"channel":{"id":"D999"}}`,
		"chat.postMessage":   `{"ok":true,"channel":"D999","ts":"1.1"}`,
		"chat.update":        `{"ok":true,"channel":"D999","ts":"1.1"}`,
	})
}

type blockJSON struct {
	Type     string                 `json:"type"`
	Text     *struct{ Text string } `json:"text"`
	Elements []struct {
		ActionID string          `json:"action_id"`
		Value    string          `json:"value"`
		Style    string          `json:"style"`
		Text     json.RawMessage `json:"text"`
	} `json:"elements"`
}

func blocksOf(t *testing.T, raw string) []blockJSON {
	t.Helper()
	var blocks []blockJSON
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		t.Fatalf("decode blocks %q: %v", raw, err)
	}
	return blocks
}

func TestRequestPostsOneButtonPerOption(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		wantStyles []string
		wantLock   bool
	}{
		{"information has no styling", "information", []string{"", ""}, false},
		{"permission styles approve and deny", kindPermission, []string{"primary", "danger"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slackAPI := requestSlack(t)
			srv := newTestServer(t, &fakeHost{credentials: testCredentials}, slackAPI.URL+"/api/")

			env := decodeTask(t, rpc(t, srv, "channel/request",
				requestParams("direct", "U0123ABC", "ship <!channel> & go?", tc.kind, opt("approve", "Approve"), opt("deny", "Deny"))))
			if env.Status != "working" || env.TaskID == "" {
				t.Fatalf("envelope = %+v, want a working task", env)
			}

			if got := slackAPI.form("conversations.open").Get("users"); got != "U0123ABC" {
				t.Errorf("conversations.open users = %q", got)
			}
			post := slackAPI.form("chat.postMessage")
			if got := post.Get("channel"); got != "D999" {
				t.Errorf("channel = %q, want the DM", got)
			}
			blocks := blocksOf(t, post.Get("blocks"))
			if len(blocks) != 2 || blocks[0].Type != "section" || blocks[1].Type != "actions" {
				t.Fatalf("blocks = %+v, want section + actions", blocks)
			}
			text := blocks[0].Text.Text
			if !strings.Contains(text, "ship &lt;!channel&gt; &amp; go?") || strings.Contains(text, "<!channel>") {
				t.Errorf("message text not escaped: %q", text)
			}
			if strings.Contains(text, "Permission requested") != tc.wantLock {
				t.Errorf("permission header presence = %v, want %v: %q", !tc.wantLock, tc.wantLock, text)
			}
			buttons := blocks[1].Elements
			if len(buttons) != 2 {
				t.Fatalf("buttons = %+v", buttons)
			}
			for i, id := range []string{"approve", "deny"} {
				if want := actionIDFor(env.TaskID, id); buttons[i].ActionID != want {
					t.Errorf("button %d action_id = %q, want %q", i, buttons[i].ActionID, want)
				}
				if buttons[i].Value != id {
					t.Errorf("button %d value = %q", i, buttons[i].Value)
				}
				if buttons[i].Style != tc.wantStyles[i] {
					t.Errorf("button %d style = %q, want %q", i, buttons[i].Style, tc.wantStyles[i])
				}
			}
		})
	}
}

func TestRequestSharedDeliverySkipsConversationsOpen(t *testing.T) {
	slackAPI := requestSlack(t)
	srv := newTestServer(t, &fakeHost{credentials: testCredentials}, slackAPI.URL+"/api/")
	decodeTask(t, rpc(t, srv, "channel/request", requestParams("shared", "C0123ABC", "ok?", "", opt("yes", "Yes"))))
	if slackAPI.calls("conversations.open") != 0 {
		t.Error("conversations.open called for a shared delivery")
	}
	if got := slackAPI.form("chat.postMessage").Get("channel"); got != "C0123ABC" {
		t.Errorf("channel = %q", got)
	}
}

func TestRequestRefusalsPostNothing(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
	}{
		{"schema only falls through to in-app", func() map[string]any {
			p := notifyParams("direct", "U0123ABC", "fill in")
			p["requestedSchema"] = map[string]any{"type": "object"}
			return p
		}()},
		{"malformed address", requestParams("direct", "not-a-user", "ok?", "", opt("yes", "Yes"))},
		{"option id too long for an action id", requestParams("direct", "U0123ABC", "ok?", "", opt(strings.Repeat("x", 250), "X"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slackAPI := requestSlack(t)
			srv := newTestServer(t, &fakeHost{credentials: testCredentials}, slackAPI.URL+"/api/")
			res := rpc(t, srv, "channel/request", tc.params)
			if res.Error == nil {
				t.Fatalf("expected an error, got %s", res.Result)
			}
			if n := slackAPI.calls("chat.postMessage"); n != 0 {
				t.Errorf("chat.postMessage called %d times", n)
			}
		})
	}
}

func TestRequestSlackFailureFailsTheCall(t *testing.T) {
	slackAPI := newFakeSlack(t, map[string]string{"chat.postMessage": `{"ok":false,"error":"channel_not_found"}`})
	srv := newTestServer(t, &fakeHost{credentials: testCredentials}, slackAPI.URL+"/api/")
	res := rpc(t, srv, "channel/request", requestParams("shared", "C0123ABC", "ok?", "", opt("yes", "Yes")))
	if res.Error == nil || !strings.Contains(res.Error.Message, "channel_not_found") {
		t.Errorf("error = %+v, want channel_not_found", res.Error)
	}
}

func TestTaskSurvivesARestart(t *testing.T) {
	slackAPI := requestSlack(t)
	dataDir := t.TempDir()
	host := &fakeHost{credentials: testCredentials}
	first, err := newServer(host, slackAPI.URL+"/api/", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	env := decodeTask(t, rpc(t, first, "channel/request", requestParams("shared", "C0123ABC", "ok?", "", opt("yes", "Yes"))))

	restarted, err := newServer(host, slackAPI.URL+"/api/", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeTask(t, rpc(t, restarted, "tasks/get", map[string]any{"taskId": env.TaskID}))
	if got.Status != "working" {
		t.Errorf("status after restart = %q, want working", got.Status)
	}

	// The restarted process can still retract the message it did not post.
	cancelled := decodeTask(t, rpc(t, restarted, "tasks/cancel", map[string]any{"taskId": env.TaskID}))
	if cancelled.Status != "cancelled" {
		t.Errorf("cancel status = %q", cancelled.Status)
	}
	if n := slackAPI.calls("chat.update"); n != 1 {
		t.Errorf("chat.update calls = %d, want 1", n)
	}
}

func TestCancelUpdatesTheMessageToAResolvedSummary(t *testing.T) {
	slackAPI := requestSlack(t)
	srv := newTestServer(t, &fakeHost{credentials: testCredentials}, slackAPI.URL+"/api/")
	env := decodeTask(t, rpc(t, srv, "channel/request", requestParams("shared", "C0123ABC", "deploy <!here>?", "", opt("yes", "Yes"))))

	decodeTask(t, rpc(t, srv, "tasks/cancel", map[string]any{"taskId": env.TaskID}))

	update := slackAPI.form("chat.update")
	if update.Get("channel") != "D999" || update.Get("ts") != "1.1" {
		t.Errorf("update targets %q/%q, want the posted message D999/1.1", update.Get("channel"), update.Get("ts"))
	}
	blocks := blocksOf(t, update.Get("blocks"))
	for _, b := range blocks {
		if b.Type == "actions" {
			t.Errorf("resolved summary still has actions: %+v", blocks)
		}
	}
	if len(blocks) == 0 || !strings.Contains(blocks[0].Text.Text, "deploy &lt;!here&gt;?") {
		t.Errorf("summary lost the escaped original text: %+v", blocks)
	}
}

func TestCancelAfterCompletionDoesNotUpdate(t *testing.T) {
	slackAPI := requestSlack(t)
	dataDir := t.TempDir()
	srv, err := newServer(&fakeHost{credentials: testCredentials}, slackAPI.URL+"/api/", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	env := decodeTask(t, rpc(t, srv, "channel/request", requestParams("shared", "C0123ABC", "ok?", "", opt("yes", "Yes"))))

	store, err := channelext.NewFileTaskStore(dataDir + "/tasks")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(t.Context(), env.TaskID, channelext.Resolution{OptionID: "yes", ActorExternalID: "U1"}); err != nil {
		t.Fatal(err)
	}

	got := decodeTask(t, rpc(t, srv, "tasks/cancel", map[string]any{"taskId": env.TaskID}))
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
	if n := slackAPI.calls("chat.update"); n != 0 {
		t.Errorf("chat.update called %d times after completion", n)
	}
}

func TestMarkResolvedIsIdempotent(t *testing.T) {
	slackAPI := requestSlack(t)
	srv, err := newServer(&fakeHost{credentials: testCredentials}, slackAPI.URL+"/api/", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := decodeTask(t, rpc(t, srv, "channel/request", requestParams("shared", "C0123ABC", "ok?", "", opt("yes", "Yes"))))
	decodeTask(t, rpc(t, srv, "tasks/cancel", map[string]any{"taskId": env.TaskID}))
	first := slackAPI.form("chat.update").Encode()
	// A second cancel finds the task already terminal: no second update.
	decodeTask(t, rpc(t, srv, "tasks/cancel", map[string]any{"taskId": env.TaskID}))
	if n := slackAPI.calls("chat.update"); n != 1 {
		t.Errorf("chat.update calls = %d, want 1", n)
	}
	if slackAPI.form("chat.update").Encode() != first {
		t.Error("update content changed")
	}
}
