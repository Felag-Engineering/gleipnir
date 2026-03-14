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
			Sensors: []model.SensorCapability{
				{Tool: "my-server.read_pods", Params: map[string]any{"namespace": "worker-01"}},
			},
			Actuators: []model.ActuatorCapability{
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

	sensor := result[0]
	if sensor.Role != model.CapabilityRoleSensor {
		t.Errorf("result[0].Role = %q, want %q", sensor.Role, model.CapabilityRoleSensor)
	}
	if sensor.ServerName != "my-server" {
		t.Errorf("result[0].ServerName = %q, want %q", sensor.ServerName, "my-server")
	}
	if sensor.ToolName != "read_pods" {
		t.Errorf("result[0].ToolName = %q, want %q", sensor.ToolName, "read_pods")
	}
	if sensor.Approval != model.ApprovalModeNone {
		t.Errorf("result[0].Approval = %q, want %q", sensor.Approval, model.ApprovalModeNone)
	}
	if sensor.Timeout != 0 {
		t.Errorf("result[0].Timeout = %v, want 0", sensor.Timeout)
	}
	if sensor.Client == nil {
		t.Errorf("result[0].Client is nil")
	}

	actuator := result[1]
	if actuator.Role != model.CapabilityRoleActuator {
		t.Errorf("result[1].Role = %q, want %q", actuator.Role, model.CapabilityRoleActuator)
	}
	if actuator.Approval != model.ApprovalModeRequired {
		t.Errorf("result[1].Approval = %q, want %q", actuator.Approval, model.ApprovalModeRequired)
	}
	if actuator.Timeout != 30*time.Minute {
		t.Errorf("result[1].Timeout = %v, want %v", actuator.Timeout, 30*time.Minute)
	}
	if actuator.OnTimeout != model.OnTimeoutReject {
		t.Errorf("result[1].OnTimeout = %q, want %q", actuator.OnTimeout, model.OnTimeoutReject)
	}
	if actuator.Client == nil {
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
			Sensors: []model.SensorCapability{
				{Tool: "my-server.read_pods"},
				{Tool: "my-server.nonexistent_tool"},
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
			Sensors: []model.SensorCapability{
				{Tool: "nodot"},
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
			Sensors: []model.SensorCapability{
				{Tool: "ghost-server.some_tool"},
			},
		},
	}

	_, err := reg.ResolveForPolicy(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for unregistered server, got nil")
	}
}

func TestResolveForPolicy_ActuatorNotFound(t *testing.T) {
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
			Sensors: []model.SensorCapability{
				{Tool: "my-server.read_pods"},
			},
			Actuators: []model.ActuatorCapability{
				{Tool: "my-server.ghost_actuator", Approval: model.ApprovalModeNone},
			},
		},
	}

	_, err := reg.ResolveForPolicy(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for missing actuator tool, got nil")
	}
	if !strings.Contains(err.Error(), "ghost_actuator") {
		t.Errorf("error %q does not mention the missing actuator tool name", err.Error())
	}
}

func TestResolveForPolicy_SensorsAndActuatorsOrdered(t *testing.T) {
	reg, _ := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "sensor_a", "description": "sensor a", "inputSchema": map[string]any{"type": "object"}},
		{"name": "sensor_b", "description": "sensor b", "inputSchema": map[string]any{"type": "object"}},
		{"name": "actuator_c", "description": "actuator c", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if err := reg.RegisterServer(context.Background(), "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Sensors: []model.SensorCapability{
				{Tool: "my-server.sensor_a"},
				{Tool: "my-server.sensor_b"},
			},
			Actuators: []model.ActuatorCapability{
				{Tool: "my-server.actuator_c", Approval: model.ApprovalModeNone},
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
	if result[0].Role != model.CapabilityRoleSensor {
		t.Errorf("result[0].Role = %q, want sensor", result[0].Role)
	}
	if result[1].Role != model.CapabilityRoleSensor {
		t.Errorf("result[1].Role = %q, want sensor", result[1].Role)
	}
	if result[2].Role != model.CapabilityRoleActuator {
		t.Errorf("result[2].Role = %q, want actuator", result[2].Role)
	}
}
