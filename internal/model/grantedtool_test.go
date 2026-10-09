package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGrantedTool_OutboundHeadersJSON(t *testing.T) {
	t.Run("old snapshot without the fields parses", func(t *testing.T) {
		var gt GrantedTool
		old := `{"server_name":"fs","tool_name":"read","approval":"none","timeout":0,"on_timeout":""}`
		if err := json.Unmarshal([]byte(old), &gt); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(gt.OutboundHeaders) != 0 || gt.OutboundHeadersRejected {
			t.Errorf("got %+v, want zero outbound fields", gt)
		}
	})

	t.Run("empty fields are omitted", func(t *testing.T) {
		b, err := json.Marshal(GrantedTool{ServerName: "fs", ToolName: "read"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(b), "outbound") {
			t.Errorf("marshaled %s, want no outbound fields", b)
		}
	})

	t.Run("round trip", func(t *testing.T) {
		in := GrantedTool{ServerName: "fs", ToolName: "read", OutboundHeaders: []OutboundHeader{{Parameter: "tenant", Header: "X-Tenant-Id"}}}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out GrantedTool
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(out.OutboundHeaders) != 1 || out.OutboundHeaders[0] != in.OutboundHeaders[0] {
			t.Errorf("got %+v, want %+v", out.OutboundHeaders, in.OutboundHeaders)
		}
	})
}
