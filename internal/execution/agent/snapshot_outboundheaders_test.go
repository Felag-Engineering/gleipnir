package agent

import (
	"testing"

	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
)

func TestBuildCapabilitySnapshotTools_CarriesOutboundHeaders(t *testing.T) {
	tools := []mcp.ResolvedTool{{
		GrantedTool: model.GrantedTool{
			ServerName:              "fs",
			ToolName:                "read",
			OutboundHeaders:         []model.OutboundHeader{{Parameter: "tenant", Header: "X-Tenant-Id"}},
			OutboundHeadersRejected: false,
		},
	}, {
		GrantedTool: model.GrantedTool{ServerName: "fs", ToolName: "bad", OutboundHeadersRejected: true},
	}}

	got := buildCapabilitySnapshotTools(tools, nil, false)

	if len(got[0].OutboundHeaders) != 1 || got[0].OutboundHeaders[0].Header != "X-Tenant-Id" {
		t.Errorf("got[0].OutboundHeaders = %+v, want X-Tenant-Id", got[0].OutboundHeaders)
	}
	if !got[1].OutboundHeadersRejected {
		t.Error("got[1].OutboundHeadersRejected = false, want true")
	}
}
