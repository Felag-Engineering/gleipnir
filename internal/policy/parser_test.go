package policy

import (
	"errors"
	"strings"
	"testing"

	"github.com/rapp992/gleipnir/internal/model"
)

func TestParse_WebhookMinimal(t *testing.T) {
	raw := `
name: my-policy
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: github.list_repos
agent:
  task: Do something
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.Name != "my-policy" {
		t.Errorf("name = %q, want %q", p.Name, "my-policy")
	}
	if p.Trigger.Type != model.TriggerTypeWebhook {
		t.Errorf("trigger.type = %q, want %q", p.Trigger.Type, model.TriggerTypeWebhook)
	}
	if len(p.Capabilities.Sensors) != 1 {
		t.Fatalf("len(sensors) = %d, want 1", len(p.Capabilities.Sensors))
	}
	if p.Capabilities.Sensors[0].Tool != "github.list_repos" {
		t.Errorf("sensor tool = %q, want %q", p.Capabilities.Sensors[0].Tool, "github.list_repos")
	}
	if p.Agent.Task != "Do something" {
		t.Errorf("task = %q, want %q", p.Agent.Task, "Do something")
	}
}

func TestParse_Defaults(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: s.t
agent:
  task: do it
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.Agent.Limits.MaxTokensPerRun != defaultMaxTokensPerRun {
		t.Errorf("max_tokens = %d, want %d", p.Agent.Limits.MaxTokensPerRun, defaultMaxTokensPerRun)
	}
	if p.Agent.Limits.MaxToolCallsPerRun != defaultMaxToolCallsPerRun {
		t.Errorf("max_tool_calls = %d, want %d", p.Agent.Limits.MaxToolCallsPerRun, defaultMaxToolCallsPerRun)
	}
	if p.Agent.Concurrency != model.ConcurrencySkip {
		t.Errorf("concurrency = %q, want %q", p.Agent.Concurrency, model.ConcurrencySkip)
	}
}

func TestParse_ActuatorDefaults(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  actuators:
    - tool: deploy.run
agent:
  task: deploy
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(p.Capabilities.Actuators) != 1 {
		t.Fatalf("len(actuators) = %d, want 1", len(p.Capabilities.Actuators))
	}
	a := p.Capabilities.Actuators[0]
	if a.Approval != model.ApprovalModeNone {
		t.Errorf("approval = %q, want %q", a.Approval, model.ApprovalModeNone)
	}
	if a.OnTimeout != "" {
		t.Errorf("on_timeout = %q, want empty (ignored for approval: none)", a.OnTimeout)
	}
}

func TestParse_ActuatorWithApproval(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  actuators:
    - tool: deploy.run
      approval: required
      timeout: 30m
      on_timeout: approve
agent:
  task: deploy
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	a := p.Capabilities.Actuators[0]
	if a.Approval != model.ApprovalModeRequired {
		t.Errorf("approval = %q, want %q", a.Approval, model.ApprovalModeRequired)
	}
	if a.Timeout != "30m" {
		t.Errorf("timeout = %q, want %q", a.Timeout, "30m")
	}
	if a.OnTimeout != model.OnTimeoutApprove {
		t.Errorf("on_timeout = %q, want %q", a.OnTimeout, model.OnTimeoutApprove)
	}
}


func TestParse_Params(t *testing.T) {
	raw := `
name: params-test
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: github.list_repos
      params:
        org: myorg
  actuators:
    - tool: deploy.run
      params:
        env: staging
agent:
  task: deploy
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.Capabilities.Sensors[0].Params["org"] != "myorg" {
		t.Errorf("sensor params[org] = %v", p.Capabilities.Sensors[0].Params["org"])
	}
	if p.Capabilities.Actuators[0].Params["env"] != "staging" {
		t.Errorf("actuator params[env] = %v", p.Capabilities.Actuators[0].Params["env"])
	}
}

func TestParse_CustomLimits(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: s.t
agent:
  task: do it
  limits:
    max_tokens_per_run: 50000
    max_tool_calls_per_run: 100
  concurrency: queue
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Agent.Limits.MaxTokensPerRun != 50000 {
		t.Errorf("max_tokens = %d, want 50000", p.Agent.Limits.MaxTokensPerRun)
	}
	if p.Agent.Limits.MaxToolCallsPerRun != 100 {
		t.Errorf("max_tool_calls = %d, want 100", p.Agent.Limits.MaxToolCallsPerRun)
	}
	if p.Agent.Concurrency != model.ConcurrencyQueue {
		t.Errorf("concurrency = %q, want %q", p.Agent.Concurrency, model.ConcurrencyQueue)
	}
}

func TestParse_ModelDefault(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: s.t
agent:
  task: do it
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Agent.Model != defaultModel {
		t.Errorf("model = %q, want %q", p.Agent.Model, defaultModel)
	}
}

func TestParse_ModelExplicit(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: s.t
agent:
  task: do it
  model: claude-opus-4-6
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Agent.Model != "claude-opus-4-6" {
		t.Errorf("model = %q, want %q", p.Agent.Model, "claude-opus-4-6")
	}
}

func TestParse_ManualTrigger(t *testing.T) {
	raw := `
name: manual-policy
trigger:
  type: manual
capabilities:
  sensors:
    - tool: s.t
agent:
  task: do it manually
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Trigger.Type != model.TriggerTypeManual {
		t.Errorf("trigger.type = %q, want %q", p.Trigger.Type, model.TriggerTypeManual)
	}
}

func TestParse_ScheduledTrigger(t *testing.T) {
	raw := `
name: scheduled-policy
trigger:
  type: scheduled
  fire_at:
    - "2030-01-01T09:00:00Z"
    - "2030-06-15T12:00:00Z"
capabilities:
  sensors:
    - tool: s.t
agent:
  task: scheduled task
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Trigger.Type != model.TriggerTypeScheduled {
		t.Errorf("trigger.type = %q, want %q", p.Trigger.Type, model.TriggerTypeScheduled)
	}
	if len(p.Trigger.FireAt) != 2 {
		t.Fatalf("len(fire_at) = %d, want 2", len(p.Trigger.FireAt))
	}
	if p.Trigger.FireAt[0].Year() != 2030 {
		t.Errorf("fire_at[0].Year = %d, want 2030", p.Trigger.FireAt[0].Year())
	}
}

func TestParse_ScheduledTrigger_InvalidTimestampSkipped(t *testing.T) {
	raw := `
name: scheduled-policy
trigger:
  type: scheduled
  fire_at:
    - "2030-01-01T09:00:00Z"
    - "not-a-timestamp"
    - "2030-06-15T12:00:00Z"
capabilities:
  sensors:
    - tool: s.t
agent:
  task: scheduled task
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Unparseable entries are silently skipped by the parser.
	// The validator catches the mismatch between raw YAML count and parsed count
	// only if that's needed; here we just confirm valid entries are preserved.
	if len(p.Trigger.FireAt) != 2 {
		t.Fatalf("len(fire_at) = %d, want 2 (invalid entry should be skipped)", len(p.Trigger.FireAt))
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	_, err := Parse("{{bad yaml")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParse_SizeLimit(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantSizeErr bool
	}{
		{
			name:        "empty string",
			input:       "",
			wantSizeErr: false,
		},
		{
			name:        "exactly at limit",
			input:       strings.Repeat("x", MaxPolicyYAMLBytes),
			wantSizeErr: false,
		},
		{
			name:        "one byte over limit",
			input:       strings.Repeat("x", MaxPolicyYAMLBytes+1),
			wantSizeErr: true,
		},
		{
			name:        "well over limit",
			input:       strings.Repeat("x", 2*MaxPolicyYAMLBytes),
			wantSizeErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.input)
			if tc.wantSizeErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), "exceeds maximum size") {
					t.Errorf("error %q does not contain %q", err.Error(), "exceeds maximum size")
				}
				var pe *ParseError
				if errors.As(err, &pe) {
					t.Error("size limit error should not be a *ParseError")
				}
			} else {
				// A size error must not be returned; other errors (YAML syntax,
				// validation) are acceptable since the payload is synthetic.
				if err != nil && strings.Contains(err.Error(), "exceeds maximum size") {
					t.Errorf("unexpected size error: %v", err)
				}
			}
		})
	}
}

func TestParse_CustomPreamble(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  sensors:
    - tool: s.t
agent:
  preamble: Custom preamble text
  task: do it
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Agent.Preamble != "Custom preamble text" {
		t.Errorf("preamble = %q, want %q", p.Agent.Preamble, "Custom preamble text")
	}
}
