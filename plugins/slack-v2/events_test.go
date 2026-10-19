package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/events"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func declaredBindingFields(t *testing.T, k events.Kind) []string {
	t.Helper()
	schema, ok := k.BindingSchema.(map[string]any)
	if !ok {
		t.Fatalf("kind %s: binding schema is %T", k.Kind, k.BindingSchema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("kind %s: binding schema has no properties", k.Kind)
	}
	var names []string
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// representativeEvents is one realistic translation per declared kind.
func representativeEvents(t *testing.T) map[string]translation {
	t.Helper()
	channel := slack.Channel{GroupConversation: slack.GroupConversation{Conversation: slack.Conversation{ID: "C01ABC"}}}
	message, reason := translateInner(slackevents.EventsAPIInnerEvent{Data: &slackevents.MessageEvent{
		Channel: "C01ABC", ChannelType: "channel", User: "U01USER", Text: "<@U07BOT> hi", TimeStamp: "1700000000.000100",
	}}, "T1", testSelf)
	if reason != "" {
		t.Fatalf("channel message dropped: %s", reason)
	}
	dm, reason := translateInner(slackevents.EventsAPIInnerEvent{Data: &slackevents.MessageEvent{
		Channel: "D01DM", ChannelType: "im", User: "U01USER", Text: "hi", TimeStamp: "1700000000.000200",
	}}, "T1", testSelf)
	if reason != "" {
		t.Fatalf("direct message dropped: %s", reason)
	}
	messageShortcut, _ := translateShortcut(slack.InteractionCallback{
		Type: slack.InteractionTypeMessageAction, CallbackID: "run_agent", TriggerID: "trig-2", User: slack.User{ID: "U01USER"},
		Channel: channel, Message: slack.Message{Msg: slack.Msg{Text: "the db is down", Timestamp: "1700000000.000300"}},
	})
	globalShortcut, _ := translateShortcut(slack.InteractionCallback{
		Type: slack.InteractionTypeShortcut, CallbackID: "start_agent", TriggerID: "trig-3", User: slack.User{ID: "U01USER"},
	})
	return map[string]translation{
		kindChannelMessage:  message,
		kindDirectMessage:   dm,
		kindSlashCommand:    translateSlashCommand(slack.SlashCommand{Command: "/deploy", Text: "staging", UserID: "U01USER", ChannelID: "C01ABC", TriggerID: "trig-1"}),
		kindMessageShortcut: messageShortcut,
		kindGlobalShortcut:  globalShortcut,
	}
}

func TestEveryBindingFieldIsPopulated(t *testing.T) {
	samples := representativeEvents(t)
	for _, k := range eventKinds {
		t.Run(k.Kind, func(t *testing.T) {
			sample, ok := samples[k.Kind]
			if !ok {
				t.Fatalf("no representative event for kind %s", k.Kind)
			}
			if sample.Event.Type != k.Kind {
				t.Fatalf("representative event is a %s, not a %s", sample.Event.Type, k.Kind)
			}
			data := dataMap(t, sample.Event.Data)
			for _, field := range declaredBindingFields(t, k) {
				value, present := data[field]
				if !present {
					t.Errorf("binding field %q is declared for %s but absent from its event data %v", field, k.Kind, data)
					continue
				}
				if s, isString := value.(string); isString && s == "" {
					t.Errorf("binding field %q is declared for %s but empty in a representative event", field, k.Kind)
				}
			}
		})
	}
	if len(samples) != len(eventKinds) {
		t.Errorf("%d representative events for %d kinds", len(samples), len(eventKinds))
	}
}

func TestEventKindsDeclareNoReservedFieldNames(t *testing.T) {
	for _, k := range eventKinds {
		if slices.Contains(declaredBindingFields(t, k), "mention_only") {
			t.Errorf("kind %s declares the reserved binding field mention_only", k.Kind)
		}
	}
}

func TestEveryOperatorNamesADeclaredField(t *testing.T) {
	for _, k := range eventKinds {
		fields := declaredBindingFields(t, k)
		for field := range k.Operators {
			if !slices.Contains(fields, field) {
				t.Errorf("kind %s attests operators for %q, which is not a binding field", k.Kind, field)
			}
		}
		for _, field := range fields {
			if len(k.Operators[field]) == 0 {
				t.Errorf("kind %s binding field %q has no attested operator", k.Kind, field)
			}
		}
	}
}

func TestEventScopeAllowsMessage(t *testing.T) {
	tests := []struct {
		name      string
		scope     eventScope
		channel   string
		mentioned bool
		isDM      bool
		want      bool
	}{
		{name: "empty scope allows a channel", channel: "C1", want: true},
		{name: "empty scope refuses DMs", channel: "D1", isDM: true, want: false},
		{name: "DMs enabled", scope: eventScope{DirectMessages: true}, channel: "D1", isDM: true, want: true},
		{name: "DM ignores the allow-list", scope: eventScope{DirectMessages: true, Channels: []string{"C9"}}, channel: "D1", isDM: true, want: true},
		{name: "DM ignores mention-only", scope: eventScope{DirectMessages: true, MentionOnly: true}, channel: "D1", isDM: true, want: true},
		{name: "channel on the list", scope: eventScope{Channels: []string{"C1", "C2"}}, channel: "C2", want: true},
		{name: "channel off the list", scope: eventScope{Channels: []string{"C1"}}, channel: "C2", want: false},
		{name: "mention-only refuses an unmentioned message", scope: eventScope{MentionOnly: true}, channel: "C1", want: false},
		{name: "mention-only allows a mention", scope: eventScope{MentionOnly: true}, channel: "C1", mentioned: true, want: true},
		{name: "mention-only on a listed channel", scope: eventScope{MentionOnly: true, Channels: []string{"C1"}}, channel: "C1", mentioned: true, want: true},
		{name: "slash and shortcut flags do not filter messages", scope: eventScope{SlashCommands: true, Shortcuts: true}, channel: "C1", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.scope.allowsMessage(tt.channel, tt.mentioned, tt.isDM); got != tt.want {
				t.Errorf("allowsMessage = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScopeStorePersists(t *testing.T) {
	dir := t.TempDir()
	store, err := newScopeStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, known := store.current(); known {
		t.Fatal("a new store should have no scope")
	}
	want := eventScope{Channels: []string{"C1"}, DirectMessages: true}
	if err := store.set(want); err != nil {
		t.Fatal(err)
	}
	reopened, err := newScopeStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, known := reopened.current()
	if !known || !reflect.DeepEqual(got, want) {
		t.Errorf("reopened scope = %+v (known %v), want %+v", got, known, want)
	}
}

func TestCaptureScopeRecordsAndForwards(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		want      eventScope
		wantKnown bool
	}{
		{name: "scope is recorded", body: `{"params":{"scope":{"channels":["C1"],"mention_only":true}}}`, want: eventScope{Channels: []string{"C1"}, MentionOnly: true}, wantKnown: true},
		{name: "absent scope is the empty scope", body: `{"params":{"kinds":["channel_message"]}}`, wantKnown: true},
		{name: "not json is left to the handler", body: `not json`},
		{name: "scope of the wrong shape is ignored", body: `{"params":{"scope":"all"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := newScopeStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var forwarded string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				forwarded = string(b)
			})
			captureScope(next, store).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body)))

			if forwarded != tt.body {
				t.Errorf("handler saw body %q, want %q", forwarded, tt.body)
			}
			got, known := store.current()
			if known != tt.wantKnown || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("scope = %+v (known %v), want %+v (known %v)", got, known, tt.want, tt.wantKnown)
			}
		})
	}
}

// recordingPublisher stands in for the events handler.
type recordingPublisher struct {
	mu     sync.Mutex
	events []events.Event
	err    error
}

func (p *recordingPublisher) Publish(_ context.Context, e events.Event) (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return 0, p.err
	}
	p.events = append(p.events, e)
	return uint64(len(p.events)), nil
}

func (p *recordingPublisher) ids() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ids []string
	for _, e := range p.events {
		ids = append(ids, e.ID)
	}
	return ids
}

const authTestOK = `{"ok":true,"user_id":"U07BOT","bot_id":"B07BOT","team_id":"T1"}`

type sourceFixture struct {
	src   *eventSource
	pub   *recordingPublisher
	slack *fakeSlack
	dir   string
}

func newSourceFixture(t *testing.T, slackResponses map[string]string) *sourceFixture {
	t.Helper()
	slackAPI := newFakeSlack(t, slackResponses)
	dir := t.TempDir()
	_, p, err := newServer(&fakeHost{credentials: testCredentials}, slackAPI.URL+"/api/", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.events.close() })
	pub := &recordingPublisher{}
	p.events.pub = pub
	return &sourceFixture{src: p.events, pub: pub, slack: slackAPI, dir: dir}
}

func (f *sourceFixture) subscribe(t *testing.T, scope eventScope) {
	t.Helper()
	if err := f.src.scope.set(scope); err != nil {
		t.Fatal(err)
	}
}

func eventsAPIEvent(inner any, innerType string) socketmode.Event {
	return socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{
			Type: slackevents.CallbackEvent, TeamID: "T1",
			InnerEvent: slackevents.EventsAPIInnerEvent{Type: innerType, Data: inner},
		},
	}
}

func channelMessage(channel, user, text, ts string) socketmode.Event {
	return eventsAPIEvent(&slackevents.MessageEvent{Channel: channel, ChannelType: "channel", User: user, Text: text, TimeStamp: ts}, "message")
}

func TestHandleEventsAPI(t *testing.T) {
	tests := []struct {
		name    string
		scope   *eventScope // nil = no host has ever listened
		evt     socketmode.Event
		wantIDs []string
	}{
		{name: "in scope", scope: &eventScope{}, evt: channelMessage("C1", "U1", "hi", "1.1"), wantIDs: []string{"msg:C1:1.1"}},
		{name: "no host has subscribed yet", evt: channelMessage("C1", "U1", "hi", "1.1")},
		{name: "off the channel list", scope: &eventScope{Channels: []string{"C2"}}, evt: channelMessage("C1", "U1", "hi", "1.1")},
		{name: "own post", scope: &eventScope{}, evt: channelMessage("C1", testBotUser, "I did a thing", "1.1")},
		{
			name: "DM refused until enabled", scope: &eventScope{},
			evt: eventsAPIEvent(&slackevents.MessageEvent{Channel: "D1", ChannelType: "im", User: "U1", Text: "hi", TimeStamp: "2.2"}, "message"),
		},
		{
			name: "DM when enabled", scope: &eventScope{DirectMessages: true},
			evt:     eventsAPIEvent(&slackevents.MessageEvent{Channel: "D1", ChannelType: "im", User: "U1", Text: "hi", TimeStamp: "2.2"}, "message"),
			wantIDs: []string{"msg:D1:2.2"},
		},
		{
			name: "mention-only drops the unmentioned and keeps the mention", scope: &eventScope{MentionOnly: true},
			evt:     eventsAPIEvent(&slackevents.AppMentionEvent{Channel: "C1", User: "U1", Text: "<@U07BOT> hi", TimeStamp: "3.3"}, "app_mention"),
			wantIDs: []string{"msg:C1:3.3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSourceFixture(t, map[string]string{"auth.test": authTestOK})
			if tt.scope != nil {
				f.subscribe(t, *tt.scope)
			}
			if _, err := f.src.handleEventsAPI(t.Context(), tt.evt); err != nil {
				t.Fatalf("handleEventsAPI: %v", err)
			}
			if got := f.pub.ids(); !slices.Equal(got, tt.wantIDs) {
				t.Errorf("published %v, want %v", got, tt.wantIDs)
			}
		})
	}
}

func TestSelfTriggerGuardFailsClosedWithoutAnIdentity(t *testing.T) {
	f := newSourceFixture(t, map[string]string{"auth.test": `{"ok":false,"error":"invalid_auth"}`})
	f.subscribe(t, eventScope{})

	_, err := f.src.handleEventsAPI(t.Context(), channelMessage("C1", testBotUser, "my own post", "1.1"))
	if err == nil {
		t.Fatal("expected an error when the bot's identity cannot be resolved")
	}
	if ids := f.pub.ids(); len(ids) != 0 {
		t.Errorf("published %v without knowing who the bot is", ids)
	}
}

func TestBotIdentityIsCachedPerToken(t *testing.T) {
	f := newSourceFixture(t, map[string]string{"auth.test": authTestOK})
	f.subscribe(t, eventScope{})
	for i, ts := range []string{"1.1", "1.2", "1.3"} {
		if _, err := f.src.handleEventsAPI(t.Context(), channelMessage("C1", "U1", "m"+string(rune('a'+i)), ts)); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.slack.calls("auth.test"); n != 1 {
		t.Errorf("auth.test calls = %d, want 1", n)
	}
}

func TestSlashCommandAndShortcutsIgnoreChannelScope(t *testing.T) {
	f := newSourceFixture(t, map[string]string{"auth.test": authTestOK})
	f.subscribe(t, eventScope{Channels: []string{"C-OTHER"}, MentionOnly: true})

	if _, err := f.src.handleSlashCommand(t.Context(), socketmode.Event{Data: slack.SlashCommand{Command: "/x", ChannelID: "C1", TriggerID: "t1"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.src.handleShortcut(t.Context(), slack.InteractionCallback{Type: slack.InteractionTypeShortcut, TriggerID: "t2"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"cmd:C1:t1", "shortcut:t2"}; !slices.Equal(f.pub.ids(), want) {
		t.Errorf("published %v, want %v", f.pub.ids(), want)
	}
}

func TestNothingIsEmittedBeforeAHostSubscribes(t *testing.T) {
	f := newSourceFixture(t, map[string]string{"auth.test": authTestOK})
	if _, err := f.src.handleSlashCommand(t.Context(), socketmode.Event{Data: slack.SlashCommand{TriggerID: "t1"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.src.handleShortcut(t.Context(), slack.InteractionCallback{Type: slack.InteractionTypeShortcut, TriggerID: "t2"}); err != nil {
		t.Fatal(err)
	}
	if ids := f.pub.ids(); len(ids) != 0 {
		t.Errorf("published %v with no subscriber", ids)
	}
}

func TestPublishFailureIsReturnedSoItIsLogged(t *testing.T) {
	f := newSourceFixture(t, map[string]string{"auth.test": authTestOK})
	f.subscribe(t, eventScope{})
	f.pub.err = errors.New("disk full")
	if _, err := f.src.handleEventsAPI(t.Context(), channelMessage("C1", "U1", "hi", "1.1")); err == nil {
		t.Error("expected the publish failure to surface")
	}
}

func TestMalformedEnvelopeDataIsAnError(t *testing.T) {
	f := newSourceFixture(t, nil)
	if _, err := f.src.handleEventsAPI(t.Context(), socketmode.Event{Data: "nope"}); err == nil {
		t.Error("events_api: expected an error")
	}
	if _, err := f.src.handleSlashCommand(t.Context(), socketmode.Event{Data: "nope"}); err == nil {
		t.Error("slash_commands: expected an error")
	}
}

// TestPublishedEventsAreDurableAndReplayable proves the real buffer and file
// store are wired: an event published by one process is there for the next.
func TestPublishedEventsAreDurableAndReplayable(t *testing.T) {
	slackAPI := newFakeSlack(t, map[string]string{"auth.test": authTestOK})
	dir := t.TempDir()
	host := &fakeHost{credentials: testCredentials}
	_, first, err := newServer(host, slackAPI.URL+"/api/", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.events.scope.set(eventScope{}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.events.handleEventsAPI(t.Context(), channelMessage("C1", "U1", "durable", "9.9")); err != nil {
		t.Fatal(err)
	}
	if err := first.events.close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := events.NewFileStore(dir+"/events", eventRetention)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	stored, err := restarted.Since(t.Context(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].ID != "msg:C1:9.9" || stored[0].Type != kindChannelMessage {
		t.Errorf("stored events = %+v", stored)
	}

	// A restarted plugin keeps filtering by the scope the host last sent.
	_, second, err := newServer(host, slackAPI.URL+"/api/", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.events.close() })
	if _, known := second.events.scope.current(); !known {
		t.Error("scope was not restored after a restart")
	}
}

func TestListenRequestRecordsItsScope(t *testing.T) {
	slackAPI := newFakeSlack(t, nil)
	srv, p, err := newServer(&fakeHost{credentials: testCredentials}, slackAPI.URL+"/api/", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.events.close() })

	// A cursor the buffer cannot read is refused before any stream opens, so
	// the request returns; the scope it carried is recorded regardless.
	res := rpc(t, srv, "events/listen", map[string]any{
		"kinds": []string{"channel_message"}, "cursor": "not-a-cursor",
		"scope": map[string]any{"channels": []string{"C1"}, "direct_messages": true},
	})
	if res.Error == nil {
		t.Fatalf("expected the malformed cursor to be refused, got %s", res.Result)
	}
	got, known := p.events.scope.current()
	if want := (eventScope{Channels: []string{"C1"}, DirectMessages: true}); !known || !reflect.DeepEqual(got, want) {
		t.Errorf("scope = %+v (known %v), want %+v", got, known, want)
	}
}

func TestRouteInteractive(t *testing.T) {
	f := newSourceFixture(t, map[string]string{"auth.test": authTestOK})
	f.subscribe(t, eventScope{})

	var clicked []slack.InteractionType
	clicks := func(_ context.Context, evt socketmode.Event) (any, error) {
		cb, ok := evt.Data.(slack.InteractionCallback)
		if !ok {
			return nil, errors.New("not an interaction")
		}
		clicked = append(clicked, cb.Type)
		return nil, nil
	}
	route := routeInteractive(clicks, f.src)

	interaction := func(typ slack.InteractionType, trigger string) socketmode.Event {
		return socketmode.Event{Type: socketmode.EventTypeInteractive, Data: slack.InteractionCallback{Type: typ, TriggerID: trigger}}
	}
	for _, evt := range []socketmode.Event{
		interaction(slack.InteractionTypeShortcut, "t1"),
		interaction(slack.InteractionTypeMessageAction, "t2"),
		interaction(slack.InteractionTypeBlockActions, "t3"),
		interaction(slack.InteractionTypeViewSubmission, "t4"),
	} {
		if _, err := route(t.Context(), evt); err != nil {
			t.Fatalf("route %v: %v", evt.Data, err)
		}
	}
	if want := []string{"shortcut:t1", "msgshortcut::t2"}; !slices.Equal(f.pub.ids(), want) {
		t.Errorf("published %v, want %v", f.pub.ids(), want)
	}
	if want := []slack.InteractionType{slack.InteractionTypeBlockActions, slack.InteractionTypeViewSubmission}; !slices.Equal(clicked, want) {
		t.Errorf("click handler saw %v, want %v", clicked, want)
	}

	// A payload that is not an interaction at all still reaches the click
	// handler, whose rejection of it is unchanged.
	if _, err := route(t.Context(), socketmode.Event{Data: "garbage"}); err == nil {
		t.Error("expected the click handler's error for a malformed envelope")
	}
}

func TestRouteInteractiveKeepsTheClickPathFailClosed(t *testing.T) {
	click := newClickFixture(t)
	clicks := newClickHandler(click.plugin, failingAuthorizer{})
	route := routeInteractive(clicks.handle, click.plugin.events)

	if _, err := route(t.Context(), socketmode.Event{Data: 42}); err == nil {
		t.Error("expected an error for a payload that is not an interaction callback")
	}

	// A click the host cannot vouch for still settles nothing.
	if _, err := route(t.Context(), click.event(actionIDFor(click.taskID, "approve"), "U1")); err != nil {
		t.Fatal(err)
	}
	clicks.wait()
	task := decodeTask(t, rpc(t, click.srv, "tasks/get", map[string]any{"taskId": click.taskID}))
	if task.Status != "working" {
		t.Errorf("task status after an unverifiable click = %q, want working", task.Status)
	}
}
