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

func validPollCheck() model.PollCheck {
	return model.PollCheck{
		Tool:       "server.check",
		Path:       "$.status",
		Comparator: "equals",
		Value:      "degraded",
	}
}

func TestValidate_PollTrigger_Valid(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Interval = 5 * time.Minute
	p.Trigger.Match = model.MatchAll
	p.Trigger.Checks = []model.PollCheck{validPollCheck()}
	if err := Validate(p); err != nil {
		t.Errorf("expected valid poll trigger, got: %v", err)
	}
}

func TestValidate_PollTrigger_MissingInterval(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Checks = []model.PollCheck{validPollCheck()}
	// Interval is zero (not set)
	assertValidationContains(t, p, "trigger.interval is required")
}

func TestValidate_PollTrigger_IntervalTooShort(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Interval = 30 * time.Second
	p.Trigger.Checks = []model.PollCheck{validPollCheck()}
	assertValidationContains(t, p, "at least 1m")
}

func TestValidate_PollTrigger_NoChecks(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Interval = 5 * time.Minute
	// Checks is empty
	assertValidationContains(t, p, "trigger.checks is required")
}

func TestValidate_PollTrigger_CheckMissingTool(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Interval = 5 * time.Minute
	c := validPollCheck()
	c.Tool = ""
	p.Trigger.Checks = []model.PollCheck{c}
	assertValidationContains(t, p, "trigger.checks[0].tool is required")
}

func TestValidate_PollTrigger_CheckBadToolDotNotation(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Interval = 5 * time.Minute
	c := validPollCheck()
	c.Tool = "no-dot-here"
	p.Trigger.Checks = []model.PollCheck{c}
	assertValidationContains(t, p, "dot notation")
}

func TestValidate_PollTrigger_CheckMissingPath(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Interval = 5 * time.Minute
	c := validPollCheck()
	c.Path = ""
	p.Trigger.Checks = []model.PollCheck{c}
	assertValidationContains(t, p, "trigger.checks[0].path is required")
}

func TestValidate_PollTrigger_CheckNoComparator(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Interval = 5 * time.Minute
	c := validPollCheck()
	c.Comparator = ""
	p.Trigger.Checks = []model.PollCheck{c}
	assertValidationContains(t, p, "must specify exactly one comparator")
}

func TestValidate_PollTrigger_CheckInvalidComparator(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Interval = 5 * time.Minute
	c := validPollCheck()
	c.Comparator = "banana"
	p.Trigger.Checks = []model.PollCheck{c}
	assertValidationContains(t, p, "comparator")
}

func TestValidate_PollTrigger_InvalidMatch(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Interval = 5 * time.Minute
	p.Trigger.Match = "xor"
	p.Trigger.Checks = []model.PollCheck{validPollCheck()}
	assertValidationContains(t, p, "trigger.match")
}

func TestValidate_NoCapabilities(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Tools = nil
	p.Capabilities.Feedback = model.FeedbackConfig{Enabled: false}
	assertValidationContains(t, p, "at least one capability is required")
}

func TestValidate_FeedbackOnlyPolicy_Valid(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Tools = nil
	p.Capabilities.Feedback = model.FeedbackConfig{Enabled: true}
	if err := Validate(p); err != nil {
		t.Errorf("expected feedback-only policy to be valid, got: %v", err)
	}
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

// TestValidate_RejectsClaudeCodeProvider asserts that a policy declaring
// provider: claude-code fails validation with an actionable error message.
// The "claude-code" subprocess runner was removed in issue #611; operators
// must update their policies to use a supported LLM provider.
func TestValidate_RejectsClaudeCodeProvider(t *testing.T) {
	p := validPolicy()
	p.Agent.ModelConfig.Provider = "claude-code"

	err := Validate(p)
	if err == nil {
		t.Fatal("expected validation error for claude-code provider, got nil")
	}

	msg := err.Error()
	for _, want := range []string{
		"no longer supported",
		"anthropic",
		"google",
		"openai",
		"openaicompat",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q does not mention %q", msg, want)
		}
	}
}

func TestValidate_FeedbackValidTimeout(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Feedback = model.FeedbackConfig{
		Enabled:   true,
		Timeout:   "30m",
		OnTimeout: model.FeedbackOnTimeoutFail,
	}
	if err := Validate(p); err != nil {
		t.Errorf("expected valid feedback config, got: %v", err)
	}
}

func TestValidate_FeedbackInvalidTimeout(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Feedback = model.FeedbackConfig{
		Enabled:   true,
		Timeout:   "bad-duration",
		OnTimeout: model.FeedbackOnTimeoutFail,
	}
	assertValidationContains(t, p, "not a valid duration")
}

func TestValidate_FeedbackDisabledIsValid(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Feedback = model.FeedbackConfig{Enabled: false}
	if err := Validate(p); err != nil {
		t.Errorf("expected disabled feedback to be valid regardless, got: %v", err)
	}
}

// TestValidate_WebhookAuth covers the trigger.auth validation rules.
func TestValidate_WebhookAuth(t *testing.T) {
	t.Run("valid hmac is accepted", func(t *testing.T) {
		p := validPolicy()
		p.Trigger.WebhookAuth = model.WebhookAuthHMAC
		if err := Validate(p); err != nil {
			t.Errorf("expected valid, got: %v", err)
		}
	})

	t.Run("valid bearer is accepted", func(t *testing.T) {
		p := validPolicy()
		p.Trigger.WebhookAuth = model.WebhookAuthBearer
		if err := Validate(p); err != nil {
			t.Errorf("expected valid, got: %v", err)
		}
	})

	t.Run("valid none is accepted", func(t *testing.T) {
		p := validPolicy()
		p.Trigger.WebhookAuth = model.WebhookAuthNone
		if err := Validate(p); err != nil {
			t.Errorf("expected valid, got: %v", err)
		}
	})

	t.Run("invalid auth mode is rejected", func(t *testing.T) {
		p := validPolicy()
		p.Trigger.WebhookAuth = "invalidmode"
		assertValidationContains(t, p, "trigger.auth")
		assertValidationContains(t, p, "invalidmode")
	})
}

// TestCheckLegacyWebhookSecret covers the save-time rejection of the legacy field.
func TestCheckLegacyWebhookSecret(t *testing.T) {
	t.Run("webhook trigger with webhook_secret returns error mentioning rotate", func(t *testing.T) {
		yaml := `
name: test
trigger:
  type: webhook
  webhook_secret: mysecret
agent:
  task: do it
`
		err := CheckLegacyWebhookSecret(yaml)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "rotate") {
			t.Errorf("expected error to mention rotate endpoint, got: %v", err)
		}
	})

	t.Run("non-webhook trigger with webhook_secret returns error", func(t *testing.T) {
		yaml := `
name: test
trigger:
  type: manual
  webhook_secret: mysecret
agent:
  task: do it
`
		err := CheckLegacyWebhookSecret(yaml)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "webhook triggers") {
			t.Errorf("expected error to mention webhook triggers, got: %v", err)
		}
	})

	t.Run("YAML without webhook_secret is accepted", func(t *testing.T) {
		yaml := `
name: test
trigger:
  type: webhook
  auth: hmac
agent:
  task: do it
`
		if err := CheckLegacyWebhookSecret(yaml); err != nil {
			t.Errorf("expected nil, got: %v", err)
		}
	})
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
