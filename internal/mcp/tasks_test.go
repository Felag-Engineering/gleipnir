package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// tasksFakeResponder is a minimal, dedicated JSON-RPC fake for the Tasks
// extension. FakeMCPServer (fakeserver.go) is deliberately not extended here:
// it is the shared protocol-era (discover/tools) fixture and already carries
// a documented extension contract for THAT concern; Tasks is a separate
// wire surface with its own small, fixed response shape, so a standalone
// handler is more direct than growing FakeMCPServer's option surface for a
// single new method family.
type tasksFakeResponder struct {
	// respond returns the JSON-RPC "result" object (as a Go value marshaled
	// directly) for one request, or a non-2xx status with a JSON-RPC error
	// envelope when errCode != 0.
	respond func(method string, taskID string, params json.RawMessage) (result any, errCode int, errMsg string)

	requests []taskFakeRequest
}

// taskFakeRequest records one request tasksFakeResponder received.
type taskFakeRequest struct {
	Method string
	TaskID string
	Params json.RawMessage
	Header http.Header
}

// RequestsFor returns every recorded request whose JSON-RPC method equals
// method, in arrival order.
func (f *tasksFakeResponder) RequestsFor(method string) []taskFakeRequest {
	var out []taskFakeRequest
	for _, r := range f.requests {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

func (f *tasksFakeResponder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var decoded struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var taskParams struct {
		TaskID string `json:"taskId"`
	}
	json.Unmarshal(decoded.Params, &taskParams) //nolint:errcheck

	f.requests = append(f.requests, taskFakeRequest{
		Method: decoded.Method, TaskID: taskParams.TaskID, Params: decoded.Params, Header: r.Header.Clone(),
	})

	result, errCode, errMsg := f.respond(decoded.Method, taskParams.TaskID, decoded.Params)

	w.Header().Set("Content-Type", "application/json")
	if errCode != 0 {
		// A JSON-RPC application-level error for an ordinary method call (as
		// opposed to server/discover's era-detection status codes) rides HTTP
		// 200 with an "error" envelope — client.go's post() rejects any
		// non-2xx status before the JSON-RPC body is ever inspected, so a 4xx
		// here would surface as an *HTTPStatusError instead of the
		// *JSONRPCError this fixture means to drive.
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"jsonrpc": "2.0",
			"id":      2,
			"error":   map[string]any{"code": errCode, "message": errMsg},
		})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"jsonrpc": "2.0",
		"id":      2,
		"result":  result,
	})
}

// newModernClient returns a Client pinned to the 2026-07-28 transport,
// targeting srv.
func newModernClient(srv *httptest.Server) *Client {
	return NewClient(srv.URL, WithProtocolVersion(ProtocolVersion20260728))
}

func TestClient_GetTask_GatedOnModernProtocol(t *testing.T) {
	tests := []struct {
		name string
		pin  string
	}{
		{name: "unpinned/never-probed", pin: ""},
		{name: "legacy pin", pin: ProtocolVersionLegacy},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient("http://example.invalid", WithProtocolVersion(tc.pin))
			if _, err := c.GetTask(context.Background(), "task-1"); err == nil {
				t.Fatal("GetTask: expected an error on a non-modern client, got nil")
			}
		})
	}
}

func TestClient_GetTask_HappyPath(t *testing.T) {
	fake := &tasksFakeResponder{
		respond: func(method, taskID string, params json.RawMessage) (any, int, string) {
			if method != methodTasksGet {
				t.Fatalf("unexpected method %q", method)
			}
			return map[string]any{
				"taskId":         taskID,
				"status":         "working",
				"pollIntervalMs": 5000,
				"ttlMs":          60000,
			}, 0, ""
		},
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	c := newModernClient(srv)
	status, err := c.GetTask(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if status.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want task-1", status.TaskID)
	}
	if status.Status != TaskStatusWorking {
		t.Errorf("Status = %q, want %q", status.Status, TaskStatusWorking)
	}
	if status.PollInterval.Seconds() != 5 {
		t.Errorf("PollInterval = %v, want 5s", status.PollInterval)
	}
	if status.TTL.Seconds() != 60 {
		t.Errorf("TTL = %v, want 60s", status.TTL)
	}

	if len(fake.requests) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(fake.requests))
	}
	if got := fake.requests[0].Header.Get("Mcp-Name"); got != "task-1" {
		t.Errorf("Mcp-Name header = %q, want task-1", got)
	}
	if got := fake.requests[0].Header.Get("Mcp-Method"); got != methodTasksGet {
		t.Errorf("Mcp-Method header = %q, want %q", got, methodTasksGet)
	}
}

func TestClient_GetTask_PollIntervalAndTTLClamping(t *testing.T) {
	tests := []struct {
		name           string
		pollIntervalMs any
		ttlMs          any
		wantPoll       time.Duration
		wantTTL        time.Duration
	}{
		{
			name:           "zero pollIntervalMs floors to minimum",
			pollIntervalMs: 0,
			ttlMs:          1000,
			wantPoll:       minTaskPollInterval,
			wantTTL:        time.Second,
		},
		{
			name:           "absurd pollIntervalMs ceilings",
			pollIntervalMs: int64(1) << 40,
			ttlMs:          1000,
			wantPoll:       maxTaskPollInterval,
			wantTTL:        time.Second,
		},
		{
			name:           "absurd ttlMs ceilings",
			pollIntervalMs: 5000,
			ttlMs:          int64(1) << 50,
			wantPoll:       5 * time.Second,
			wantTTL:        maxTaskTTL,
		},
		{
			name:           "negative values treated as absent",
			pollIntervalMs: -1,
			ttlMs:          -1,
			wantPoll:       0,
			wantTTL:        0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &tasksFakeResponder{
				respond: func(method, taskID string, params json.RawMessage) (any, int, string) {
					return map[string]any{
						"taskId":         taskID,
						"status":         "working",
						"pollIntervalMs": tc.pollIntervalMs,
						"ttlMs":          tc.ttlMs,
					}, 0, ""
				},
			}
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)

			status, err := newModernClient(srv).GetTask(context.Background(), "task-1")
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if status.PollInterval != tc.wantPoll {
				t.Errorf("PollInterval = %v, want %v", status.PollInterval, tc.wantPoll)
			}
			if status.TTL != tc.wantTTL {
				t.Errorf("TTL = %v, want %v", status.TTL, tc.wantTTL)
			}
		})
	}
}

func TestClient_GetTask_MissingStatusIsAStructuralError(t *testing.T) {
	fake := &tasksFakeResponder{
		respond: func(method, taskID string, params json.RawMessage) (any, int, string) {
			return map[string]any{"taskId": taskID}, 0, "" // no "status" key at all
		},
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	if _, err := newModernClient(srv).GetTask(context.Background(), "task-1"); err == nil {
		t.Fatal("GetTask: expected an error for a response with no status, got nil")
	}
}

func TestClient_GetTask_JSONRPCErrorPropagates(t *testing.T) {
	fake := &tasksFakeResponder{
		respond: func(method, taskID string, params json.RawMessage) (any, int, string) {
			return nil, -32602, "unknown taskId"
		},
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	_, err := newModernClient(srv).GetTask(context.Background(), "task-1")
	if err == nil {
		t.Fatal("GetTask: expected an error, got nil")
	}
	var rpcErr *JSONRPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("GetTask error is not a *JSONRPCError: %v", err)
	}
}

func TestClient_CancelTask_HappyPath(t *testing.T) {
	fake := &tasksFakeResponder{
		respond: func(method, taskID string, params json.RawMessage) (any, int, string) {
			if method != methodTasksCancel {
				t.Fatalf("unexpected method %q", method)
			}
			return map[string]any{"taskId": taskID, "status": "cancelled"}, 0, ""
		},
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	status, err := newModernClient(srv).CancelTask(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if status.Status != TaskStatusCancelled {
		t.Errorf("Status = %q, want %q", status.Status, TaskStatusCancelled)
	}
}

func TestClient_UpdateTask_SendsInputResponses(t *testing.T) {
	fake := &tasksFakeResponder{
		respond: func(method, taskID string, params json.RawMessage) (any, int, string) {
			if method != methodTasksUpdate {
				t.Fatalf("unexpected method %q", method)
			}
			return map[string]any{"taskId": taskID, "status": "working", "pollIntervalMs": 1000}, 0, ""
		},
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	responses := []InputResponse{{Action: "accept", Content: json.RawMessage(`{"confirmed":true}`)}}
	if _, err := newModernClient(srv).UpdateTask(context.Background(), "task-1", responses); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	if len(fake.requests) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(fake.requests))
	}
	if !strings.Contains(string(fake.requests[0].Params), "inputResponses") {
		t.Errorf("request params missing inputResponses: %s", fake.requests[0].Params)
	}
	var params tasksUpdateParams
	if err := json.Unmarshal(fake.requests[0].Params, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if len(params.InputResponses) != 1 || params.InputResponses[0].Action != "accept" {
		t.Errorf("InputResponses = %+v, want one accept entry", params.InputResponses)
	}
}

func TestTaskStatusValue_Terminal(t *testing.T) {
	tests := []struct {
		status TaskStatusValue
		want   bool
	}{
		{TaskStatusWorking, false},
		{TaskStatusInputRequired, false},
		{TaskStatusCompleted, true},
		{TaskStatusFailed, true},
		{TaskStatusCancelled, true},
		{TaskStatusValue("unknown"), false},
	}
	for _, tc := range tests {
		if got := tc.status.Terminal(); got != tc.want {
			t.Errorf("%q.Terminal() = %v, want %v", tc.status, got, tc.want)
		}
	}
}
