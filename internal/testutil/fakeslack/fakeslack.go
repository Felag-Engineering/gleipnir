// Package fakeslack is an in-process fake of the Slack Web API and Socket Mode
// protocol, for tests that drive a real slack-go client without network access.
//
// It is test-only: nothing outside *_test.go files may import it
// (TestNotLinkedIntoProduction enforces this). Every recorded call is exposed
// on a channel so tests synchronize on the event instead of polling.
package fakeslack

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/slack-go/slack"
)

// recordBuffer bounds each recording channel. A test that records more calls
// than this without draining gets a t.Errorf rather than a silent drop.
const recordBuffer = 1024

const (
	apiPrefix     = "/api/"
	socketPath    = "/socketmode"
	actionIDStart = "feedback_response:"
)

// PostedMessage is one chat.postMessage call.
type PostedMessage struct {
	Channel  string
	Text     string
	ThreadTS string
	// Blocks is the raw JSON of the "blocks" argument ("" when absent).
	Blocks string
	// TS is the timestamp the fake returned for the message.
	TS string
	// Form holds every argument exactly as received.
	Form url.Values
}

// Update is one chat.update call.
type Update struct {
	Channel string
	TS      string
	Text    string
	Blocks  string
	Form    url.Values
}

// Ephemeral is one chat.postEphemeral call.
type Ephemeral struct {
	Channel string
	User    string
	Text    string
	Blocks  string
	Form    url.Values
}

// Ack is a Socket Mode acknowledgement received from the client.
type Ack struct {
	EnvelopeID string
	Payload    json.RawMessage
}

// MethodHandler serves a Web API method. The returned map is the response
// body; "ok":true is added unless the map already carries an "ok" key. A
// non-empty errCode produces {"ok":false,"error":errCode} instead.
type MethodHandler func(args url.Values) (resp map[string]any, errCode string)

// Server is the fake. Create one with New.
type Server struct {
	t   testing.TB
	srv *httptest.Server

	posted     chan PostedMessage
	updates    chan Update
	ephemerals chan Ephemeral
	acks       chan Ack
	hellos     chan struct{}
	outbound   chan []byte

	mu       sync.Mutex
	handlers map[string]MethodHandler
	conns    map[*websocket.Conn]struct{}
	counter  int
	botUser  string
	channels []slack.Channel
	history  map[string][]slack.Message
	replies  map[string][]slack.Message
	users    []slack.User

	wg sync.WaitGroup
}

// New starts a fake and registers its shutdown with t.Cleanup.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		t:          t,
		posted:     make(chan PostedMessage, recordBuffer),
		updates:    make(chan Update, recordBuffer),
		ephemerals: make(chan Ephemeral, recordBuffer),
		acks:       make(chan Ack, recordBuffer),
		hellos:     make(chan struct{}, recordBuffer),
		outbound:   make(chan []byte, recordBuffer),
		handlers:   map[string]MethodHandler{},
		conns:      map[*websocket.Conn]struct{}{},
		botUser:    "UBOT",
		history:    map[string][]slack.Message{},
		replies:    map[string][]slack.Message{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc(apiPrefix, s.serveAPI)
	mux.HandleFunc(socketPath, s.serveSocket)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.Close)

	s.registerBuiltins()
	return s
}

// URL is the base URL for slack.OptionAPIURL (it ends in "/api/").
func (s *Server) URL() string { return s.srv.URL + apiPrefix }

// SocketURL is the ws:// URL apps.connections.open hands out.
func (s *Server) SocketURL() string {
	return "ws" + strings.TrimPrefix(s.srv.URL, "http") + socketPath
}

// Close stops the server and closes any open Socket Mode connections. It is
// idempotent and also runs from t.Cleanup.
func (s *Server) Close() {
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.srv.Close()
	s.wg.Wait()
}

// PostedMessages yields each chat.postMessage call in order.
func (s *Server) PostedMessages() <-chan PostedMessage { return s.posted }

// Updates yields each chat.update call in order.
func (s *Server) Updates() <-chan Update { return s.updates }

// Ephemerals yields each chat.postEphemeral call in order.
func (s *Server) Ephemerals() <-chan Ephemeral { return s.ephemerals }

// Acks yields each Socket Mode acknowledgement the client sent.
func (s *Server) Acks() <-chan Ack { return s.acks }

// Hellos receives one value each time a Socket Mode client connects and the
// hello frame has been written.
func (s *Server) Hellos() <-chan struct{} { return s.hellos }

// SetBotUserID sets the user_id auth.test reports.
func (s *Server) SetBotUserID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.botUser = id
}

// SetChannels sets the fixture served by conversations.list.
func (s *Server) SetChannels(chs []slack.Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels = chs
}

// SetHistory sets the fixture served by conversations.history for a channel.
func (s *Server) SetHistory(channel string, msgs []slack.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history[channel] = msgs
}

// SetReplies sets the fixture served by conversations.replies for a thread.
func (s *Server) SetReplies(channel, threadTS string, msgs []slack.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies[channel+"/"+threadTS] = msgs
}

// SetUsers sets the fixture served by users.info.
func (s *Server) SetUsers(users []slack.User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users = users
}

// HandleMethod registers (or replaces) the handler for a Web API method such
// as "conversations.list".
func (s *Server) HandleMethod(name string, h MethodHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[name] = h
}

// InjectEvent delivers an events_api envelope whose inner event is the given
// object (it must carry a "type", e.g. "message" or "app_mention"). Envelopes
// injected before a client connects are held until one does. It returns the
// envelope_id.
func (s *Server) InjectEvent(inner map[string]any) string {
	id := s.nextID("env")
	payload := map[string]any{
		"token":        "fake-verification-token",
		"team_id":      "TFAKE",
		"api_app_id":   "AFAKE",
		"type":         "event_callback",
		"event_id":     s.nextID("Ev"),
		"event_time":   s.nextCounter(),
		"event":        inner,
		"authed_users": []string{s.botUserID()},
	}
	s.send(map[string]any{
		"type":                     "events_api",
		"envelope_id":              id,
		"accepts_response_payload": false,
		"payload":                  payload,
	})
	return id
}

// InjectBlockAction delivers an interactive block_actions envelope for a
// button click whose action_id encodes taskID and optionID. It returns the
// envelope_id.
func (s *Server) InjectBlockAction(taskID, optionID, userID string) string {
	id := s.nextID("env")
	payload := map[string]any{
		"type":       "block_actions",
		"api_app_id": "AFAKE",
		"team":       map[string]any{"id": "TFAKE"},
		"user":       map[string]any{"id": userID, "username": "user-" + userID},
		"channel":    map[string]any{"id": "CFAKE"},
		"container":  map[string]any{"type": "message", "channel_id": "CFAKE", "message_ts": "1.000001"},
		"actions": []map[string]any{{
			"type":      "button",
			"action_id": ActionID(taskID, optionID),
			"block_id":  "fake-block",
			"value":     optionID,
			"action_ts": "1.000002",
		}},
	}
	s.send(map[string]any{
		"type":                     "interactive",
		"envelope_id":              id,
		"accepts_response_payload": false,
		"payload":                  payload,
	})
	return id
}

// ActionID returns the button action_id the Slack channel plugin emits for a
// request + option: "feedback_response:<requestID>:<optionID>". It is
// duplicated here on purpose so the fake does not import the plugin.
func ActionID(taskID, optionID string) string {
	return fmt.Sprintf("%s%s:%s", actionIDStart, taskID, optionID)
}

// ParseActionID reverses ActionID. ok is false for any other format.
func ParseActionID(actionID string) (taskID, optionID string, ok bool) {
	rest, found := strings.CutPrefix(actionID, actionIDStart)
	if !found {
		return "", "", false
	}
	idx := strings.LastIndex(rest, ":")
	if idx < 0 {
		return "", "", false
	}
	taskID, optionID = rest[:idx], rest[idx+1:]
	if taskID == "" || optionID == "" {
		return "", "", false
	}
	return taskID, optionID, true
}

func (s *Server) nextCounter() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	return s.counter
}

func (s *Server) nextID(prefix string) string {
	return fmt.Sprintf("%s-%06d", prefix, s.nextCounter())
}

func (s *Server) nextTS() string {
	return fmt.Sprintf("1700000000.%06d", s.nextCounter())
}

func (s *Server) botUserID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.botUser
}

func (s *Server) send(envelope map[string]any) {
	raw, err := json.Marshal(envelope)
	if err != nil {
		s.t.Errorf("fakeslack: marshal envelope: %v", err)
		return
	}
	select {
	case s.outbound <- raw:
	default:
		s.t.Errorf("fakeslack: outbound envelope buffer full")
	}
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, apiPrefix)
	args, err := parseArgs(r)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}

	s.mu.Lock()
	h, ok := s.handlers[method]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, map[string]any{"ok": false, "error": "unknown_method"})
		return
	}

	resp, errCode := h(args)
	if errCode != "" {
		writeJSON(w, map[string]any{"ok": false, "error": errCode})
		return
	}
	if resp == nil {
		resp = map[string]any{}
	}
	if _, has := resp["ok"]; !has {
		resp["ok"] = true
	}
	writeJSON(w, resp)
}

// parseArgs reads form-encoded or JSON bodies; slack-go uses both.
func parseArgs(r *http.Request) (url.Values, error) {
	args := url.Values{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) { // socket-mode open sends no body
			return nil, fmt.Errorf("decode json body: %w", err)
		}
		for k, v := range body {
			if str, isString := v.(string); isString {
				args.Set(k, str)
				continue
			}
			raw, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("re-encode %q: %w", k, err)
			}
			args.Set(k, string(raw))
		}
		return args, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("parse form: %w", err)
	}
	for k, v := range r.Form {
		args[k] = v
	}
	return args, nil
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) registerBuiltins() {
	s.HandleMethod("auth.test", func(url.Values) (map[string]any, string) {
		return map[string]any{
			"url":     "https://fake.slack.test/",
			"team":    "Fake",
			"team_id": "TFAKE",
			"user":    "gleipnir",
			"user_id": s.botUserID(),
			"bot_id":  "BFAKE",
		}, ""
	})

	s.HandleMethod("apps.connections.open", func(url.Values) (map[string]any, string) {
		return map[string]any{"url": s.SocketURL()}, ""
	})

	s.HandleMethod("conversations.open", func(args url.Values) (map[string]any, string) {
		users := args.Get("users")
		if users == "" {
			return nil, "users_list_not_supplied"
		}
		return map[string]any{"channel": map[string]any{"id": "D" + strings.ReplaceAll(users, ",", "_")}}, ""
	})

	s.HandleMethod("chat.postMessage", func(args url.Values) (map[string]any, string) {
		msg := PostedMessage{
			Channel:  args.Get("channel"),
			Text:     args.Get("text"),
			ThreadTS: args.Get("thread_ts"),
			Blocks:   args.Get("blocks"),
			TS:       s.nextTS(),
			Form:     args,
		}
		record(s, s.posted, msg)
		return map[string]any{"channel": msg.Channel, "ts": msg.TS}, ""
	})

	s.HandleMethod("chat.update", func(args url.Values) (map[string]any, string) {
		u := Update{
			Channel: args.Get("channel"),
			TS:      args.Get("ts"),
			Text:    args.Get("text"),
			Blocks:  args.Get("blocks"),
			Form:    args,
		}
		record(s, s.updates, u)
		return map[string]any{"channel": u.Channel, "ts": u.TS, "text": u.Text}, ""
	})

	s.HandleMethod("chat.postEphemeral", func(args url.Values) (map[string]any, string) {
		e := Ephemeral{
			Channel: args.Get("channel"),
			User:    args.Get("user"),
			Text:    args.Get("text"),
			Blocks:  args.Get("blocks"),
			Form:    args,
		}
		record(s, s.ephemerals, e)
		return map[string]any{"message_ts": s.nextTS()}, ""
	})

	s.HandleMethod("conversations.list", func(url.Values) (map[string]any, string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return map[string]any{
			"channels":          s.channels,
			"response_metadata": map[string]any{"next_cursor": ""},
		}, ""
	})

	s.HandleMethod("conversations.history", func(args url.Values) (map[string]any, string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return map[string]any{"messages": nonNil(s.history[args.Get("channel")]), "has_more": false}, ""
	})

	s.HandleMethod("conversations.replies", func(args url.Values) (map[string]any, string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return map[string]any{"messages": nonNil(s.replies[args.Get("channel")+"/"+args.Get("ts")]), "has_more": false}, ""
	})

	s.HandleMethod("users.info", func(args url.Values) (map[string]any, string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, u := range s.users {
			if u.ID == args.Get("user") {
				return map[string]any{"user": u}, ""
			}
		}
		return nil, "user_not_found"
	})
}

func nonNil(msgs []slack.Message) []slack.Message {
	if msgs == nil {
		return []slack.Message{}
	}
	return msgs
}

func record[T any](s *Server, ch chan<- T, v T) {
	select {
	case ch <- v:
	default:
		s.t.Errorf("fakeslack: recording buffer full; drain the channel")
	}
}

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func (s *Server) serveSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.conns[conn] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		_ = conn.Close()
		s.wg.Done()
	}()

	hello := map[string]any{
		"type":            "hello",
		"num_connections": 1,
		"connection_info": map[string]any{"app_id": "AFAKE"},
		"debug_info":      map[string]any{"host": "fakeslack", "approximate_connection_time": 18060},
	}
	if err := conn.WriteJSON(hello); err != nil {
		return
	}
	record(s, s.hellos, struct{}{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var ack struct {
				EnvelopeID string          `json:"envelope_id"`
				Payload    json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(raw, &ack); err != nil || ack.EnvelopeID == "" {
				continue
			}
			record(s, s.acks, Ack{EnvelopeID: ack.EnvelopeID, Payload: ack.Payload})
		}
	}()

	for {
		select {
		case <-done:
			return
		case raw := <-s.outbound:
			if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				return
			}
		}
	}
}
