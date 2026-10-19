package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient/hostclienttest"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

const clickerID = "U0CLICKER"

type failingAuthorizer struct{}

func (failingAuthorizer) AuthorizeActor(context.Context, hostclient.AuthorizeActorRequest) (*hostclient.AuthorizeActorResponse, error) {
	return nil, errors.New("host unreachable")
}

// clickFixture is a plugin with one open request, ready to receive clicks.
type clickFixture struct {
	slack  *fakeSlack
	srv    http.Handler
	plugin *plugin
	taskID string
}

func newClickFixture(t *testing.T) *clickFixture {
	t.Helper()
	slackAPI := newFakeSlack(t, map[string]string{
		"chat.postMessage":   `{"ok":true,"channel":"C0123ABC","ts":"1.1"}`,
		"chat.update":        `{"ok":true,"channel":"C0123ABC","ts":"1.1"}`,
		"chat.postEphemeral": `{"ok":true,"message_ts":"2.2"}`,
	})
	srv, p, err := newServer(&fakeHost{credentials: testCredentials}, slackAPI.URL+"/api/", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := decodeTask(t, rpc(t, srv, "channel/request",
		requestParams("shared", "C0123ABC", "ship it?", "", opt("approve", "Approve <b>"), opt("deny", "Deny"))))
	return &clickFixture{slack: slackAPI, srv: srv, plugin: p, taskID: env.TaskID}
}

func (f *clickFixture) event(actionID, user string) socketmode.Event {
	return socketmode.Event{
		Type: socketmode.EventTypeInteractive,
		Data: slack.InteractionCallback{
			Type:    slack.InteractionTypeBlockActions,
			User:    slack.User{ID: user, Name: "display name"},
			Channel: slack.Channel{GroupConversation: slack.GroupConversation{Conversation: slack.Conversation{ID: "C0123ABC"}}},
			ActionCallback: slack.ActionCallbacks{
				BlockActions: []*slack.BlockAction{{ActionID: actionID}},
			},
		},
	}
}

// click delivers one click and waits for its processing to finish.
func (f *clickFixture) click(t *testing.T, h *clickHandler, user, optionID string) {
	t.Helper()
	ack, err := h.handle(t.Context(), f.event(actionIDFor(f.taskID, optionID), user))
	if err != nil || ack != nil {
		t.Fatalf("handle = (%v, %v), want a bare ack", ack, err)
	}
	h.wait()
}

func (f *clickFixture) status(t *testing.T) (status string, result map[string]string) {
	t.Helper()
	res := rpc(t, f.srv, "tasks/get", map[string]any{"taskId": f.taskID})
	if res.Error != nil {
		t.Fatalf("tasks/get: %+v", res.Error)
	}
	var env struct {
		Status string            `json:"status"`
		Result map[string]string `json:"result"`
	}
	if err := json.Unmarshal(res.Result, &env); err != nil {
		t.Fatal(err)
	}
	return env.Status, env.Result
}

func (f *clickFixture) ephemeralText(t *testing.T) string {
	t.Helper()
	form := f.slack.form("chat.postEphemeral")
	if form == nil {
		return ""
	}
	if form.Get("user") != clickerID {
		t.Errorf("ephemeral user = %q, want the clicker", form.Get("user"))
	}
	return form.Get("text")
}

func authorizeCalls(srv *hostclienttest.Server) []hostclienttest.Call {
	var out []hostclienttest.Call
	for _, c := range srv.Calls() {
		if c.Method == "host/authorize_actor" {
			out = append(out, c)
		}
	}
	return out
}

func hostClient(t *testing.T, opts ...hostclienttest.Option) (*hostclient.Client, *hostclienttest.Server) {
	t.Helper()
	fake := hostclienttest.NewServer(t, opts...)
	c, err := hostclient.New()
	if err != nil {
		t.Fatal(err)
	}
	return c, fake
}

func TestActorExternalIDIsTheSlackUserID(t *testing.T) {
	user := slack.User{ID: "U0123ABC", Name: "ada", RealName: "Ada Lovelace", Profile: slack.UserProfile{DisplayName: "U0999ZZZ"}}
	if got := actorExternalID(user); got != "U0123ABC" {
		t.Errorf("actorExternalID = %q, want the user id, never a name", got)
	}
	if got := actorExternalID(slack.User{Name: "ada", RealName: "Ada"}); got != "" {
		t.Errorf("actorExternalID without an id = %q, want empty", got)
	}
}

func TestAuthorizedClickCompletesTheTask(t *testing.T) {
	f := newClickFixture(t)
	host, fake := hostClient(t, hostclienttest.WithAuthorizedActor(clickerID, "user-1"))
	h := newClickHandler(f.plugin, host)

	f.click(t, h, clickerID, "approve")

	status, result := f.status(t)
	if status != "completed" {
		t.Fatalf("status = %q, want completed", status)
	}
	if result["optionId"] != "approve" || result["actorExternalId"] != clickerID {
		t.Errorf("result = %v, want optionId approve and actorExternalId %s", result, clickerID)
	}
	calls := authorizeCalls(fake)
	if len(calls) != 1 {
		t.Fatalf("authorize_actor calls = %d, want 1", len(calls))
	}
	var args hostclient.AuthorizeActorRequest
	if err := json.Unmarshal(calls[0].Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if args.RequestID != f.taskID || args.ActorExternalID != clickerID {
		t.Errorf("authorize_actor args = %+v", args)
	}

	if n := f.slack.calls("chat.update"); n != 1 {
		t.Fatalf("chat.update calls = %d, want 1", n)
	}
	update := f.slack.form("chat.update")
	if strings.Contains(update.Get("blocks"), `"actions"`) {
		t.Errorf("resolved summary still has actions: %s", update.Get("blocks"))
	}
	// json.Marshal writes <, > and & as \u escapes; the decoded text is what
	// Slack renders.
	var summary []struct {
		Elements []struct{ Text string } `json:"elements"`
	}
	if err := json.Unmarshal([]byte(update.Get("blocks")), &summary); err != nil {
		t.Fatal(err)
	}
	want := ":white_check_mark: Resolved: *Approve &lt;b&gt;* by <@" + clickerID + ">"
	if len(summary) != 2 || len(summary[1].Elements) != 1 || summary[1].Elements[0].Text != want {
		t.Errorf("summary = %+v, want note %q", summary, want)
	}
}

func TestUnauthorizedClickLeavesTheTaskWorking(t *testing.T) {
	f := newClickFixture(t)
	host, fake := hostClient(t, hostclienttest.WithAuthorizedActor("U0SOMEONEELSE", "user-2"))
	h := newClickHandler(f.plugin, host)

	f.click(t, h, clickerID, "approve")

	if status, _ := f.status(t); status != "working" {
		t.Errorf("status = %q, want working", status)
	}
	if got := f.ephemeralText(t); got != msgNotAuthorized {
		t.Errorf("ephemeral = %q, want %q", got, msgNotAuthorized)
	}
	if n := f.slack.calls("chat.update"); n != 0 {
		t.Errorf("chat.update called %d times for an unauthorized click", n)
	}
	if n := len(authorizeCalls(fake)); n != 1 {
		t.Errorf("authorize_actor calls = %d, want 1", n)
	}

	// The request stays answerable by someone who may.
	authorized, _ := hostClient(t, hostclienttest.WithAuthorizedActor("U0APPROVER", "user-3"))
	f.click(t, newClickHandler(f.plugin, authorized), "U0APPROVER", "deny")
	if status, _ := f.status(t); status != "completed" {
		t.Errorf("status after an authorized click = %q, want completed", status)
	}
}

func TestHostErrorFailsClosed(t *testing.T) {
	f := newClickFixture(t)
	h := newClickHandler(f.plugin, failingAuthorizer{})

	f.click(t, h, clickerID, "approve")

	if status, _ := f.status(t); status != "working" {
		t.Errorf("status = %q, want working", status)
	}
	if got := f.ephemeralText(t); got != msgCannotVerify {
		t.Errorf("ephemeral = %q, want %q", got, msgCannotVerify)
	}
	if n := f.slack.calls("chat.update"); n != 0 {
		t.Errorf("chat.update called %d times after a host error", n)
	}
}

func TestDoubleClickIsAlreadyResolved(t *testing.T) {
	f := newClickFixture(t)
	host, _ := hostClient(t, hostclienttest.WithAuthorizedActor(clickerID, "user-1"))
	h := newClickHandler(f.plugin, host)

	f.click(t, h, clickerID, "approve")
	if f.slack.calls("chat.postEphemeral") != 0 {
		t.Fatal("first click produced an ephemeral reply")
	}
	f.click(t, h, clickerID, "deny")

	status, result := f.status(t)
	if status != "completed" || result["optionId"] != "approve" {
		t.Errorf("task = %q %v, want the first click to stand", status, result)
	}
	if got := f.ephemeralText(t); got != msgAlreadyDone {
		t.Errorf("ephemeral = %q, want %q", got, msgAlreadyDone)
	}
	if n := f.slack.calls("chat.update"); n != 1 {
		t.Errorf("chat.update calls = %d, want 1", n)
	}
}

func TestClicksThatAreNotAnswersAreIgnored(t *testing.T) {
	f := newClickFixture(t)
	host, fake := hostClient(t, hostclienttest.WithAuthorizedActor(clickerID, "user-1"))
	h := newClickHandler(f.plugin, host)

	t.Run("foreign action id", func(t *testing.T) {
		if _, err := h.handle(t.Context(), f.event("someone_elses_button", clickerID)); err != nil {
			t.Fatal(err)
		}
		h.wait()
	})
	t.Run("option the request never offered", func(t *testing.T) {
		f.click(t, h, clickerID, "approve-everything")
		if got := f.ephemeralText(t); got != msgRequestUnknown {
			t.Errorf("ephemeral = %q, want %q", got, msgRequestUnknown)
		}
	})
	t.Run("click without a user id", func(t *testing.T) {
		f.click(t, h, "", "approve")
	})

	if n := len(authorizeCalls(fake)); n != 0 {
		t.Errorf("authorize_actor called %d times for clicks that were not answers", n)
	}
	if status, _ := f.status(t); status != "working" {
		t.Errorf("status = %q, want working", status)
	}
}

func TestHandleRejectsAForeignPayloadType(t *testing.T) {
	f := newClickFixture(t)
	h := newClickHandler(f.plugin, failingAuthorizer{})
	if _, err := h.handle(t.Context(), socketmode.Event{Data: "nope"}); err == nil {
		t.Error("expected an error for a payload that is not an interaction callback")
	}
}
