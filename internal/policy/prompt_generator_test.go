package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
)

func TestRenderSystemPrompt_DefaultPreamble(t *testing.T) {
	p := &model.ParsedPolicy{
		Agent: model.AgentConfig{
			Task: "Check the repos",
		},
	}
	granted := []model.GrantedTool{
		{ServerName: "github", ToolName: "list_repos"},
	}

	result := RenderSystemPrompt(p, granted, time.Date(2026, 3, 13, 12, 0, 0, 0, time.UTC))

	if !strings.Contains(result, "BoundAgent") {
		t.Error("expected default preamble containing 'BoundAgent'")
	}
	if !strings.Contains(result, "github.list_repos") {
		t.Error("expected tool in capabilities block")
	}
	if !strings.Contains(result, "## Your task") {
		t.Error("expected task section header")
	}
	if !strings.Contains(result, "Check the repos") {
		t.Error("expected task text")
	}
}

func TestRenderSystemPrompt_CustomPreamble(t *testing.T) {
	p := &model.ParsedPolicy{
		Agent: model.AgentConfig{
			Preamble: "You are a custom agent.",
			Task:     "Do custom things",
		},
	}

	result := RenderSystemPrompt(p, nil, time.Date(2026, 3, 13, 12, 0, 0, 0, time.UTC))

	if !strings.Contains(result, "You are a custom agent.") {
		t.Error("expected custom preamble")
	}
	if strings.Contains(result, "BoundAgent") {
		t.Error("should not contain default preamble when custom is provided")
	}
}

func TestRenderSystemPrompt_TimestampInjected(t *testing.T) {
	fixedTime := time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
	wantTimestamp := "This run started at: 2026-01-15T09:30:00Z"

	t.Run("default preamble", func(t *testing.T) {
		p := &model.ParsedPolicy{
			Agent: model.AgentConfig{Task: "Do something"},
		}
		granted := []model.GrantedTool{
			{ServerName: "s", ToolName: "t"},
		}
		result := RenderSystemPrompt(p, granted, fixedTime)

		if !strings.Contains(result, wantTimestamp) {
			t.Errorf("expected %q in result", wantTimestamp)
		}
		tsIdx := strings.Index(result, wantTimestamp)
		capIdx := strings.Index(result, "## Capabilities")
		if tsIdx >= capIdx {
			t.Error("expected timestamp to appear before ## Capabilities")
		}
	})

	t.Run("custom preamble", func(t *testing.T) {
		p := &model.ParsedPolicy{
			Agent: model.AgentConfig{Preamble: "Custom preamble.", Task: "Do something"},
		}
		result := RenderSystemPrompt(p, nil, fixedTime)

		if !strings.Contains(result, wantTimestamp) {
			t.Errorf("expected %q in result with custom preamble", wantTimestamp)
		}
	})
}

func TestRenderCapabilitiesBlock_AllRoles(t *testing.T) {
	granted := []model.GrantedTool{
		{ServerName: "github", ToolName: "list_repos"},
		{ServerName: "github", ToolName: "list_issues"},
		{ServerName: "deploy", ToolName: "run", Approval: model.ApprovalModeNone},
		{ServerName: "deploy", ToolName: "rollback", Approval: model.ApprovalModeRequired},
		{ServerName: "slack", ToolName: "send_message"},
	}

	result := renderCapabilitiesBlock(granted, model.FeedbackConfig{Enabled: true})

	// All tools appear in the ### Tools section.
	if !strings.Contains(result, "github.list_repos") {
		t.Error("expected tool github.list_repos")
	}
	if !strings.Contains(result, "github.list_issues") {
		t.Error("expected tool github.list_issues")
	}
	if !strings.Contains(result, "deploy.run") {
		t.Error("expected tool deploy.run")
	}

	// deploy.rollback must appear as a plain entry with no approval annotation.
	// Approval gates must not be visible to the agent — they are a runtime
	// enforcement detail, not a prompt-based restriction (ADR-001).
	rollbackLine := ""
	for _, line := range strings.Split(result, "\n") {
		if strings.Contains(line, "deploy.rollback") {
			rollbackLine = line
			break
		}
	}
	if rollbackLine == "" {
		t.Error("expected deploy.rollback to be listed")
	}
	if strings.Contains(rollbackLine, "[") {
		t.Errorf("deploy.rollback line must have no annotation, got: %q", rollbackLine)
	}

	// Tools formerly tagged feedback now appear in ### Tools section.
	if !strings.Contains(result, "slack.send_message") {
		t.Error("expected slack.send_message listed under ### Tools")
	}
}

func TestRenderSystemPrompt_NoApprovalAnnotation(t *testing.T) {
	p := &model.ParsedPolicy{
		Agent: model.AgentConfig{
			Task: "Do something",
		},
	}
	granted := []model.GrantedTool{
		{ServerName: "deploy", ToolName: "rollback", Approval: model.ApprovalModeRequired},
	}

	result := RenderSystemPrompt(p, granted, time.Date(2026, 3, 13, 12, 0, 0, 0, time.UTC))

	if strings.Contains(result, "requires human approval") {
		t.Error("system prompt must not contain 'requires human approval'")
	}
	if strings.Contains(strings.ToLower(result), "approval") {
		t.Error("system prompt must not contain 'approval' — enforcement is invisible to the agent")
	}
}

func TestRenderSystemPrompt_FeedbackPausesDescription(t *testing.T) {
	p := &model.ParsedPolicy{
		Agent: model.AgentConfig{
			Task: "Do something",
		},
		// Feedback must be enabled for the preamble to include the feedback paragraph.
		Capabilities: model.CapabilitiesConfig{
			Feedback: model.FeedbackConfig{Enabled: true},
		},
	}

	result := RenderSystemPrompt(p, nil, time.Date(2026, 3, 13, 12, 0, 0, 0, time.UTC))

	if !strings.Contains(result, "pause this run") {
		t.Error("default preamble must describe that feedback tools pause the run when feedback is enabled")
	}
	if !strings.Contains(result, "gleipnir.ask_operator") {
		t.Error("default preamble must reference gleipnir.ask_operator by name when feedback is enabled")
	}
}

func TestRenderCapabilitiesBlock_EmptyTools(t *testing.T) {
	result := renderCapabilitiesBlock(nil, model.FeedbackConfig{Enabled: true})

	if !strings.Contains(result, "### Tools\nNone.") {
		t.Error("expected 'None.' for empty tools section")
	}
	if !strings.Contains(result, "gleipnir.ask_operator") {
		t.Error("expected gleipnir.ask_operator reference when feedback is enabled and no tools granted")
	}
}

func TestRenderCapabilitiesBlock_FeedbackChannelAlwaysPresent(t *testing.T) {
	granted := []model.GrantedTool{
		{ServerName: "s", ToolName: "t"},
	}

	result := renderCapabilitiesBlock(granted, model.FeedbackConfig{Enabled: true})

	if !strings.Contains(result, "gleipnir.ask_operator") {
		t.Error("expected gleipnir.ask_operator reference when feedback is enabled")
	}
}

func TestRenderCapabilitiesBlock_FeedbackDisabled(t *testing.T) {
	granted := []model.GrantedTool{
		{ServerName: "s", ToolName: "t"},
	}

	result := renderCapabilitiesBlock(granted, model.FeedbackConfig{Enabled: false})

	if strings.Contains(result, "### Feedback") {
		t.Error("feedback section must not appear when feedback is disabled")
	}
	if strings.Contains(result, "built-in feedback channel") {
		t.Error("feedback channel text must not appear when feedback is disabled")
	}
}

func TestRenderSystemPrompt_FeedbackDisabled_DefaultPreamble(t *testing.T) {
	p := &model.ParsedPolicy{
		Agent: model.AgentConfig{
			Task: "Do something",
		},
		Capabilities: model.CapabilitiesConfig{
			Feedback: model.FeedbackConfig{Enabled: false},
		},
	}

	result := RenderSystemPrompt(p, nil, time.Date(2026, 3, 13, 12, 0, 0, 0, time.UTC))

	if strings.Contains(result, "Feedback: a channel to consult a human operator") {
		t.Error("default preamble must not include feedback paragraph when feedback is disabled")
	}
	if strings.Contains(result, "### Feedback") {
		t.Error("capabilities block must not include feedback section when feedback is disabled")
	}
}

func TestRenderSystemPrompt_FeedbackEnabled_DefaultPreamble(t *testing.T) {
	p := &model.ParsedPolicy{
		Agent: model.AgentConfig{
			Task: "Do something",
		},
		Capabilities: model.CapabilitiesConfig{
			Feedback: model.FeedbackConfig{Enabled: true},
		},
	}

	result := RenderSystemPrompt(p, nil, time.Date(2026, 3, 13, 12, 0, 0, 0, time.UTC))

	if !strings.Contains(result, "Feedback: a channel to consult a human operator") {
		t.Error("default preamble must include feedback paragraph when feedback is enabled")
	}
	if !strings.Contains(result, "gleipnir.ask_operator") {
		t.Error("default preamble must reference gleipnir.ask_operator when feedback is enabled")
	}
	if !strings.Contains(result, "### Feedback") {
		t.Error("capabilities block must include feedback section when feedback is enabled")
	}
}
