package events

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// sseFrame is one parsed SSE dispatch: either a comment (heartbeat) frame or
// an "event: message" / "data: ..." frame.
type sseFrame struct {
	comment string
	event   string
	data    string
}

// readSSEFrame reads lines from r until the blank line terminating one SSE
// dispatch, per the SSE spec's field-per-line framing.
func readSSEFrame(r *bufio.Reader) (sseFrame, error) {
	var f sseFrame
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return f, err
		}
		line = strings.TrimRight(line, "\n")
		if line == "" {
			return f, nil
		}
		switch {
		case strings.HasPrefix(line, ":"):
			f.comment = strings.TrimPrefix(line, ": ")
		case strings.HasPrefix(line, "event: "):
			f.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

// readNextMessage reads SSE frames, discarding heartbeat comment frames,
// and returns the first "event: message" frame.
func readNextMessage(t *testing.T, r *bufio.Reader) sseFrame {
	t.Helper()
	for {
		f, err := readSSEFrame(r)
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		if f.comment != "" {
			continue
		}
		return f
	}
}

// openListen POSTs an events/listen request and returns the still-open
// response plus a buffered reader over its body. The caller owns closing
// resp.Body (via defer or t.Cleanup) and canceling ctx to simulate a client
// disconnect.
func openListen(t *testing.T, ctx context.Context, url, rawID string, params listenParams) (*http.Response, *bufio.Reader) {
	t.Helper()
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal listen params: %v", err)
	}
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"events/listen","params":%s}`, rawID, paramsJSON)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build events/listen request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("events/listen request: %v", err)
	}
	return resp, bufio.NewReader(resp.Body)
}

// postJSONRPC issues a plain (non-streaming) JSON-RPC request and returns
// the decoded response.
func postJSONRPC(t *testing.T, url, rawID, method string, params any) jsonrpcResponse {
	t.Helper()
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s params: %v", method, err)
	}
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"%s","params":%s}`, rawID, method, paramsJSON)

	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("%s request: %v", method, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s response: %v", method, err)
	}
	var out jsonrpcResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s response %s: %v", method, raw, err)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("%s Content-Type = %q, want application/json", method, got)
	}
	return out
}

// notificationEnvelope is the JSON-RPC notification wrapper an
// events/event frame's data carries.
type notificationEnvelope struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  cloudEventWire `json:"params"`
}

// decodeCloudEvent decodes an SSE frame's data as an events/event
// notification and returns the CloudEvents envelope it carried.
func decodeCloudEvent(t *testing.T, data string) cloudEventWire {
	t.Helper()
	var env notificationEnvelope
	if err := json.Unmarshal([]byte(data), &env); err != nil {
		t.Fatalf("decode events/event notification %s: %v", data, err)
	}
	if env.Method != methodEventsEvent {
		t.Fatalf("notification method = %q, want %q", env.Method, methodEventsEvent)
	}
	if bytes.Contains([]byte(data), []byte(`"id"`)) {
		// A notification MUST NOT carry a top-level "id" — that would make
		// it a request/response instead (JSON-RPC 2.0 §4).
		var raw map[string]json.RawMessage
		_ = json.Unmarshal([]byte(data), &raw)
		if _, hasID := raw["id"]; hasID {
			t.Fatalf("events/event notification carries a top-level id, want none: %s", data)
		}
	}
	return env.Params
}

// waitFor polls cond until it reports true or timeout elapses, failing the
// test if the deadline passes first. Used only where no event the system
// already publishes can be synchronized on instead — e.g. proving a
// server-side goroutine has exited after a client disconnect, which has no
// externally observable completion signal beyond polling internal state.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}
