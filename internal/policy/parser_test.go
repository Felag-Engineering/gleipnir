package policy

import (
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

func TestParse_CronTrigger(t *testing.T) {
	raw := `
name: cron-policy
trigger:
  type: cron
  schedule: "0 * * * *"
capabilities:
  sensors:
    - tool: s.t
agent:
  task: check things
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Trigger.Type != model.TriggerTypeCron {
		t.Errorf("trigger.type = %q, want %q", p.Trigger.Type, model.TriggerTypeCron)
	}
	if p.Trigger.Schedule != "0 * * * *" {
		t.Errorf("schedule = %q, want %q", p.Trigger.Schedule, "0 * * * *")
	}
}

func TestParse_PollTrigger(t *testing.T) {
	raw := `
name: poll-policy
trigger:
  type: poll
  interval: 5m
  request:
    url: "https://example.com/api"
    method: GET
    headers:
      Authorization: "Bearer ${TOKEN}"
  filter: "$.items[?(@.status == 'open')]"
capabilities:
  sensors:
    - tool: s.t
agent:
  task: poll things
`
	p, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Trigger.Type != model.TriggerTypePoll {
		t.Errorf("trigger.type = %q, want %q", p.Trigger.Type, model.TriggerTypePoll)
	}
	if p.Trigger.Poll == nil {
		t.Fatal("poll config is nil")
	}
	if p.Trigger.Poll.Interval != "5m" {
		t.Errorf("interval = %q, want %q", p.Trigger.Poll.Interval, "5m")
	}
	if p.Trigger.Poll.Request.URL != "https://example.com/api" {
		t.Errorf("url = %q", p.Trigger.Poll.Request.URL)
	}
	if p.Trigger.Poll.Request.Headers["Authorization"] != "Bearer ${TOKEN}" {
		t.Errorf("auth header = %q", p.Trigger.Poll.Request.Headers["Authorization"])
	}
	if p.Trigger.Poll.Filter != "$.items[?(@.status == 'open')]" {
		t.Errorf("filter = %q", p.Trigger.Poll.Filter)
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

func TestParse_InvalidYAML(t *testing.T) {
	_, err := Parse("{{bad yaml")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
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
