package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
)

func TestResolveForPolicy_AllToolsFound(t *testing.T) {
	reg, _ := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "read_pods", "description": "list pods", "inputSchema": map[string]any{"type": "object"}},
		{"name": "delete_pod", "description": "delete a pod", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if err := reg.RegisterServer(context.Background(), "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "my-server.read_pods", Approval: model.ApprovalModeNone, Params: map[string]any{"namespace": "worker-01"}},
				{
					Tool:      "my-server.delete_pod",
					Approval:  model.ApprovalModeRequired,
					Timeout:   "30m",
					OnTimeout: model.OnTimeoutReject,
					Params:    map[string]any{"namespace": "worker-01"},
				},
			},
		},
	}

	result, err := reg.ResolveForPolicy(context.Background(), p)
	if err != nil {
		t.Fatalf("ResolveForPolicy: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}

	tool0 := result[0]
	if tool0.Role != model.CapabilityRoleTool {
		t.Errorf("result[0].Role = %q, want %q", tool0.Role, model.CapabilityRoleTool)
	}
	if tool0.ServerName != "my-server" {
		t.Errorf("result[0].ServerName = %q, want %q", tool0.ServerName, "my-server")
	}
	if tool0.ToolName != "read_pods" {
		t.Errorf("result[0].ToolName = %q, want %q", tool0.ToolName, "read_pods")
	}
	if tool0.Approval != model.ApprovalModeNone {
		t.Errorf("result[0].Approval = %q, want %q", tool0.Approval, model.ApprovalModeNone)
	}
	if tool0.Timeout != 0 {
		t.Errorf("result[0].Timeout = %v, want 0", tool0.Timeout)
	}
	if tool0.Client == nil {
		t.Errorf("result[0].Client is nil")
	}

	tool1 := result[1]
	if tool1.Role != model.CapabilityRoleTool {
		t.Errorf("result[1].Role = %q, want %q", tool1.Role, model.CapabilityRoleTool)
	}
	if tool1.Approval != model.ApprovalModeRequired {
		t.Errorf("result[1].Approval = %q, want %q", tool1.Approval, model.ApprovalModeRequired)
	}
	if tool1.Timeout != 30*time.Minute {
		t.Errorf("result[1].Timeout = %v, want %v", tool1.Timeout, 30*time.Minute)
	}
	if tool1.OnTimeout != model.OnTimeoutReject {
		t.Errorf("result[1].OnTimeout = %q, want %q", tool1.OnTimeout, model.OnTimeoutReject)
	}
	if tool1.Client == nil {
		t.Errorf("result[1].Client is nil")
	}
}

func TestResolveForPolicy_MissingTool(t *testing.T) {
	reg, _ := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "read_pods", "description": "list pods", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if err := reg.RegisterServer(context.Background(), "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "my-server.read_pods", Approval: model.ApprovalModeNone},
				{Tool: "my-server.nonexistent_tool", Approval: model.ApprovalModeNone},
			},
		},
	}

	_, err := reg.ResolveForPolicy(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for missing tool, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent_tool") {
		t.Errorf("error %q does not mention the missing tool name", err.Error())
	}
}

func TestResolveForPolicy_EmptyCapabilities(t *testing.T) {
	reg, _ := newTestRegistry(t)

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{},
	}

	result, err := reg.ResolveForPolicy(context.Background(), p)
	if err != nil {
		t.Fatalf("ResolveForPolicy: unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("len(result) = %d, want 0", len(result))
	}
}

func TestResolveForPolicy_InvalidDotNotation(t *testing.T) {
	reg, _ := newTestRegistry(t)

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "nodot", Approval: model.ApprovalModeNone},
			},
		},
	}

	_, err := reg.ResolveForPolicy(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for invalid dot-notation, got nil")
	}
	// Error must mention dot-notation or server.tool format.
	if !strings.Contains(err.Error(), "dot-notation") && !strings.Contains(err.Error(), "server.tool") {
		t.Errorf("error %q does not mention dot-notation or server.tool format", err.Error())
	}
}

func TestResolveForPolicy_ServerNotFound(t *testing.T) {
	reg, _ := newTestRegistry(t)

	// No servers registered — any tool reference should fail.
	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "ghost-server.some_tool", Approval: model.ApprovalModeNone},
			},
		},
	}

	_, err := reg.ResolveForPolicy(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for unregistered server, got nil")
	}
}

func TestResolveForPolicy_ToolNotFound(t *testing.T) {
	reg, _ := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "read_pods", "description": "list pods", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if err := reg.RegisterServer(context.Background(), "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "my-server.read_pods", Approval: model.ApprovalModeNone},
				{Tool: "my-server.ghost_tool", Approval: model.ApprovalModeNone},
			},
		},
	}

	_, err := reg.ResolveForPolicy(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for missing tool, got nil")
	}
	if !strings.Contains(err.Error(), "ghost_tool") {
		t.Errorf("error %q does not mention the missing tool name", err.Error())
	}
}

func TestResolveForPolicy_ToolsOrdered(t *testing.T) {
	reg, _ := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "tool_a", "description": "tool a", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool_b", "description": "tool b", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool_c", "description": "tool c", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if err := reg.RegisterServer(context.Background(), "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "my-server.tool_a", Approval: model.ApprovalModeNone},
				{Tool: "my-server.tool_b", Approval: model.ApprovalModeNone},
				{Tool: "my-server.tool_c", Approval: model.ApprovalModeRequired},
			},
		},
	}

	result, err := reg.ResolveForPolicy(context.Background(), p)
	if err != nil {
		t.Fatalf("ResolveForPolicy: %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("len(result) = %d, want 3", len(result))
	}
	if result[0].Role != model.CapabilityRoleTool {
		t.Errorf("result[0].Role = %q, want tool", result[0].Role)
	}
	if result[1].Role != model.CapabilityRoleTool {
		t.Errorf("result[1].Role = %q, want tool", result[1].Role)
	}
	if result[2].Role != model.CapabilityRoleTool {
		t.Errorf("result[2].Role = %q, want tool", result[2].Role)
	}
}
