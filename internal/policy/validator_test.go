package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
)

// validPolicy returns a minimal valid ParsedPolicy for mutation in tests.
func validPolicy() *model.ParsedPolicy {
	return &model.ParsedPolicy{
		Name: "test-policy",
		Trigger: model.TriggerConfig{
			Type: model.TriggerTypeWebhook,
		},
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "server.tool", Approval: model.ApprovalModeNone},
			},
		},
		Agent: model.AgentConfig{
			Task:        "do something",
			Concurrency: model.ConcurrencySkip,
			Limits: model.RunLimits{
				MaxTokensPerRun:    20000,
				MaxToolCallsPerRun: 50,
			},
		},
	}
}

func TestValidate_ValidMinimal(t *testing.T) {
	if err := Validate(validPolicy()); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestValidate_MissingName(t *testing.T) {
	p := validPolicy()
	p.Name = ""
	assertValidationContains(t, p, "name is required")
}

func TestValidate_InvalidTriggerType(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = "invalid"
	assertValidationContains(t, p, "trigger.type")
}

func TestValidate_CronTriggerIsInvalid(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = "cron"
	assertValidationContains(t, p, "trigger.type")
}

func TestValidate_PollTriggerIsInvalid(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = "poll"
	assertValidationContains(t, p, "trigger.type")
}

func TestValidate_NoTools(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Tools = nil
	assertValidationContains(t, p, "at least one tool is required")
}

func TestValidate_EmptyToolRef(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Tools = []model.ToolCapability{{Tool: "", Approval: model.ApprovalModeNone}}
	assertValidationContains(t, p, "tool is required")
}

func TestValidate_BadDotNotation(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Tools = []model.ToolCapability{{Tool: "no_dot", Approval: model.ApprovalModeNone}}
	assertValidationContains(t, p, "dot notation")
}

func TestValidate_DuplicateTool(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Tools = []model.ToolCapability{
		{Tool: "s.t", Approval: model.ApprovalModeNone},
		{Tool: "s.t", Approval: model.ApprovalModeNone},
	}
	assertValidationContains(t, p, "duplicate")
}

func TestValidate_InvalidApprovalMode(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Tools = []model.ToolCapability{
		{Tool: "s.t", Approval: "maybe"},
	}
	assertValidationContains(t, p, "approval")
}

func TestValidate_InvalidTimeout(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Tools = []model.ToolCapability{
		{Tool: "s.t", Approval: model.ApprovalModeRequired, Timeout: "bad", OnTimeout: model.OnTimeoutReject},
	}
	assertValidationContains(t, p, "not a valid duration")
}

func TestValidate_OnTimeoutApproveRejected(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Tools = []model.ToolCapability{
		{Tool: "s.t", Approval: model.ApprovalModeRequired, OnTimeout: "approve"},
	}
	assertValidationContains(t, p, "on_timeout")
}

func TestValidate_MissingTask(t *testing.T) {
	p := validPolicy()
	p.Agent.Task = ""
	assertValidationContains(t, p, "agent.task is required")
}

func TestValidate_InvalidConcurrency(t *testing.T) {
	p := validPolicy()
	p.Agent.Concurrency = "invalid"
	assertValidationContains(t, p, "agent.concurrency")
}

func TestValidate_ReplacePlusApproval(t *testing.T) {
	p := validPolicy()
	p.Agent.Concurrency = model.ConcurrencyReplace
	p.Capabilities.Tools = []model.ToolCapability{
		{Tool: "s.t", Approval: model.ApprovalModeRequired, OnTimeout: model.OnTimeoutReject},
	}
	assertValidationContains(t, p, "replace")
}

func TestValidate_NegativeLimits(t *testing.T) {
	p := validPolicy()
	p.Agent.Limits.MaxTokensPerRun = -1
	assertValidationContains(t, p, "max_tokens_per_run must be positive")
}

func TestValidate_ZeroToolCalls(t *testing.T) {
	p := validPolicy()
	p.Agent.Limits.MaxToolCallsPerRun = 0
	assertValidationContains(t, p, "max_tool_calls_per_run must be positive")
}

func TestValidate_ToolWithApprovalValid(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Tools = []model.ToolCapability{
		{Tool: "s.t", Approval: model.ApprovalModeRequired, OnTimeout: model.OnTimeoutReject},
	}
	if err := Validate(p); err != nil {
		t.Errorf("expected valid tool with approval, got: %v", err)
	}
}

func TestValidate_ManualTrigger(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypeManual
	if err := Validate(p); err != nil {
		t.Errorf("expected valid manual trigger policy, got: %v", err)
	}
}

func TestValidate_ScheduledTriggerValid(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypeScheduled
	p.Trigger.FireAt = []time.Time{time.Now().Add(time.Hour)}
	if err := Validate(p); err != nil {
		t.Errorf("expected valid scheduled trigger policy, got: %v", err)
	}
}

func TestValidate_ScheduledTriggerEmptyFireAt(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypeScheduled
	p.Trigger.FireAt = nil
	assertValidationContains(t, p, "trigger.fire_at is required")
}

func TestValidate_NegativeQueueDepth(t *testing.T) {
	p := validPolicy()
	p.Agent.QueueDepth = -1
	assertValidationContains(t, p, "agent.queue_depth must not be negative")
}

func TestValidate_ZeroQueueDepthIsValid(t *testing.T) {
	// queue_depth: 0 is treated as "use default" by the parser, not rejected by the validator.
	p := validPolicy()
	p.Agent.QueueDepth = 0
	if err := Validate(p); err != nil {
		t.Errorf("expected queue_depth 0 to be valid (means use default), got: %v", err)
	}
}

func assertValidationContains(t *testing.T, p *model.ParsedPolicy, substr string) {
	t.Helper()
	err := Validate(p)
	if err == nil {
		t.Fatalf("expected validation error containing %q, got nil", substr)
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	for _, e := range ve.Errors {
		if strings.Contains(e, substr) {
			return
		}
	}
	t.Errorf("expected error containing %q in %v", substr, ve.Errors)
}
