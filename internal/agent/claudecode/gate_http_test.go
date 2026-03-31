package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rapp992/gleipnir/internal/model"
)

// postJSONRPC sends a JSON-RPC request to url and returns the decoded response.
func postJSONRPC(t *testing.T, url string, req jsonrpcRequest) jsonrpcResponse {
	t.Helper()

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(url, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}

	var result jsonrpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

// newTestGate creates and starts an HTTP gate with the given grants.
// Returns the gate URL and a shutdown function.
func newTestGate(t *testing.T, grants map[string]ToolGrant) (string, func()) {
	t.Helper()

	bridge := &recordingBridge{}
	approvalCh := make(chan bool, 1)

	cfg := GateConfig{
		RunID:      "test-run",
		ToolGrants: grants,
		Bridge:     bridge.bridge(),
		ApprovalCh: approvalCh,
	}
	gate := NewGateServer(bytes.NewReader(nil), io.Discard, cfg)

	ctx := context.Background()
	url, shutdown, err := StartHTTPGate(ctx, gate)
	if err != nil {
		t.Fatalf("StartHTTPGate: %v", err)
	}
	t.Cleanup(shutdown)
	return url, shutdown
}

func TestStartHTTPGate_Initialize(t *testing.T) {
	url, _ := newTestGate(t, nil)

	resp := postJSONRPC(t, url, jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	})

	if resp.Error != nil {
		t.Fatalf("expected no error, got: %+v", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}

	protocolVersion, ok := result["protocolVersion"].(string)
	if !ok || protocolVersion == "" {
		t.Fatalf("expected non-empty protocolVersion, got: %v", result["protocolVersion"])
	}
}

func TestStartHTTPGate_ToolsList(t *testing.T) {
	url, _ := newTestGate(t, nil)

	resp := postJSONRPC(t, url, jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/list",
	})

	if resp.Error != nil {
		t.Fatalf("expected no error, got: %+v", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}

	tools, ok := result["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("expected non-empty tools list, got: %v", result["tools"])
	}

	// Verify the gleipnir_gate tool is present.
	toolMap, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected tool shape: %T", tools[0])
	}
	if toolMap["name"] != "gleipnir_gate" {
		t.Fatalf("expected tool name gleipnir_gate, got: %v", toolMap["name"])
	}
}

func TestStartHTTPGate_ToolsCall_AllowedTool(t *testing.T) {
	grants := map[string]ToolGrant{
		"mcp__myserver__read_data": {Approval: model.ApprovalModeNone},
	}
	url, _ := newTestGate(t, grants)

	args, _ := json.Marshal(gateArguments{
		ToolName: "mcp__myserver__read_data",
		Input:    map[string]any{"path": "/tmp"},
	})
	params, _ := json.Marshal(toolCallParams{
		Name:      "gleipnir_gate",
		Arguments: args,
	})

	resp := postJSONRPC(t, url, jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("expected no error, got: %+v", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}

	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("unexpected content shape: %v", result["content"])
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected block shape: %T", content[0])
	}
	textStr, _ := block["text"].(string)
	if !strings.Contains(textStr, `"allow"`) {
		t.Fatalf("expected behavior allow, got: %s", textStr)
	}
}

func TestStartHTTPGate_Notification(t *testing.T) {
	url, _ := newTestGate(t, nil)

	body, _ := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      nil, // notification: no ID
		Method:  "notifications/initialized",
	})

	resp, err := http.Post(url, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Notifications must not receive a JSON-RPC response body.
	respBody, _ := io.ReadAll(resp.Body)
	if len(strings.TrimSpace(string(respBody))) != 0 {
		t.Fatalf("expected empty body for notification, got: %q", respBody)
	}
}
