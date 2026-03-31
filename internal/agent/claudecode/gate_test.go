package claudecode

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
)

// recordingBridge records bridge callback invocations for test assertions.
type recordingBridge struct {
	mu           sync.Mutex
	requestCalls []requestCall
	resumeCalls  int
	requestErr   error // injected to test error paths
}

type requestCall struct {
	ToolName string
	Input    map[string]any
}

func (b *recordingBridge) bridge() ApprovalBridge {
	return ApprovalBridge{
		RequestApproval: func(ctx context.Context, toolName string, proposedInput map[string]any) (string, error) {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.requestCalls = append(b.requestCalls, requestCall{ToolName: toolName, Input: proposedInput})
			return "test-approval-id", b.requestErr
		},
		ResumeRunning: func(ctx context.Context) error {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.resumeCalls++
			return nil
		},
	}
}

func (b *recordingBridge) requestCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.requestCalls)
}

func (b *recordingBridge) resumeCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.resumeCalls
}

// pipeGate creates a GateServer wired to in-memory pipes, starts the Run loop
// in a goroutine, and returns the write end of stdin and the read end of stdout
// along with a cancel func.
func pipeGate(t *testing.T, grants map[string]ToolGrant, bridge *recordingBridge, approvalCh chan bool) (*io.PipeWriter, *io.PipeReader, context.CancelFunc) {
	t.Helper()

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	cfg := GateConfig{
		RunID:      "run-test",
		ToolGrants: grants,
		Bridge:     bridge.bridge(),
		ApprovalCh: approvalCh,
	}
	gate := NewGateServer(stdinR, stdoutW, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = gate.Run(ctx)
		_ = stdoutW.Close()
	}()

	t.Cleanup(func() {
		cancel()
		_ = stdinW.Close()
	})

	return stdinW, stdoutR, cancel
}

// sendRequest writes a JSON-RPC request line to the pipe writer.
func sendRequest(t *testing.T, w *io.PipeWriter, method string, id json.RawMessage, params any) {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if id != nil {
		req["id"] = json.RawMessage(id)
	}
	if params != nil {
		req["params"] = params
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("sendRequest: marshal: %v", err)
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		t.Fatalf("sendRequest: write: %v", err)
	}
}

// readResponse reads one newline-terminated JSON line from the pipe reader and
// unmarshals it into a jsonrpcResponse.
func readResponse(t *testing.T, r *io.PipeReader) jsonrpcResponse {
	t.Helper()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[0])
			if tmp[0] == '\n' {
				break
			}
		}
		if err != nil {
			t.Fatalf("readResponse: read: %v", err)
		}
	}
	var resp jsonrpcResponse
	if err := json.Unmarshal(buf, &resp); err != nil {
		t.Fatalf("readResponse: unmarshal %q: %v", string(buf), err)
	}
	return resp
}

// extractText pulls the first content block's text from a tool result response.
func extractText(t *testing.T, resp jsonrpcResponse) string {
	t.Helper()
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("extractText: unmarshal result %q: %v", string(resp.Result), err)
	}
	if len(result.Content) == 0 {
		t.Fatal("extractText: no content blocks")
	}
	return result.Content[0].Text
}

// toolCallParams builds the tools/call params envelope for the gleipnir_gate tool.
func makeGateParams(toolName string, input map[string]any) map[string]any {
	return map[string]any{
		"name": "gleipnir_gate",
		"arguments": map[string]any{
			"toolName": toolName,
			"input":    input,
		},
	}
}

// --- TestGateServer_PermissionCheck ---

func TestGateServer_PermissionCheck(t *testing.T) {
	tests := []struct {
		name           string
		toolName       string
		grants         map[string]ToolGrant
		approvalSignal *bool // nil means don't send; pointer value is sent
		closeChBefore  bool
		wantBehavior   string
		wantMsgContain string
		wantRequests   int
		wantResumes    int
	}{
		{
			name:     "granted_no_approval",
			toolName: "mcp__s__t",
			grants: map[string]ToolGrant{
				"mcp__s__t": {Approval: model.ApprovalModeNone},
			},
			wantBehavior: "allow",
			wantRequests: 0,
			wantResumes:  0,
		},
		{
			name:     "granted_approval_approved",
			toolName: "mcp__s__t",
			grants: map[string]ToolGrant{
				"mcp__s__t": {Approval: model.ApprovalModeRequired},
			},
			approvalSignal: boolPtr(true),
			wantBehavior:   "allow",
			wantRequests:   1,
			wantResumes:    1,
		},
		{
			name:     "granted_approval_rejected",
			toolName: "mcp__s__t",
			grants: map[string]ToolGrant{
				"mcp__s__t": {Approval: model.ApprovalModeRequired},
			},
			approvalSignal: boolPtr(false),
			wantBehavior:   "deny",
			wantMsgContain: "rejected",
			wantRequests:   1,
			wantResumes:    0,
		},
		{
			name:     "not_granted",
			toolName: "mcp__s__unknown",
			grants:   map[string]ToolGrant{},
			wantBehavior:   "deny",
			wantMsgContain: "not granted",
			wantRequests:   0,
			wantResumes:    0,
		},
		{
			name:     "approval_timeout",
			toolName: "mcp__s__t",
			grants: map[string]ToolGrant{
				"mcp__s__t": {Approval: model.ApprovalModeRequired, Timeout: 50 * time.Millisecond},
			},
			// No approvalSignal — let the timeout fire.
			wantBehavior:   "deny",
			wantMsgContain: "timeout",
			wantRequests:   1,
			wantResumes:    0,
		},
		{
			name:     "closed_approval_channel",
			toolName: "mcp__s__t",
			grants: map[string]ToolGrant{
				"mcp__s__t": {Approval: model.ApprovalModeRequired},
			},
			closeChBefore:  true,
			wantBehavior:   "deny",
			wantRequests:   1,
			wantResumes:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			approvalCh := make(chan bool, 1)
			if tc.closeChBefore {
				close(approvalCh)
			}

			rb := &recordingBridge{}
			stdinW, stdoutR, _ := pipeGate(t, tc.grants, rb, approvalCh)

			// Send approval signal in background if needed.
			if tc.approvalSignal != nil {
				go func() {
					time.Sleep(10 * time.Millisecond)
					approvalCh <- *tc.approvalSignal
				}()
			}

			sendRequest(t, stdinW, "tools/call", json.RawMessage(`1`),
				makeGateParams(tc.toolName, map[string]any{"key": "val"}))

			// Use a deadline to catch hangs.
			done := make(chan jsonrpcResponse, 1)
			go func() { done <- readResponse(t, stdoutR) }()

			select {
			case resp := <-done:
				text := extractText(t, resp)
				var behavior map[string]any
				if err := json.Unmarshal([]byte(text), &behavior); err != nil {
					t.Fatalf("behavior JSON: %v (text=%q)", err, text)
				}
				if got := behavior["behavior"]; got != tc.wantBehavior {
					t.Errorf("behavior = %q, want %q", got, tc.wantBehavior)
				}
				if tc.wantMsgContain != "" {
					msg, _ := behavior["message"].(string)
					if !strings.Contains(msg, tc.wantMsgContain) {
						t.Errorf("message = %q, want it to contain %q", msg, tc.wantMsgContain)
					}
				}
				if got := rb.requestCallCount(); got != tc.wantRequests {
					t.Errorf("requestCalls = %d, want %d", got, tc.wantRequests)
				}
				if got := rb.resumeCallCount(); got != tc.wantResumes {
					t.Errorf("resumeCalls = %d, want %d", got, tc.wantResumes)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for response")
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// --- TestGateServer_Initialize ---

func TestGateServer_Initialize(t *testing.T) {
	rb := &recordingBridge{}
	stdinW, stdoutR, _ := pipeGate(t, nil, rb, nil)

	sendRequest(t, stdinW, "initialize", json.RawMessage(`1`), map[string]any{})
	resp := readResponse(t, stdoutR)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	// Verify the ID is echoed as the number 1.
	if string(resp.ID) != "1" {
		t.Errorf("id = %s, want 1", string(resp.ID))
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if got := result["protocolVersion"]; got != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", got)
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities missing tools key: %v", caps)
	}
	info, _ := result["serverInfo"].(map[string]any)
	if got := info["name"]; got != "gleipnir-gate" {
		t.Errorf("serverInfo.name = %v, want gleipnir-gate", got)
	}
}

// --- TestGateServer_InitializeStringID ---

func TestGateServer_InitializeStringID(t *testing.T) {
	rb := &recordingBridge{}
	stdinW, stdoutR, _ := pipeGate(t, nil, rb, nil)

	sendRequest(t, stdinW, "initialize", json.RawMessage(`"abc"`), map[string]any{})
	resp := readResponse(t, stdoutR)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	// json.RawMessage round-trips the string literal including quotes.
	if string(resp.ID) != `"abc"` {
		t.Errorf("id = %s, want \"abc\"", string(resp.ID))
	}
}

// --- TestGateServer_ToolsList ---

func TestGateServer_ToolsList(t *testing.T) {
	rb := &recordingBridge{}
	stdinW, stdoutR, _ := pipeGate(t, nil, rb, nil)

	sendRequest(t, stdinW, "tools/list", json.RawMessage(`2`), nil)
	resp := readResponse(t, stdoutR)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	var result struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Tools) != 1 {
		t.Fatalf("tools count = %d, want 1", len(result.Tools))
	}
	if result.Tools[0].Name != "gleipnir_gate" {
		t.Errorf("tool name = %q, want gleipnir_gate", result.Tools[0].Name)
	}
	props, _ := result.Tools[0].InputSchema["properties"].(map[string]any)
	if _, ok := props["toolName"]; !ok {
		t.Error("inputSchema missing toolName property")
	}
	if _, ok := props["input"]; !ok {
		t.Error("inputSchema missing input property")
	}
}

// --- TestGateServer_NotificationNoResponse ---

func TestGateServer_NotificationNoResponse(t *testing.T) {
	rb := &recordingBridge{}
	stdinW, stdoutR, _ := pipeGate(t, nil, rb, nil)

	// Send a notification first (no ID, no response expected).
	notif, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	})
	notif = append(notif, '\n')
	if _, err := stdinW.Write(notif); err != nil {
		t.Fatalf("write notification: %v", err)
	}

	// Then send a tools/list request.
	sendRequest(t, stdinW, "tools/list", json.RawMessage(`42`), nil)

	// The only response we should receive is the tools/list response.
	done := make(chan jsonrpcResponse, 1)
	go func() { done <- readResponse(t, stdoutR) }()

	select {
	case resp := <-done:
		// Should be the tools/list response (id == 42), not a notification response.
		if string(resp.ID) != "42" {
			t.Errorf("expected tools/list response with id=42, got id=%s", string(resp.ID))
		}
		if resp.Error != nil {
			t.Errorf("unexpected error: %+v", resp.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tools/list response")
	}
}

// --- TestGateServer_UnknownMethod ---

func TestGateServer_UnknownMethod(t *testing.T) {
	rb := &recordingBridge{}
	stdinW, stdoutR, _ := pipeGate(t, nil, rb, nil)

	sendRequest(t, stdinW, "foo/bar", json.RawMessage(`99`), nil)
	resp := readResponse(t, stdoutR)

	if resp.Error == nil {
		t.Fatal("expected error response, got nil")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", resp.Error.Code)
	}
	if string(resp.ID) != "99" {
		t.Errorf("id = %s, want 99", string(resp.ID))
	}
}

// --- TestGateServer_ContextCancellation ---

func TestGateServer_ContextCancellation(t *testing.T) {
	stdinR, _ := io.Pipe()
	_, stdoutW := io.Pipe()

	cfg := GateConfig{
		RunID:      "run-cancel",
		ToolGrants: map[string]ToolGrant{},
		Bridge:     (&recordingBridge{}).bridge(),
		ApprovalCh: make(chan bool),
	}
	gate := NewGateServer(stdinR, stdoutW, cfg)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- gate.Run(ctx) }()

	cancel()

	select {
	case err := <-done:
		// context.Canceled is the expected return value.
		if err != context.Canceled {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// --- TestGateServer_EOFShutdown ---

func TestGateServer_EOFShutdown(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	_, stdoutW := io.Pipe()

	cfg := GateConfig{
		RunID:      "run-eof",
		ToolGrants: map[string]ToolGrant{},
		Bridge:     (&recordingBridge{}).bridge(),
		ApprovalCh: make(chan bool),
	}
	gate := NewGateServer(stdinR, stdoutW, cfg)

	done := make(chan error, 1)
	go func() { done <- gate.Run(context.Background()) }()

	// Close stdin to trigger EOF.
	_ = stdinW.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil (clean EOF)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after stdin close")
	}
}
