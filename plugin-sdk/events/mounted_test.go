package events

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

// newComposedServer mounts a Handler plus one tool into a single
// mcpserver.Server, the shape a managed plugin that both emits events and
// serves tools runs.
func newComposedServer(t *testing.T, h *Handler) *httptest.Server {
	t.Helper()
	srv := mcpserver.NewServer("composed", "0.0.1")
	if err := srv.RegisterTool(mcpserver.Tool{
		Name: "ping",
		Handler: func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{}, nil
		},
	}); err != nil {
		t.Fatalf("RegisterTool: %v", err)
	}
	if err := srv.Mount(h); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if err := srv.MountStreaming(methodEventsListen, h.ListenHandler()); err != nil {
		t.Fatalf("MountStreaming: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

// postComposed posts a JSON-RPC request carrying the A4 headers the
// composed server requires of every non-streaming method.
func postComposed(t *testing.T, url, method string, params any) jsonrpcResponse {
	t.Helper()
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, paramsJSON)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("MCP-Protocol-Version", mcpserver.ProtocolVersion)
	req.Header.Set("Mcp-Method", method)
	resp, err := http.DefaultClient.Do(req)
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
	return out
}

func TestMounted_ServerDiscoverShowsToolsAndEventsDeclaration(t *testing.T) {
	h := NewHandler("src", nil, WithHeartbeat(7*time.Second), WithMaxBatch(25))
	ts := newComposedServer(t, h)

	resp := postComposed(t, ts.URL, "server/discover", map[string]any{
		"_meta": map[string]any{
			mcpserver.MetaKeyProtocolVersion:    mcpserver.ProtocolVersion,
			mcpserver.MetaKeyClientCapabilities: map[string]any{},
		},
	})
	if resp.Error != nil {
		t.Fatalf("server/discover error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("re-marshal result: %v", err)
	}
	var result struct {
		Capabilities struct {
			Tools      *json.RawMessage `json:"tools"`
			Extensions map[string]struct {
				Version     string `json:"version"`
				HeartbeatMs int64  `json:"heartbeatMs"`
				MaxBatch    int    `json:"maxBatch"`
			} `json:"extensions"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode result %s: %v", raw, err)
	}

	if result.Capabilities.Tools == nil {
		t.Errorf("tools capability missing from composed server/discover: %s", raw)
	}
	decl, ok := result.Capabilities.Extensions[ExtensionEvents]
	if !ok {
		t.Fatalf("%s not declared: %s", ExtensionEvents, raw)
	}
	if decl.Version != ExtensionVersion || decl.HeartbeatMs != 7000 || decl.MaxBatch != 25 {
		t.Errorf("declaration = %+v, want version %s heartbeatMs 7000 maxBatch 25", decl, ExtensionVersion)
	}
}

func TestMounted_EventsDiscoverAndToolsListCoexist(t *testing.T) {
	h := NewHandler("src", []Kind{{Kind: "thing.created"}})
	ts := newComposedServer(t, h)

	tests := []struct {
		method string
		want   string
	}{
		{"events/discover", "thing.created"},
		{"tools/list", "ping"},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			resp := postComposed(t, ts.URL, tc.method, map[string]any{})
			if resp.Error != nil {
				t.Fatalf("%s error: %+v", tc.method, resp.Error)
			}
			raw, _ := json.Marshal(resp.Result)
			if !strings.Contains(string(raw), tc.want) {
				t.Errorf("%s result %s does not mention %q", tc.method, raw, tc.want)
			}
		})
	}
}

func TestMounted_ListenStreamsThroughMux(t *testing.T) {
	h := NewHandler("src", []Kind{{Kind: "thing.created"}})
	ts := newComposedServer(t, h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, r := openListen(t, ctx, ts.URL, `5`, listenParams{Kinds: []string{"thing.created"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	// openListen returns once response headers arrive, and the handler
	// subscribes before writing them, so this publish cannot be missed.
	if _, err := h.Publish(ctx, Event{Type: "thing.created", ID: "a"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	frame := readNextMessage(t, r)
	if ev := decodeCloudEvent(t, frame.data); ev.ID != "a" || ev.Source != "src" {
		t.Errorf("delivered event = %+v, want id a from source src", ev)
	}

	h.Close("test over")
	closeFrame := readNextMessage(t, r)
	if !strings.Contains(closeFrame.data, `"reason":"test over"`) {
		t.Errorf("close frame = %s, want reason test over", closeFrame.data)
	}
}

func TestMount_RejectsDuplicateListenMount(t *testing.T) {
	h := NewHandler("src", nil)
	srv := mcpserver.NewServer("composed", "0.0.1")
	if err := srv.Mount(h); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if err := srv.Mount(h); err == nil {
		t.Fatal("second Mount of the same Handler succeeded, want a duplicate-extension error")
	}
}
