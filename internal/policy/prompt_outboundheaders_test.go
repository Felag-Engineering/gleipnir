package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/model"
)

// Outbound header detail is audit-only: it must never reach the model.
func TestRenderSystemPrompt_OmitsOutboundHeaders(t *testing.T) {
	p := &model.ParsedPolicy{Agent: model.AgentConfig{Task: "t"}}
	granted := []model.GrantedTool{{
		ServerName:      "fs",
		ToolName:        "read",
		OutboundHeaders: []model.OutboundHeader{{Parameter: "tenant", Header: "X-Tenant-Id"}},
	}}

	result := RenderSystemPrompt(p, granted, time.Date(2026, 3, 13, 12, 0, 0, 0, time.UTC))

	for _, leaked := range []string{"X-Tenant-Id", "tenant", "outbound"} {
		if strings.Contains(result, leaked) {
			t.Errorf("system prompt contains %q; outbound headers must stay out of model context", leaked)
		}
	}
}
