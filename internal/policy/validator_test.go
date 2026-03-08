package policy

import (
	"strings"
	"testing"

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
			Sensors: []model.SensorCapability{
				{Tool: "server.tool"},
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

func TestValidate_CronMissingSchedule(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypeCron
	p.Trigger.Schedule = ""
	assertValidationContains(t, p, "trigger.schedule is required")
}

func TestValidate_PollMissingConfig(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Poll = nil
	assertValidationContains(t, p, "trigger poll config is required")
}

func TestValidate_PollInvalidInterval(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Poll = &model.PollConfig{
		Interval: "bad",
		Request:  model.PollRequest{URL: "https://example.com", Method: "GET"},
		Filter:   "$.x",
	}
	assertValidationContains(t, p, "not a valid duration")
}

func TestValidate_PollMissingURL(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Poll = &model.PollConfig{
		Interval: "5m",
		Request:  model.PollRequest{Method: "GET"},
		Filter:   "$.x",
	}
	assertValidationContains(t, p, "trigger.request.url is required")
}

func TestValidate_PollInvalidMethod(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Poll = &model.PollConfig{
		Interval: "5m",
		Request:  model.PollRequest{URL: "https://example.com", Method: "DELETE"},
		Filter:   "$.x",
	}
	assertValidationContains(t, p, "must be GET or POST")
}

func TestValidate_PollMissingFilter(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Poll = &model.PollConfig{
		Interval: "5m",
		Request:  model.PollRequest{URL: "https://example.com", Method: "GET"},
	}
	assertValidationContains(t, p, "trigger.filter is required")
}

func TestValidate_NoSensorsOrActuators(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Sensors = nil
	p.Capabilities.Actuators = nil
	assertValidationContains(t, p, "at least one sensor or actuator")
}

func TestValidate_EmptyToolRef(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Sensors = []model.SensorCapability{{Tool: ""}}
	assertValidationContains(t, p, "tool is required")
}

func TestValidate_BadDotNotation(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Sensors = []model.SensorCapability{{Tool: "no_dot"}}
	assertValidationContains(t, p, "dot notation")
}

func TestValidate_DuplicateTool(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Sensors = []model.SensorCapability{
		{Tool: "s.t"},
		{Tool: "s.t"},
	}
	assertValidationContains(t, p, "duplicate")
}

func TestValidate_DuplicateToolAcrossRoles(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Sensors = []model.SensorCapability{{Tool: "s.t"}}
	p.Capabilities.Actuators = []model.ActuatorCapability{
		{Tool: "s.t", Approval: model.ApprovalModeNone},
	}
	assertValidationContains(t, p, "duplicate")
}

func TestValidate_InvalidApprovalMode(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Sensors = nil
	p.Capabilities.Actuators = []model.ActuatorCapability{
		{Tool: "s.t", Approval: "maybe"},
	}
	assertValidationContains(t, p, "approval")
}

func TestValidate_InvalidTimeout(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Sensors = nil
	p.Capabilities.Actuators = []model.ActuatorCapability{
		{Tool: "s.t", Approval: model.ApprovalModeRequired, Timeout: "bad", OnTimeout: model.OnTimeoutReject},
	}
	assertValidationContains(t, p, "not a valid duration")
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
	p.Capabilities.Sensors = nil
	p.Capabilities.Actuators = []model.ActuatorCapability{
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

func TestValidate_ActuatorOnlyValid(t *testing.T) {
	p := validPolicy()
	p.Capabilities.Sensors = nil
	p.Capabilities.Actuators = []model.ActuatorCapability{
		{Tool: "s.t", Approval: model.ApprovalModeNone},
	}
	if err := Validate(p); err != nil {
		t.Errorf("expected valid actuator-only policy, got: %v", err)
	}
}

func TestValidate_ValidPollTrigger(t *testing.T) {
	p := validPolicy()
	p.Trigger.Type = model.TriggerTypePoll
	p.Trigger.Poll = &model.PollConfig{
		Interval: "5m",
		Request:  model.PollRequest{URL: "https://example.com", Method: "GET"},
		Filter:   "$.items",
	}
	if err := Validate(p); err != nil {
		t.Errorf("expected valid poll policy, got: %v", err)
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
