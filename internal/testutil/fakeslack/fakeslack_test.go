package fakeslack_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/felag-engineering/gleipnir/internal/testutil/fakeslack"
)

// ciBound is a generous upper bound for CI; tests proceed as soon as the
// awaited event arrives.
const ciBound = 30 * time.Second

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(ciBound):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func newAPI(f *fakeslack.Server) *slack.Client {
	return slack.New("xoxb-fake", slack.OptionAPIURL(f.URL()), slack.OptionAppLevelToken("xapp-fake"))
}

// startSocketMode runs a real socketmode client against the fake and returns
// it once the connection is established.
func startSocketMode(t *testing.T, f *fakeslack.Server) *socketmode.Client {
	t.Helper()
	client := socketmode.New(newAPI(f))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = client.RunContext(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	recv(t, f.Hellos(), "hello frame")
	return client
}

// nextEvent returns the next socketmode event of the wanted type.
func nextEvent(t *testing.T, c *socketmode.Client, want socketmode.EventType) socketmode.Event {
	t.Helper()
	deadline := time.After(ciBound)
	for {
		select {
		case ev := <-c.Events:
			if ev.Type == want {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for socketmode event %q", want)
		}
	}
}

func TestWebAPI_RecordsPostUpdateEphemeral(t *testing.T) {
	f := fakeslack.New(t)
	api := newAPI(f)

	auth, err := api.AuthTest()
	if err != nil {
		t.Fatalf("AuthTest: %v", err)
	}
	if auth.UserID != "UBOT" {
		t.Errorf("auth user_id = %q, want UBOT", auth.UserID)
	}

	dm, _, _, err := api.OpenConversation(&slack.OpenConversationParameters{Users: []string{"U1"}})
	if err != nil {
		t.Fatalf("OpenConversation: %v", err)
	}
	if dm.ID == "" {
		t.Error("conversations.open returned empty channel id")
	}

	_, ts, err := api.PostMessage("C1", slack.MsgOptionText("hello", false), slack.MsgOptionTS("123.456"))
	if err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	posted := recv(t, f.PostedMessages(), "postMessage")
	if posted.Channel != "C1" || posted.Text != "hello" || posted.ThreadTS != "123.456" || posted.TS != ts {
		t.Errorf("posted = %+v (returned ts %q)", posted, ts)
	}

	if _, _, _, err := api.UpdateMessage("C1", ts, slack.MsgOptionText("edited", false)); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}
	upd := recv(t, f.Updates(), "update")
	if upd.Channel != "C1" || upd.TS != ts || upd.Text != "edited" {
		t.Errorf("update = %+v", upd)
	}

	if _, err := api.PostEphemeral("C1", "U2", slack.MsgOptionText("psst", false)); err != nil {
		t.Fatalf("PostEphemeral: %v", err)
	}
	eph := recv(t, f.Ephemerals(), "ephemeral")
	if eph.Channel != "C1" || eph.User != "U2" || eph.Text != "psst" {
		t.Errorf("ephemeral = %+v", eph)
	}
}

func TestWebAPI_UnknownMethodAndCustomHandler(t *testing.T) {
	f := fakeslack.New(t)
	api := newAPI(f)

	if _, err := api.GetPermalink(&slack.PermalinkParameters{Channel: "C1", Ts: "1"}); err == nil {
		t.Fatal("expected error for unregistered method")
	}

	f.HandleMethod("chat.getPermalink", func(args url.Values) (map[string]any, string) {
		return map[string]any{"permalink": "https://fake/" + args["message_ts"][0]}, ""
	})
	link, err := api.GetPermalink(&slack.PermalinkParameters{Channel: "C1", Ts: "42"})
	if err != nil {
		t.Fatalf("GetPermalink: %v", err)
	}
	if link != "https://fake/42" {
		t.Errorf("permalink = %q", link)
	}
}

func TestWebAPI_ReadFixtures(t *testing.T) {
	f := fakeslack.New(t)
	api := newAPI(f)

	f.SetChannels([]slack.Channel{{GroupConversation: slack.GroupConversation{Conversation: slack.Conversation{ID: "C1"}, Name: "general"}}})
	f.SetUsers([]slack.User{{ID: "U1", Name: "ada"}})
	f.SetHistory("C1", []slack.Message{{Msg: slack.Msg{Text: "first", Timestamp: "1.0"}}})
	f.SetReplies("C1", "1.0", []slack.Message{{Msg: slack.Msg{Text: "first", Timestamp: "1.0"}}, {Msg: slack.Msg{Text: "reply", Timestamp: "2.0"}}})

	chs, _, err := api.GetConversations(&slack.GetConversationsParameters{})
	if err != nil || len(chs) != 1 || chs[0].Name != "general" {
		t.Fatalf("GetConversations = %+v, %v", chs, err)
	}
	user, err := api.GetUserInfo("U1")
	if err != nil || user.Name != "ada" {
		t.Fatalf("GetUserInfo = %+v, %v", user, err)
	}
	if _, err := api.GetUserInfo("U404"); err == nil {
		t.Error("expected user_not_found")
	}
	hist, err := api.GetConversationHistory(&slack.GetConversationHistoryParameters{ChannelID: "C1"})
	if err != nil || len(hist.Messages) != 1 || hist.Messages[0].Text != "first" {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	msgs, _, _, err := api.GetConversationReplies(&slack.GetConversationRepliesParameters{ChannelID: "C1", Timestamp: "1.0"})
	if err != nil || len(msgs) != 2 || msgs[1].Text != "reply" {
		t.Fatalf("replies = %+v, %v", msgs, err)
	}
}

func TestSocketMode_HelloAndInjectEventAck(t *testing.T) {
	f := fakeslack.New(t)
	client := startSocketMode(t, f)
	nextEvent(t, client, socketmode.EventTypeHello)

	envelopeID := f.InjectEvent(map[string]any{
		"type":    "app_mention",
		"user":    "U1",
		"text":    "<@UBOT> hi",
		"channel": "C1",
		"ts":      "1.0",
	})

	ev := nextEvent(t, client, socketmode.EventTypeEventsAPI)
	if ev.Request == nil || ev.Request.EnvelopeID != envelopeID {
		t.Fatalf("request = %+v, want envelope %q", ev.Request, envelopeID)
	}
	apiEv, ok := ev.Data.(slackevents.EventsAPIEvent)
	if !ok {
		t.Fatalf("data = %T, want EventsAPIEvent", ev.Data)
	}
	mention, ok := apiEv.InnerEvent.Data.(*slackevents.AppMentionEvent)
	if !ok {
		t.Fatalf("inner = %T, want AppMentionEvent", apiEv.InnerEvent.Data)
	}
	if mention.User != "U1" || mention.Channel != "C1" {
		t.Errorf("mention = %+v", mention)
	}

	client.Ack(*ev.Request)
	ack := recv(t, f.Acks(), "ack")
	if ack.EnvelopeID != envelopeID {
		t.Errorf("ack envelope = %q, want %q", ack.EnvelopeID, envelopeID)
	}
}

func TestSocketMode_InjectBlockActionRoundTrip(t *testing.T) {
	f := fakeslack.New(t)
	client := startSocketMode(t, f)

	envelopeID := f.InjectBlockAction("run-1:req-9", "approve", "U7")

	ev := nextEvent(t, client, socketmode.EventTypeInteractive)
	cb, ok := ev.Data.(slack.InteractionCallback)
	if !ok {
		t.Fatalf("data = %T, want InteractionCallback", ev.Data)
	}
	if cb.Type != slack.InteractionTypeBlockActions || cb.User.ID != "U7" || len(cb.ActionCallback.BlockActions) != 1 {
		t.Fatalf("callback = %+v", cb)
	}
	taskID, optionID, ok := fakeslack.ParseActionID(cb.ActionCallback.BlockActions[0].ActionID)
	if !ok || taskID != "run-1:req-9" || optionID != "approve" {
		t.Errorf("decoded action id = (%q, %q, %v)", taskID, optionID, ok)
	}

	client.Ack(*ev.Request, map[string]any{"text": "done"})
	ack := recv(t, f.Acks(), "ack")
	if ack.EnvelopeID != envelopeID {
		t.Errorf("ack envelope = %q, want %q", ack.EnvelopeID, envelopeID)
	}
	var payload map[string]any
	if err := json.Unmarshal(ack.Payload, &payload); err != nil || payload["text"] != "done" {
		t.Errorf("ack payload = %s (%v)", ack.Payload, err)
	}
}

func TestSocketMode_EventInjectedBeforeConnectIsHeld(t *testing.T) {
	f := fakeslack.New(t)
	envelopeID := f.InjectEvent(map[string]any{"type": "message", "user": "U1", "text": "early", "channel": "C1", "ts": "1.0"})

	client := startSocketMode(t, f)
	ev := nextEvent(t, client, socketmode.EventTypeEventsAPI)
	if ev.Request.EnvelopeID != envelopeID {
		t.Errorf("envelope = %q, want %q", ev.Request.EnvelopeID, envelopeID)
	}
}

func TestActionID_RoundTrip(t *testing.T) {
	tests := []struct {
		name, task, option string
		wantOK             bool
	}{
		{"simple", "t1", "yes", true},
		{"colons in task id", "a:b:c", "no", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task, option, ok := fakeslack.ParseActionID(fakeslack.ActionID(tt.task, tt.option))
			if ok != tt.wantOK || task != tt.task || option != tt.option {
				t.Errorf("got (%q, %q, %v)", task, option, ok)
			}
		})
	}
	for _, bad := range []string{"", "other:x:y", "feedback_response:onlyone", "feedback_response::opt"} {
		if _, _, ok := fakeslack.ParseActionID(bad); ok {
			t.Errorf("ParseActionID(%q) ok, want false", bad)
		}
	}
}

// TestNotLinkedIntoProduction proves fakeslack stays test-only: no non-test
// package in the module may import it. `go list` without -test reports only
// production imports.
func TestNotLinkedIntoProduction(t *testing.T) {
	const self = "github.com/felag-engineering/gleipnir/internal/testutil/fakeslack"

	cmd := exec.Command("go", "list", "-f", `{{.ImportPath}}: {{join .Imports " "}}`, "./...")
	cmd.Dir = "../../.."
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list ./...: %v\n%s", err, out)
	}

	for _, line := range strings.Split(string(out), "\n") {
		pkg, imports, found := strings.Cut(line, ": ")
		if !found || pkg == self {
			continue
		}
		for _, imp := range strings.Fields(imports) {
			if imp == self {
				t.Errorf("production package %s imports %s; the fake must only be imported from tests", pkg, self)
			}
		}
	}
}
