package mcp

import (
	"context"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/model"
)

func TestResolveForPolicy_OutboundHeaders(t *testing.T) {
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "tagged", "description": "d", "inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tenant": map[string]any{"type": "string", "x-mcp-header": "X-Tenant-Id"},
				"other":  map[string]any{"type": "string"},
			},
		}},
		{"name": "plain", "description": "d", "inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"other": map[string]any{"type": "string"}},
		}},
		{"name": "bad", "description": "d", "inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"conn": map[string]any{"type": "string", "x-mcp-header": "Connection"},
			},
		}},
	}
	srv := makeMCPServer(t, tools)
	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "my-server.tagged", Approval: model.ApprovalModeNone},
				{Tool: "my-server.plain", Approval: model.ApprovalModeNone},
				{Tool: "my-server.bad", Approval: model.ApprovalModeNone},
			},
		},
	}
	result, err := reg.ResolveForPolicy(context.Background(), p)
	if err != nil {
		t.Fatalf("ResolveForPolicy: %v", err)
	}

	tagged, plain, bad := result[0], result[1], result[2]
	if len(tagged.OutboundHeaders) != 1 || tagged.OutboundHeaders[0] != (model.OutboundHeader{Parameter: "tenant", Header: "X-Tenant-Id"}) {
		t.Errorf("tagged.OutboundHeaders = %+v, want [{tenant X-Tenant-Id}]", tagged.OutboundHeaders)
	}
	if tagged.OutboundHeadersRejected {
		t.Error("tagged.OutboundHeadersRejected = true, want false")
	}
	if len(plain.OutboundHeaders) != 0 || plain.OutboundHeadersRejected {
		t.Errorf("plain = %+v, want no outbound headers", plain.GrantedTool)
	}
	if len(bad.OutboundHeaders) != 0 || !bad.OutboundHeadersRejected {
		t.Errorf("bad = %+v, want rejected with no headers", bad.GrantedTool)
	}
}
