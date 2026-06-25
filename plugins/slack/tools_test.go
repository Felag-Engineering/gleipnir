package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/slack-go/slack"

	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
)

// slackOKResponse returns a JSON-encoded Slack API success body. Most Slack
// API endpoints include ok:true and method-specific fields. Additional fields
// can be added via the extra map.
func slackOKResponse(extra map[string]any) []byte {
	m := map[string]any{"ok": true}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

// slackErrResponse returns a JSON-encoded Slack API error body for the
// given error code string.
func slackErrResponse(code string) []byte {
	b, _ := json.Marshal(map[string]any{"ok": false, "error": code})
	return b
}

// readForm parses the form body from an HTTP request. Slack API calls use
// form-encoded POST bodies with the token embedded as "token=<value>".
func readForm(r *http.Request) url.Values {
	body, _ := io.ReadAll(r.Body)
	v, _ := url.ParseQuery(string(body))
	return v
}

const testToken = "xoxb-test"

// credsJSON is the StoredCredentials JSON shape the fake host returns.
const credsJSON = `{"strategy":"oauth2_authcode","token":{"access_token":"` + testToken + `","token_type":"Bearer"}}`

// ── Test matrix ──────────────────────────────────────────────────────────────

// toolCallCase describes one (tool, scenario) combination to exercise.
type toolCallCase struct {
	name string
	// handler responds to Slack API requests from the tool handler.
	// Nil means no Slack HTTP server is needed (credentials-level failures, etc.).
	handler func(t *testing.T, w http.ResponseWriter, r *http.Request)
	// hostOpts configure the fake host (credentials, instance config, etc.).
	hostOpts []plugintest.Option
	// toolName is the Slack tool to invoke.
	toolName string
	// inputJSON is the tool's input.
	inputJSON string
	// wantCode is the expected ErrorEnvelope code. Zero means success (no error).
	wantCode commonv1.ErrorCode
	// wantOutputKey is a JSON key expected in the successful output JSON.
	wantOutputKey string
	// wantMetric asserts exactly one metric was recorded (only checked when
	// wantCode == 0 or wantCode is a Slack API error, not for pre-dispatch failures).
	wantMetric bool
	// wantHealthUnhealthy asserts SetHealthState was called with UNHEALTHY.
	wantHealthUnhealthy bool
	// wantHealthDetail is the expected detail string on the health update.
	wantHealthDetail string
}

func toolCases(t *testing.T) []toolCallCase {
	t.Helper()
	return []toolCallCase{
		// ── post_message happy path ──────────────────────────────────────────
		{
			name: "post_message_happy",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if !strings.HasSuffix(r.URL.Path, "chat.postMessage") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				form := readForm(r)
				if form.Get("token") != testToken {
					t.Errorf("token: want %q, got %q", testToken, form.Get("token"))
				}
				if form.Get("channel") != "C123" {
					t.Errorf("channel: want C123, got %q", form.Get("channel"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackOKResponse(map[string]any{
					"channel": "C123",
					"ts":      "1700000000.123456",
				}))
			},
			hostOpts:      []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:      "post_message",
			inputJSON:     `{"channel":"C123","text":"hello world"}`,
			wantOutputKey: "ts",
			wantMetric:    true,
		},
		// ── list_channels happy path ─────────────────────────────────────────
		{
			name: "list_channels_happy",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if !strings.HasSuffix(r.URL.Path, "conversations.list") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackOKResponse(map[string]any{
					"channels": []map[string]any{
						{"id": "C001", "name": "general", "is_private": false, "is_archived": false, "num_members": 42},
					},
					"response_metadata": map[string]any{"next_cursor": ""},
				}))
			},
			hostOpts:      []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:      "list_channels",
			inputJSON:     `{}`,
			wantOutputKey: "channels",
			wantMetric:    true,
		},
		// ── react happy path ─────────────────────────────────────────────────
		{
			name: "react_happy",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if !strings.HasSuffix(r.URL.Path, "reactions.add") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				form := readForm(r)
				if form.Get("name") != "thumbsup" {
					t.Errorf("name: want thumbsup, got %q", form.Get("name"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackOKResponse(nil))
			},
			hostOpts:      []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:      "react",
			inputJSON:     `{"channel":"C123","timestamp":"1700000000.123456","name":"thumbsup"}`,
			wantOutputKey: "ok",
			wantMetric:    true,
		},
		// ── set_topic happy path ─────────────────────────────────────────────
		{
			name: "set_topic_happy",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if !strings.HasSuffix(r.URL.Path, "conversations.setTopic") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				form := readForm(r)
				if form.Get("topic") != "my topic" {
					t.Errorf("topic: want %q, got %q", "my topic", form.Get("topic"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackOKResponse(map[string]any{
					"channel": map[string]any{
						"id": "C123",
						"topic": map[string]any{
							"value":    "my topic",
							"creator":  "",
							"last_set": 0,
						},
					},
				}))
			},
			hostOpts:      []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:      "set_topic",
			inputJSON:     `{"channel":"C123","topic":"my topic"}`,
			wantOutputKey: "ok",
			wantMetric:    true,
		},
		// ── post_message channel_not_found → NOT_FOUND ───────────────────────
		{
			name: "post_message_invalid_channel",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackErrResponse("channel_not_found"))
			},
			hostOpts:   []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:   "post_message",
			inputJSON:  `{"channel":"CBAD","text":"hello"}`,
			wantCode:   commonv1.ErrorCode_ERROR_CODE_NOT_FOUND,
			wantMetric: true,
		},
		// ── invalid_auth → PERMISSION + UNHEALTHY "auth_expired" ─────────────
		{
			name: "persistent_auth",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackErrResponse("invalid_auth"))
			},
			hostOpts:            []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:            "post_message",
			inputJSON:           `{"channel":"C123","text":"hello"}`,
			wantCode:            commonv1.ErrorCode_ERROR_CODE_PERMISSION,
			wantMetric:          true,
			wantHealthUnhealthy: true,
			wantHealthDetail:    "auth_expired",
		},
		// ── empty credentials → PERMISSION + UNHEALTHY "auth_missing" ────────
		{
			name:                "missing_credentials",
			handler:             nil, // no Slack API call made
			hostOpts:            []plugintest.Option{plugintest.WithCredentialsJSON(`{}`)},
			toolName:            "post_message",
			inputJSON:           `{"channel":"C123","text":"hello"}`,
			wantCode:            commonv1.ErrorCode_ERROR_CODE_PERMISSION,
			wantMetric:          false, // fails before dispatch; no metric emitted
			wantHealthUnhealthy: true,
			wantHealthDetail:    "auth_missing",
		},
		// ── unknown tool → INVALID_ARG ────────────────────────────────────────
		{
			name:      "unknown_tool",
			handler:   nil, // no Slack API call made
			hostOpts:  []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:  "nonexistent_tool",
			inputJSON: `{}`,
			wantCode:  commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
			// Unknown tool returns before metric emission.
			wantMetric: false,
		},

		// ── post_message with Block Kit blocks ───────────────────────────────
		{
			name: "post_message_blocks",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if !strings.HasSuffix(r.URL.Path, "chat.postMessage") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				form := readForm(r)
				// The outbound form must carry a "blocks" field when Block Kit is used.
				if form.Get("blocks") == "" {
					t.Errorf("expected blocks field in outbound form, got empty")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackOKResponse(map[string]any{
					"channel": "C123",
					"ts":      "1700000000.123456",
				}))
			},
			hostOpts:      []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:      "post_message",
			inputJSON:     `{"channel":"C123","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"hello"}}]}`,
			wantOutputKey: "ts",
			wantMetric:    true,
		},

		// ── post_message with object-shaped blocks (pre-flight → INVALID_ARG, no chat.postMessage call) ──
		{
			name: "post_message_malformed_blocks",
			// auth.test must succeed (handled by newSlackMux catch-all in TestToolCalls);
			// the pre-flight check fires inside dispatch() before any chat.postMessage call.
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				// This catch-all should never be hit for chat.postMessage.
				if strings.HasSuffix(r.URL.Path, "chat.postMessage") {
					t.Errorf("chat.postMessage should not be called when blocks are malformed; got path: %s", r.URL.Path)
				}
				// Respond with an error so any unexpected call is visible in the test output.
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackErrResponse("unexpected_call"))
			},
			hostOpts:   []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:   "post_message",
			inputJSON:  `{"channel":"C123","blocks":{"blocks":[]}}`,
			wantCode:   commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
			wantMetric: true, // dispatch is reached; metric is emitted
		},

		// ── read_thread happy path ───────────────────────────────────────────
		{
			name: "read_thread_happy",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if !strings.HasSuffix(r.URL.Path, "conversations.replies") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				form := readForm(r)
				if form.Get("channel") != "C123" {
					t.Errorf("channel: want C123, got %q", form.Get("channel"))
				}
				if form.Get("ts") != "1700000001.000000" {
					t.Errorf("ts: want 1700000001.000000, got %q", form.Get("ts"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackOKResponse(map[string]any{
					"messages": []map[string]any{
						{"text": "parent message", "user": "U001", "ts": "1700000001.000000", "thread_ts": "1700000001.000000"},
						{"text": "reply one", "user": "U002", "ts": "1700000002.000000", "thread_ts": "1700000001.000000"},
					},
					"has_more":          false,
					"response_metadata": map[string]any{"next_cursor": ""},
				}))
			},
			hostOpts:      []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:      "read_thread",
			inputJSON:     `{"channel":"C123","ts":"1700000001.000000"}`,
			wantOutputKey: "messages",
			wantMetric:    true,
		},

		// ── read_thread thread_not_found → NOT_FOUND ─────────────────────────
		{
			name: "read_thread_not_found",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackErrResponse("thread_not_found"))
			},
			hostOpts:   []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:   "read_thread",
			inputJSON:  `{"channel":"C123","ts":"1700000001.000000"}`,
			wantCode:   commonv1.ErrorCode_ERROR_CODE_NOT_FOUND,
			wantMetric: true,
		},

		// ── read_history happy path ──────────────────────────────────────────
		{
			name: "read_history_happy",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if !strings.HasSuffix(r.URL.Path, "conversations.history") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				form := readForm(r)
				if form.Get("channel") != "C123" {
					t.Errorf("channel: want C123, got %q", form.Get("channel"))
				}
				// Default limit of 100 should be sent.
				if form.Get("limit") != "100" {
					t.Errorf("limit: want 100, got %q", form.Get("limit"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackOKResponse(map[string]any{
					"messages": []map[string]any{
						{"text": "recent message", "user": "U001", "ts": "1700000010.000000"},
					},
					"has_more":          false,
					"response_metadata": map[string]any{"next_cursor": ""},
				}))
			},
			hostOpts:      []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:      "read_history",
			inputJSON:     `{"channel":"C123"}`,
			wantOutputKey: "messages",
			wantMetric:    true,
		},

		// ── update_message happy path ────────────────────────────────────────
		{
			name: "update_message_happy",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if !strings.HasSuffix(r.URL.Path, "chat.update") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				form := readForm(r)
				// blocks should be in the outbound form when supplied.
				if form.Get("blocks") == "" {
					t.Errorf("expected blocks field in outbound form, got empty")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackOKResponse(map[string]any{
					"channel": "C123",
					"ts":      "1700000001.000000",
					"text":    "",
				}))
			},
			hostOpts:      []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:      "update_message",
			inputJSON:     `{"channel":"C123","ts":"1700000001.000000","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"updated"}}]}`,
			wantOutputKey: "text",
			wantMetric:    true,
		},

		// ── update_message message_not_found → NOT_FOUND ─────────────────────
		{
			name: "update_message_not_found",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackErrResponse("message_not_found"))
			},
			hostOpts:   []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:   "update_message",
			inputJSON:  `{"channel":"C123","ts":"1700000001.000000","text":"updated"}`,
			wantCode:   commonv1.ErrorCode_ERROR_CODE_NOT_FOUND,
			wantMetric: true,
		},

		// ── update_message with object-shaped blocks (pre-flight → INVALID_ARG, no chat.update call) ──
		{
			name: "update_message_malformed_blocks",
			// Same pattern: auth.test is served by the catch-all; chat.update must not be called.
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if strings.HasSuffix(r.URL.Path, "chat.update") {
					t.Errorf("chat.update should not be called when blocks are malformed; got path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackErrResponse("unexpected_call"))
			},
			hostOpts:   []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:   "update_message",
			inputJSON:  `{"channel":"C123","ts":"1700000001.000000","blocks":{"blocks":[]}}`,
			wantCode:   commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
			wantMetric: true,
		},

		// ── update_message Slack-side invalid_blocks → INVALID_ARG ───────────
		// Distinct from the pre-flight case above: here the JSON is a well-formed
		// array, but Slack rejects the block content. Exercises the invalid_blocks
		// → INVALID_ARG mapping added to classifySlackErrCode.
		{
			name: "update_message_invalid_blocks_server_side",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackErrResponse("invalid_blocks"))
			},
			hostOpts:   []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:   "update_message",
			inputJSON:  `{"channel":"C123","ts":"1700000001.000000","blocks":[{"type":"divider"}]}`,
			wantCode:   commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
			wantMetric: true,
		},

		// ── delete_message happy path ────────────────────────────────────────
		{
			name: "delete_message_happy",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if !strings.HasSuffix(r.URL.Path, "chat.delete") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				form := readForm(r)
				if form.Get("channel") != "C123" {
					t.Errorf("channel: want C123, got %q", form.Get("channel"))
				}
				if form.Get("ts") != "1700000001.000000" {
					t.Errorf("ts: want 1700000001.000000, got %q", form.Get("ts"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackOKResponse(map[string]any{
					"channel": "C123",
					"ts":      "1700000001.000000",
				}))
			},
			hostOpts:      []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:      "delete_message",
			inputJSON:     `{"channel":"C123","ts":"1700000001.000000"}`,
			wantOutputKey: "channel",
			wantMetric:    true,
		},

		// ── delete_message error mapping ─────────────────────────────────────
		{
			name: "delete_message_not_found",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackErrResponse("message_not_found"))
			},
			hostOpts:   []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:   "delete_message",
			inputJSON:  `{"channel":"C123","ts":"1700000001.000000"}`,
			wantCode:   commonv1.ErrorCode_ERROR_CODE_NOT_FOUND,
			wantMetric: true,
		},

		// ── lookup_user happy path ───────────────────────────────────────────
		{
			name: "lookup_user_happy",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				if !strings.HasSuffix(r.URL.Path, "users.info") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackOKResponse(map[string]any{
					"user": map[string]any{
						"id":        "U001TESTID",
						"name":      "alice",
						"real_name": "Alice Smith",
						"tz":        "America/New_York",
					},
				}))
			},
			hostOpts:      []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:      "lookup_user",
			inputJSON:     `{"user":"U001TESTID"}`,
			wantOutputKey: "real_name",
			wantMetric:    true,
		},

		// ── lookup_user user_not_found → NOT_FOUND ────────────────────────────
		{
			name: "lookup_user_not_found",
			handler: func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				t.Helper()
				w.Header().Set("Content-Type", "application/json")
				w.Write(slackErrResponse("user_not_found"))
			},
			hostOpts:   []plugintest.Option{plugintest.WithCredentialsJSON(credsJSON)},
			toolName:   "lookup_user",
			inputJSON:  `{"user":"UBAD"}`,
			wantCode:   commonv1.ErrorCode_ERROR_CODE_NOT_FOUND,
			wantMetric: true,
		},
	}
}

// TestPostMessageRateLimit verifies that callWithRetry retries once on a 429
// with Retry-After: 0 and that the tool ultimately succeeds. Exactly 2 requests
// must hit the fake Slack backend for /chat.postMessage (auth.test is excluded).
func TestPostMessageRateLimit(t *testing.T) {
	var postCalls int32
	mux := newSlackMux(true)
	mux.HandleFunc("/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&postCalls, 1)
		if n == 1 {
			// First request: 429 with Retry-After: 0 so RetryAfter == 0 and fits any budget.
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// Second request: success.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"ok":true,"channel":"C123","ts":"1.234"}`)
	})
	backend := httptest.NewServer(mux)
	defer backend.Close()

	host := plugintest.NewFakeHost(plugintest.WithCredentialsJSON(credsJSON))
	svcs, cleanup := setupAllWithHost(t, host, backend)
	defer cleanup()

	resp, err := svcs.tool.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "post_message",
		InputJson: `{"channel":"C123","text":"hello"}`,
	})
	if err != nil {
		t.Fatalf("Call RPC error: %v", err)
	}

	// The tool must succeed after the retry.
	if resp.GetError() != nil {
		t.Errorf("expected success after retry, got error: code=%v message=%q",
			resp.GetError().GetCode(), resp.GetError().GetMessage())
	}

	// Verify the output contains the expected keys.
	var out map[string]any
	if err := json.Unmarshal([]byte(resp.GetOutputJson()), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if _, ok := out["ts"]; !ok {
		t.Errorf("output JSON missing key %q; got: %s", "ts", resp.GetOutputJson())
	}

	// Exactly 2 /chat.postMessage requests: initial 429 + 1 retry. auth.test
	// is not counted because it hits a separate mux entry.
	if n := atomic.LoadInt32(&postCalls); n != 2 {
		t.Errorf("expected exactly 2 /chat.postMessage requests to fake backend, got %d", n)
	}
}

// TestReadThreadRateLimit verifies that callWithRetry retries once on a 429
// for read_thread and ultimately succeeds. Exactly 2 requests must hit the fake
// Slack backend for /conversations.replies (auth.test excluded).
func TestReadThreadRateLimit(t *testing.T) {
	var replyCalls int32
	mux := newSlackMux(true)
	mux.HandleFunc("/conversations.replies", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&replyCalls, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"ok":true,"messages":[{"text":"hi","user":"U1","ts":"1.0","thread_ts":"1.0"}],"has_more":false}`)
	})
	backend := httptest.NewServer(mux)
	defer backend.Close()

	host := plugintest.NewFakeHost(plugintest.WithCredentialsJSON(credsJSON))
	svcs, cleanup := setupAllWithHost(t, host, backend)
	defer cleanup()

	resp, err := svcs.tool.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "read_thread",
		InputJson: `{"channel":"C123","ts":"1.0"}`,
	})
	if err != nil {
		t.Fatalf("Call RPC error: %v", err)
	}
	if resp.GetError() != nil {
		t.Errorf("expected success after retry, got error: code=%v message=%q",
			resp.GetError().GetCode(), resp.GetError().GetMessage())
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resp.GetOutputJson()), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if _, ok := out["messages"]; !ok {
		t.Errorf("output JSON missing key %q; got: %s", "messages", resp.GetOutputJson())
	}
	if n := atomic.LoadInt32(&replyCalls); n != 2 {
		t.Errorf("expected exactly 2 /conversations.replies requests, got %d", n)
	}
}

// TestUpdateMessageEmptyTextShape verifies that update_message always includes
// the "text" key in the output even when Slack echoes an empty string.
// This documents the shape contract for callers.
func TestUpdateMessageEmptyTextShape(t *testing.T) {
	mux := newSlackMux(true)
	mux.HandleFunc("/chat.update", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Slack returns an empty "text" when only blocks are provided.
		fmt.Fprintln(w, `{"ok":true,"channel":"C123","ts":"1.0","text":""}`)
	})
	backend := httptest.NewServer(mux)
	defer backend.Close()

	host := plugintest.NewFakeHost(plugintest.WithCredentialsJSON(credsJSON))
	svcs, cleanup := setupAllWithHost(t, host, backend)
	defer cleanup()

	resp, err := svcs.tool.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "update_message",
		InputJson: `{"channel":"C123","ts":"1.0","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"hi"}}]}`,
	})
	if err != nil {
		t.Fatalf("Call RPC error: %v", err)
	}
	if resp.GetError() != nil {
		t.Errorf("expected success, got error: %v", resp.GetError())
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resp.GetOutputJson()), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	// "text" key must always be present, even when Slack returns "".
	if _, ok := out["text"]; !ok {
		t.Errorf("output JSON missing key %q (shape contract requires it even when empty); got: %s", "text", resp.GetOutputJson())
	}
}

// TestLookupUserOutputKeys verifies that lookup_user returns id, name, real_name,
// and tz keys in the output JSON.
func TestLookupUserOutputKeys(t *testing.T) {
	mux := newSlackMux(true)
	mux.HandleFunc("/users.info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"ok":true,"user":{"id":"U001","name":"bob","real_name":"Bob Jones","tz":"Europe/London"}}`)
	})
	backend := httptest.NewServer(mux)
	defer backend.Close()

	host := plugintest.NewFakeHost(plugintest.WithCredentialsJSON(credsJSON))
	svcs, cleanup := setupAllWithHost(t, host, backend)
	defer cleanup()

	resp, err := svcs.tool.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "lookup_user",
		InputJson: `{"user":"U001"}`,
	})
	if err != nil {
		t.Fatalf("Call RPC error: %v", err)
	}
	if resp.GetError() != nil {
		t.Errorf("expected success, got error: %v", resp.GetError())
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resp.GetOutputJson()), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	for _, key := range []string{"id", "name", "real_name", "tz"} {
		if _, ok := out[key]; !ok {
			t.Errorf("output JSON missing key %q; got: %s", key, resp.GetOutputJson())
		}
	}
	if out["id"] != "U001" {
		t.Errorf("id: want U001, got %v", out["id"])
	}
	if out["tz"] != "Europe/London" {
		t.Errorf("tz: want Europe/London, got %v", out["tz"])
	}
}

// TestReadThreadOrderedShape verifies that read_thread returns messages in the
// order Slack provides them and that the output includes next_cursor/has_more.
func TestReadThreadOrderedShape(t *testing.T) {
	mux := newSlackMux(true)
	mux.HandleFunc("/conversations.replies", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"ok":true,"messages":[`+
			`{"text":"first","user":"U1","ts":"1.0","thread_ts":"1.0"},`+
			`{"text":"second","user":"U2","ts":"2.0","thread_ts":"1.0"},`+
			`{"text":"third","user":"U3","ts":"3.0","thread_ts":"1.0"}`+
			`],"has_more":true,"response_metadata":{"next_cursor":"abc123"}}`)
	})
	backend := httptest.NewServer(mux)
	defer backend.Close()

	host := plugintest.NewFakeHost(plugintest.WithCredentialsJSON(credsJSON))
	svcs, cleanup := setupAllWithHost(t, host, backend)
	defer cleanup()

	resp, err := svcs.tool.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "read_thread",
		InputJson: `{"channel":"C1","ts":"1.0"}`,
	})
	if err != nil {
		t.Fatalf("Call RPC error: %v", err)
	}
	if resp.GetError() != nil {
		t.Errorf("expected success, got error: %v", resp.GetError())
	}

	var out struct {
		Messages []struct {
			Text string `json:"text"`
			User string `json:"user"`
			Ts   string `json:"ts"`
		} `json:"messages"`
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(resp.GetOutputJson()), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d", len(out.Messages))
	}
	// Verify order is preserved (Slack returns chronological order).
	if out.Messages[0].Text != "first" || out.Messages[1].Text != "second" || out.Messages[2].Text != "third" {
		t.Errorf("messages not in expected order; got texts: %v %v %v",
			out.Messages[0].Text, out.Messages[1].Text, out.Messages[2].Text)
	}
	if out.NextCursor != "abc123" {
		t.Errorf("next_cursor: want abc123, got %q", out.NextCursor)
	}
	if !out.HasMore {
		t.Error("has_more: want true, got false")
	}
}

// TestToolCalls is the primary table-driven test for all Slack tool call scenarios.
// Each case spins up (optionally) an httptest.Server acting as the fake Slack API,
// runs a single Call() through the full gRPC stack, and asserts error code, output
// shape, metric recording, and health state.
func TestToolCalls(t *testing.T) {
	for _, tc := range toolCases(t) {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Optionally start a fake Slack API server. Use newSlackMux so the
			// auth.test gate introduced in Call() always has a valid /auth.test
			// route; the per-case handler is registered at "/" (catch-all).
			var backend *httptest.Server
			if tc.handler != nil {
				mux := newSlackMux(true)
				mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					tc.handler(t, w, r)
				}))
				backend = httptest.NewServer(mux)
				defer backend.Close()
			}

			host := plugintest.NewFakeHost(tc.hostOpts...)
			svcs, cleanup := setupAllWithHost(t, host, backend)
			defer cleanup()

			resp, err := svcs.tool.Call(context.Background(), &toolv1.CallRequest{
				ToolName:  tc.toolName,
				InputJson: tc.inputJSON,
			})
			if err != nil {
				t.Fatalf("Call RPC error: %v", err)
			}

			// Assert error code.
			if tc.wantCode == 0 {
				// Success: no error envelope.
				if resp.GetError() != nil {
					t.Errorf("expected success, got error: code=%v message=%q",
						resp.GetError().GetCode(), resp.GetError().GetMessage())
				}
				// Assert a known key appears in the output JSON.
				if tc.wantOutputKey != "" {
					var out map[string]any
					if err := json.Unmarshal([]byte(resp.GetOutputJson()), &out); err != nil {
						t.Fatalf("output is not valid JSON: %v", err)
					}
					if _, ok := out[tc.wantOutputKey]; !ok {
						t.Errorf("output JSON missing key %q; got: %s", tc.wantOutputKey, resp.GetOutputJson())
					}
				}
			} else {
				env := resp.GetError()
				if env == nil {
					t.Fatalf("expected error code %v, got success with output: %s",
						tc.wantCode, resp.GetOutputJson())
				}
				if env.GetCode() != tc.wantCode {
					t.Errorf("error code: want %v, got %v (message: %q)",
						tc.wantCode, env.GetCode(), env.GetMessage())
				}
			}

			// Assert metric recording.
			metrics := host.Metrics()
			if tc.wantMetric {
				if len(metrics) != 1 {
					t.Errorf("want exactly 1 metric, got %d", len(metrics))
				} else {
					m := metrics[0]
					if m.Name != "tool_call_last_duration_seconds" {
						t.Errorf("metric name: want %q, got %q", "tool_call_last_duration_seconds", m.Name)
					}
					if m.Labels["tool"] != tc.toolName {
						t.Errorf("metric label tool: want %q, got %q", tc.toolName, m.Labels["tool"])
					}
					if _, ok := m.Labels["outcome"]; !ok {
						t.Error("metric missing 'outcome' label")
					}
					// Plugin and instance labels must NOT be present (host injects them).
					for _, forbidden := range []string{"plugin", "instance"} {
						if _, ok := m.Labels[forbidden]; ok {
							t.Errorf("metric must not carry %q label (host-injected)", forbidden)
						}
					}
				}
			} else {
				if len(metrics) != 0 {
					t.Errorf("want 0 metrics, got %d", len(metrics))
				}
			}

			// Assert health state.
			if tc.wantHealthUnhealthy {
				state, detail, ok := host.Health()
				if !ok {
					t.Error("expected SetHealthState to be called, but no health state recorded")
				} else {
					if state != plugintest.HealthStateUnhealthy {
						t.Errorf("health state: want Unhealthy, got %v", state)
					}
					if detail != tc.wantHealthDetail {
						t.Errorf("health detail: want %q, got %q", tc.wantHealthDetail, detail)
					}
				}
			} else {
				_, _, ok := host.Health()
				if ok {
					t.Error("expected no SetHealthState call, but health state was recorded")
				}
			}
		})
	}
}

// TestToolCallMetricOutcomeLabel verifies that the "outcome" label is "ok" on
// success and "error" on failure, using post_message as the representative tool.
func TestToolCallMetricOutcomeLabel(t *testing.T) {
	t.Run("success_outcome_ok", func(t *testing.T) {
		mux := newSlackMux(true)
		mux.HandleFunc("/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(slackOKResponse(map[string]any{"channel": "C1", "ts": "1.0"}))
		})
		backend := httptest.NewServer(mux)
		defer backend.Close()

		host := plugintest.NewFakeHost(plugintest.WithCredentialsJSON(credsJSON))
		svcs, cleanup := setupAllWithHost(t, host, backend)
		defer cleanup()

		_, _ = svcs.tool.Call(context.Background(), &toolv1.CallRequest{
			ToolName:  "post_message",
			InputJson: `{"channel":"C1","text":"hi"}`,
		})

		metrics := host.Metrics()
		if len(metrics) != 1 {
			t.Fatalf("want 1 metric, got %d", len(metrics))
		}
		if metrics[0].Labels["outcome"] != "ok" {
			t.Errorf("outcome label: want %q, got %q", "ok", metrics[0].Labels["outcome"])
		}
	})

	t.Run("error_outcome_error", func(t *testing.T) {
		mux := newSlackMux(true)
		mux.HandleFunc("/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(slackErrResponse("channel_not_found"))
		})
		backend := httptest.NewServer(mux)
		defer backend.Close()

		host := plugintest.NewFakeHost(plugintest.WithCredentialsJSON(credsJSON))
		svcs, cleanup := setupAllWithHost(t, host, backend)
		defer cleanup()

		_, _ = svcs.tool.Call(context.Background(), &toolv1.CallRequest{
			ToolName:  "post_message",
			InputJson: `{"channel":"CBAD","text":"hi"}`,
		})

		metrics := host.Metrics()
		if len(metrics) != 1 {
			t.Fatalf("want 1 metric, got %d", len(metrics))
		}
		if metrics[0].Labels["outcome"] != "error" {
			t.Errorf("outcome label: want %q, got %q", "error", metrics[0].Labels["outcome"])
		}
	})
}

// TestToolCallCtxCancel verifies that a cancelled context surfaces as UNAVAILABLE
// from the tool call. We cancel immediately before the Call to ensure the Slack
// HTTP client sees a canceled context.
func TestToolCallCtxCancel(t *testing.T) {
	// Backend that blocks until the request context is done. /auth.test gets
	// its own route via newSlackMux so the gate isn't what blocks; the
	// cancellation surfaces either at the gRPC layer or from the context-
	// aware HTTP call in auth.test or in the tool dispatch's PostMessage.
	mux := newSlackMux(true)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.Header().Set("Content-Type", "application/json")
		w.Write(slackErrResponse("context_canceled"))
	}))
	backend := httptest.NewServer(mux)
	defer backend.Close()

	host := plugintest.NewFakeHost(plugintest.WithCredentialsJSON(credsJSON))
	svcs, cleanup := setupAllWithHost(t, host, backend)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	resp, err := svcs.tool.Call(ctx, &toolv1.CallRequest{
		ToolName:  "post_message",
		InputJson: `{"channel":"C123","text":"hello"}`,
	})
	// Either a gRPC-level error or an in-band UNAVAILABLE envelope is acceptable
	// for a cancelled context.
	if err != nil {
		// gRPC-level cancellation — acceptable.
		return
	}
	if resp.GetError() == nil {
		t.Fatal("expected error (gRPC-level or in-band), got success")
	}
	got := resp.GetError().GetCode()
	if got != commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE {
		t.Errorf("expected UNAVAILABLE, got %v", got)
	}
}

// TestMapErr verifies the error classification table for the most important
// Slack error strings. This tests mapErr directly without going through gRPC.
// We use real slack-go error types (SlackErrorResponse, StatusCodeError, RateLimitedError)
// so that errors.As in mapErr unwraps them correctly.
func TestMapErr(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantCode   commonv1.ErrorCode
		wantHealth healthHint
	}{
		{
			name:       "channel_not_found",
			err:        slackAPIError("channel_not_found"),
			wantCode:   commonv1.ErrorCode_ERROR_CODE_NOT_FOUND,
			wantHealth: healthNone,
		},
		{
			name:       "invalid_arguments",
			err:        slackAPIError("invalid_arguments"),
			wantCode:   commonv1.ErrorCode_ERROR_CODE_INVALID_ARG,
			wantHealth: healthNone,
		},
		{
			name:       "invalid_auth",
			err:        slackAPIError("invalid_auth"),
			wantCode:   commonv1.ErrorCode_ERROR_CODE_PERMISSION,
			wantHealth: healthAuthExpired,
		},
		{
			// missing_scope is split out of the auth-expired bucket because
			// re-authorizing the plugin with the same Slack app cannot fix it —
			// the operator must add the missing scope in the Slack app's OAuth
			// & Permissions page first.
			name:       "missing_scope",
			err:        slackAPIError("missing_scope"),
			wantCode:   commonv1.ErrorCode_ERROR_CODE_PERMISSION,
			wantHealth: healthMissingScope,
		},
		{
			name:       "token_revoked",
			err:        slackAPIError("token_revoked"),
			wantCode:   commonv1.ErrorCode_ERROR_CODE_PERMISSION,
			wantHealth: healthAuthExpired,
		},
		{
			name:       "fatal_error",
			err:        slackAPIError("fatal_error"),
			wantCode:   commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE,
			wantHealth: healthNone,
		},
		{
			name:       "context_canceled",
			err:        context.Canceled,
			wantCode:   commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE,
			wantHealth: healthNone,
		},
		{
			name:       "context_deadline_exceeded",
			err:        context.DeadlineExceeded,
			wantCode:   commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE,
			wantHealth: healthNone,
		},
		{
			name:       "rate_limited_typed",
			err:        &slack.RateLimitedError{RetryAfter: time.Second},
			wantCode:   commonv1.ErrorCode_ERROR_CODE_RATE_LIMITED,
			wantHealth: healthNone,
		},
		{
			name:       "status_code_5xx",
			err:        slack.StatusCodeError{Code: 503},
			wantCode:   commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE,
			wantHealth: healthNone,
		},
		{
			name:       "status_code_4xx",
			err:        slack.StatusCodeError{Code: 400},
			wantCode:   commonv1.ErrorCode_ERROR_CODE_INTERNAL,
			wantHealth: healthNone,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			code, hint := mapErr(tc.err)
			if code != tc.wantCode {
				t.Errorf("code: want %v, got %v", tc.wantCode, code)
			}
			if hint != tc.wantHealth {
				t.Errorf("healthHint: want %v, got %v", tc.wantHealth, hint)
			}
		})
	}
}

// slackAPIError constructs a slack.SlackErrorResponse with the given Slack
// API error code string. mapErr uses errors.As to match this type, so we
// must use the real type from the slack-go library.
func slackAPIError(code string) error {
	return slack.SlackErrorResponse{Err: code}
}

// setupAllWithHost is a variant of setupAll that accepts a pre-constructed
// FakeHost, allowing tests to inspect metrics and health state after the call.
func setupAllWithHost(t *testing.T, host *plugintest.FakeHost, slackBackend *httptest.Server) (*services, func()) {
	t.Helper()

	// Start the host gRPC server.
	hostLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for host: %v", err)
	}
	hostSrv := grpc.NewServer()
	host.Register(hostSrv)
	go func() { _ = hostSrv.Serve(hostLis) }()

	hostConn, err := grpc.NewClient(hostLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	hostClient := hostv1.NewHostServiceClient(hostConn)

	var toolSvc *ToolService
	if slackBackend != nil {
		toolSvc = NewToolService(hostClient, slackBackend.Client(), slackBackend.URL+"/")
	} else {
		toolSvc = NewToolService(hostClient, nil, "")
	}

	toolLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for tool: %v", err)
	}
	toolSrv := grpc.NewServer()
	toolv1.RegisterToolServiceServer(toolSrv, toolSvc)
	go func() { _ = toolSrv.Serve(toolLis) }()
	toolConn, err := grpc.NewClient(toolLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial tool: %v", err)
	}

	svcs := &services{
		tool: toolv1.NewToolServiceClient(toolConn),
	}
	cleanup := func() {
		toolConn.Close()
		toolSrv.Stop()
		hostConn.Close()
		hostSrv.Stop()
	}
	return svcs, cleanup
}
