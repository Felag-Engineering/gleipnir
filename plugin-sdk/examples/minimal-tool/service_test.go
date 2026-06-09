package main

import (
	"context"
	"log/slog"
	"testing"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
	"github.com/felag-engineering/gleipnir/plugin-sdk/tool"
)

func TestEchoTool_Happy(t *testing.T) {
	h := plugintest.NewToolHarness(t, func(hc hostv1.HostServiceClient) tool.Service {
		return NewToolService(hc)
	},
		plugintest.WithInstanceConfigJSON(`{"greeting":"hi"}`),
		plugintest.WithRunContext(plugintest.RunContext{
			RunID:    "r-1",
			PolicyID: "p-1",
		}),
	)

	resp, err := h.Client.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "echo",
		InputJson: `{"message":"hello world"}`,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("unexpected error envelope: %v", resp.GetError().GetMessage())
	}
	if resp.GetOutputJson() == "" {
		t.Error("expected non-empty output_json")
	}

	h.Host.AssertMetricEmitted(t, "echo_calls_total", map[string]string{"tool": "echo"})
	h.Host.AssertLogContains(t, slog.LevelInfo, "echo handled")
}

func TestEchoTool_MalformedInput(t *testing.T) {
	h := plugintest.NewToolHarness(t, func(hc hostv1.HostServiceClient) tool.Service {
		return NewToolService(hc)
	})

	resp, err := h.Client.Call(context.Background(), &toolv1.CallRequest{
		ToolName:  "echo",
		InputJson: `not-valid-json`,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.GetError() == nil {
		t.Fatal("expected error envelope for malformed JSON, got nil")
	}

	h.Host.AssertNoMetricEmitted(t, "echo_calls_total")
}

func TestEchoTool_ListTools(t *testing.T) {
	h := plugintest.NewToolHarness(t, func(hc hostv1.HostServiceClient) tool.Service {
		return NewToolService(hc)
	})

	resp, err := h.Client.ListTools(context.Background(), &toolv1.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(resp.GetTools()) != 1 {
		t.Fatalf("want 1 tool, got %d", len(resp.GetTools()))
	}
	if resp.GetTools()[0].GetName() != "echo" {
		t.Errorf("want tool name 'echo', got %q", resp.GetTools()[0].GetName())
	}
}
