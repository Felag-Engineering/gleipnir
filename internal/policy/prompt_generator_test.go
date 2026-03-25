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
		{ServerName: "github", ToolName: "list_repos", Role: model.CapabilityRoleTool},
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
			{ServerName: "s", ToolName: "t", Role: model.CapabilityRoleTool},
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
		{ServerName: "github", ToolName: "list_repos", Role: model.CapabilityRoleTool},
		{ServerName: "github", ToolName: "list_issues", Role: model.CapabilityRoleTool},
		{ServerName: "deploy", ToolName: "run", Role: model.CapabilityRoleTool, Approval: model.ApprovalModeNone},
		{ServerName: "deploy", ToolName: "rollback", Role: model.CapabilityRoleTool, Approval: model.ApprovalModeRequired},
		{ServerName: "slack", ToolName: "send_message", Role: model.CapabilityRoleFeedback},
	}

	result := renderCapabilitiesBlock(granted)

	// Tools
	if !strings.Contains(result, "github.list_repos") {
		t.Error("expected tool github.list_repos")
	}
	if !strings.Contains(result, "github.list_issues") {
		t.Error("expected tool github.list_issues")
	}
	if !strings.Contains(result, "deploy.run") {
		t.Error("expected tool deploy.run")
	}
	if !strings.Contains(result, "deploy.rollback") {
		t.Error("expected tool deploy.rollback")
	}

	// Approval annotation
	if !strings.Contains(result, "[requires human approval before execution]") {
		t.Error("expected approval annotation on deploy.rollback")
	}
	// Ensure non-approval tool does NOT have the annotation
	runLine := ""
	for _, line := range strings.Split(result, "\n") {
		if strings.Contains(line, "deploy.run") {
			runLine = line
			break
		}
	}
	if strings.Contains(runLine, "[requires human approval") {
		t.Error("deploy.run should not have approval annotation")
	}

	// Feedback
	if !strings.Contains(result, "slack.send_message") {
		t.Error("expected feedback tool slack.send_message")
	}
}

func TestRenderCapabilitiesBlock_EmptyTools(t *testing.T) {
	granted := []model.GrantedTool{
		{ServerName: "slack", ToolName: "send_message", Role: model.CapabilityRoleFeedback},
	}

	result := renderCapabilitiesBlock(granted)

	if !strings.Contains(result, "### Tools\nNone.") {
		t.Error("expected 'None.' for empty tools section")
	}
}

func TestRenderCapabilitiesBlock_NoFeedbackTools(t *testing.T) {
	granted := []model.GrantedTool{
		{ServerName: "s", ToolName: "t", Role: model.CapabilityRoleTool},
	}

	result := renderCapabilitiesBlock(granted)

	if !strings.Contains(result, "built-in feedback channel") {
		t.Error("expected default feedback message when no feedback tools granted")
	}
}
