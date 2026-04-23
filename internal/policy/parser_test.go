package policy

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
)

func TestParse_WebhookMinimal(t *testing.T) {
	raw := `
name: my-policy
trigger:
  type: webhook
capabilities:
  tools:
    - tool: github.list_repos
agent:
  task: Do something
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.Name != "my-policy" {
		t.Errorf("name = %q, want %q", p.Name, "my-policy")
	}
	if p.Trigger.Type != model.TriggerTypeWebhook {
		t.Errorf("trigger.type = %q, want %q", p.Trigger.Type, model.TriggerTypeWebhook)
	}
	if len(p.Capabilities.Tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(p.Capabilities.Tools))
	}
	if p.Capabilities.Tools[0].Tool != "github.list_repos" {
		t.Errorf("tool = %q, want %q", p.Capabilities.Tools[0].Tool, "github.list_repos")
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
  tools:
    - tool: s.t
agent:
  task: do it
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
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

func TestParse_ToolApprovalDefaults(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: deploy.run
agent:
  task: deploy
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(p.Capabilities.Tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(p.Capabilities.Tools))
	}
	tc := p.Capabilities.Tools[0]
	if tc.Approval != model.ApprovalModeNone {
		t.Errorf("approval = %q, want %q", tc.Approval, model.ApprovalModeNone)
	}
	if tc.OnTimeout != "" {
		t.Errorf("on_timeout = %q, want empty (ignored for approval: none)", tc.OnTimeout)
	}
}

func TestParse_ToolWithApproval(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: deploy.run
      approval: required
      timeout: 30m
      on_timeout: reject
agent:
  task: deploy
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := p.Capabilities.Tools[0]
	if tc.Approval != model.ApprovalModeRequired {
		t.Errorf("approval = %q, want %q", tc.Approval, model.ApprovalModeRequired)
	}
	if tc.Timeout != "30m" {
		t.Errorf("timeout = %q, want %q", tc.Timeout, "30m")
	}
	if tc.OnTimeout != model.OnTimeoutReject {
		t.Errorf("on_timeout = %q, want %q", tc.OnTimeout, model.OnTimeoutReject)
	}
}

func TestParse_Params(t *testing.T) {
	raw := `
name: params-test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: github.list_repos
      params:
        org: myorg
    - tool: deploy.run
      params:
        env: staging
agent:
  task: deploy
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.Capabilities.Tools[0].Params["org"] != "myorg" {
		t.Errorf("tools[0] params[org] = %v", p.Capabilities.Tools[0].Params["org"])
	}
	if p.Capabilities.Tools[1].Params["env"] != "staging" {
		t.Errorf("tools[1] params[env] = %v", p.Capabilities.Tools[1].Params["env"])
	}
}

func TestParse_CustomLimits(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
  limits:
    max_tokens_per_run: 50000
    max_tool_calls_per_run: 100
  concurrency: queue
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
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
  tools:
    - tool: s.t
agent:
  task: do it
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Agent.ModelConfig.Name != "claude-sonnet-4-6" {
		t.Errorf("model = %q, want %q", p.Agent.ModelConfig.Name, "claude-sonnet-4-6")
	}
}

func TestParse_ManualTrigger(t *testing.T) {
	raw := `
name: manual-policy
trigger:
  type: manual
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it manually
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
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
  tools:
    - tool: s.t
agent:
  task: scheduled task
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
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
  tools:
    - tool: s.t
agent:
  task: scheduled task
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
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
	_, err := Parse("{{bad yaml", "anthropic", "claude-sonnet-4-6")
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
			_, err := Parse(tc.input, "anthropic", "claude-sonnet-4-6")
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

func TestParse_ProviderDefault(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Agent.ModelConfig.Provider != "anthropic" {
		t.Errorf("provider = %q, want %q (default)", p.Agent.ModelConfig.Provider, "anthropic")
	}
}

func TestParse_ModelSection(t *testing.T) {
	minimalHeader := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
`
	cases := []struct {
		name         string
		yaml         string
		wantProvider string
		wantName     string
		wantOptions  map[string]any
	}{
		{
			name: "new format with all fields",
			yaml: `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
model:
  provider: anthropic
  name: claude-sonnet-4-20250514
  options:
    enable_prompt_caching: true
`,
			wantProvider: "anthropic",
			wantName:     "claude-sonnet-4-20250514",
			wantOptions:  map[string]any{"enable_prompt_caching": true},
		},
		{
			name: "new format provider and name only",
			yaml: `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
model:
  provider: anthropic
  name: claude-sonnet-4-20250514
`,
			wantProvider: "anthropic",
			wantName:     "claude-sonnet-4-20250514",
			wantOptions:  nil,
		},
		{
			name:         "completely omitted model section",
			yaml:         minimalHeader,
			wantProvider: "anthropic",
			wantName:     "claude-sonnet-4-6",
			wantOptions:  nil,
		},
		{
			name: "partial section provider only",
			yaml: `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
model:
  provider: google
`,
			wantProvider: "google",
			wantName:     "claude-sonnet-4-6",
			wantOptions:  nil,
		},
		{
			name: "partial section name only",
			yaml: `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
model:
  name: gemini-2.5-flash
`,
			wantProvider: "anthropic",
			wantName:     "gemini-2.5-flash",
			wantOptions:  nil,
		},
		{
			name: "options with mixed types",
			yaml: `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
model:
  provider: anthropic
  name: claude-sonnet-4-6
  options:
    enable_prompt_caching: true
    max_tokens: 8192
    tag: production
`,
			wantProvider: "anthropic",
			wantName:     "claude-sonnet-4-6",
			wantOptions: map[string]any{
				"enable_prompt_caching": true,
				"max_tokens":            8192,
				"tag":                   "production",
			},
		},
		{
			name: "model present but empty",
			yaml: `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
model: {}
`,
			wantProvider: "anthropic",
			wantName:     "claude-sonnet-4-6",
			wantOptions:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse(tc.yaml, "anthropic", "claude-sonnet-4-6")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Agent.ModelConfig.Provider != tc.wantProvider {
				t.Errorf("provider = %q, want %q", p.Agent.ModelConfig.Provider, tc.wantProvider)
			}
			if p.Agent.ModelConfig.Name != tc.wantName {
				t.Errorf("name = %q, want %q", p.Agent.ModelConfig.Name, tc.wantName)
			}
			if len(p.Agent.ModelConfig.Options) != len(tc.wantOptions) {
				t.Errorf("options len = %d, want %d", len(p.Agent.ModelConfig.Options), len(tc.wantOptions))
			}
			for k, want := range tc.wantOptions {
				got, ok := p.Agent.ModelConfig.Options[k]
				if !ok {
					t.Errorf("options[%q] missing", k)
					continue
				}
				// yaml.v3 decodes integers as int, booleans as bool, strings as string.
				if got != want {
					t.Errorf("options[%q] = %v (%T), want %v (%T)", k, got, got, want, want)
				}
			}
		})
	}

	t.Run("round-trip parse render parse", func(t *testing.T) {
		// Parse an initial YAML with the new model section.
		first, err := Parse(`
name: rt-test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: round trip
model:
  provider: anthropic
  name: claude-sonnet-4-20250514
  options:
    enable_prompt_caching: true
`, "anthropic", "claude-sonnet-4-6")
		if err != nil {
			t.Fatalf("first parse error: %v", err)
		}

		// Reconstruct a minimal YAML string from the parsed fields to simulate
		// a round-trip (ParsedPolicy has no Render method).
		reconstructed := `
name: rt-test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: round trip
model:
  provider: ` + first.Agent.ModelConfig.Provider + `
  name: ` + first.Agent.ModelConfig.Name + `
  options:
    enable_prompt_caching: true
`
		second, err := Parse(reconstructed, "anthropic", "claude-sonnet-4-6")
		if err != nil {
			t.Fatalf("second parse error: %v", err)
		}

		if first.Agent.ModelConfig.Provider != second.Agent.ModelConfig.Provider {
			t.Errorf("provider mismatch: %q vs %q", first.Agent.ModelConfig.Provider, second.Agent.ModelConfig.Provider)
		}
		if first.Agent.ModelConfig.Name != second.Agent.ModelConfig.Name {
			t.Errorf("name mismatch: %q vs %q", first.Agent.ModelConfig.Name, second.Agent.ModelConfig.Name)
		}
		if first.Agent.ModelConfig.Options["enable_prompt_caching"] != second.Agent.ModelConfig.Options["enable_prompt_caching"] {
			t.Errorf("options mismatch")
		}
	})
}

func TestParse_QueueDepthDefault(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
  concurrency: queue
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Agent.QueueDepth != model.DefaultQueueDepth {
		t.Errorf("queue_depth = %d, want %d (default)", p.Agent.QueueDepth, model.DefaultQueueDepth)
	}
}

func TestParse_QueueDepthExplicit(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
  concurrency: queue
  queue_depth: 5
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Agent.QueueDepth != 5 {
		t.Errorf("queue_depth = %d, want 5", p.Agent.QueueDepth)
	}
}

func TestParse_QueueDepthZeroUsesDefault(t *testing.T) {
	// queue_depth: 0 means "use default" — consistent with how max_tokens/max_tool_calls
	// handle zero values.
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
  concurrency: queue
  queue_depth: 0
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Agent.QueueDepth != model.DefaultQueueDepth {
		t.Errorf("queue_depth = %d, want %d (default for zero)", p.Agent.QueueDepth, model.DefaultQueueDepth)
	}
}

func TestParse_FeedbackEnabled(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
  feedback:
    enabled: true
    timeout: 30m
    on_timeout: fail
agent:
  task: do it
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.Capabilities.Feedback.Enabled {
		t.Error("expected Feedback.Enabled = true")
	}
	if p.Capabilities.Feedback.Timeout != "30m" {
		t.Errorf("timeout = %q, want %q", p.Capabilities.Feedback.Timeout, "30m")
	}
	if p.Capabilities.Feedback.OnTimeout != model.FeedbackOnTimeoutFail {
		t.Errorf("on_timeout = %q, want %q", p.Capabilities.Feedback.OnTimeout, model.FeedbackOnTimeoutFail)
	}
}

func TestParse_FeedbackDisabled(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "explicit enabled: false",
			yaml: `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
  feedback:
    enabled: false
agent:
  task: do it
`,
		},
		{
			name: "feedback key absent",
			yaml: `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse(tc.yaml, "anthropic", "claude-sonnet-4-6")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Capabilities.Feedback.Enabled {
				t.Error("expected Feedback.Enabled = false")
			}
		})
	}
}

func TestParse_FeedbackOldFormat(t *testing.T) {
	// Old list format is accepted for backward compatibility.
	// The list refs are discarded; Enabled is set to true.
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
  feedback:
    - server.feedback_tool
agent:
  task: do it
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.Capabilities.Feedback.Enabled {
		t.Error("expected Feedback.Enabled = true for old list format (backward compat)")
	}
}

func TestParse_FeedbackDefaults(t *testing.T) {
	// When enabled: true but no timeout/on_timeout specified, on_timeout defaults to "fail"
	// and timeout is empty.
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
  feedback:
    enabled: true
agent:
  task: do it
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.Capabilities.Feedback.Enabled {
		t.Error("expected Feedback.Enabled = true")
	}
	if p.Capabilities.Feedback.Timeout != "" {
		t.Errorf("timeout = %q, want empty", p.Capabilities.Feedback.Timeout)
	}
	if p.Capabilities.Feedback.OnTimeout != model.FeedbackOnTimeoutFail {
		t.Errorf("on_timeout = %q, want %q (default)", p.Capabilities.Feedback.OnTimeout, model.FeedbackOnTimeoutFail)
	}
}

func TestParse_FeedbackDisabledClearsFields(t *testing.T) {
	// When enabled: false, timeout and on_timeout are cleared by the parser
	// even if the operator wrote them, avoiding confusing validation errors.
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
  feedback:
    enabled: false
    timeout: 30m
    on_timeout: fail
agent:
  task: do it
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Capabilities.Feedback.Enabled {
		t.Error("expected Feedback.Enabled = false")
	}
	if p.Capabilities.Feedback.Timeout != "" {
		t.Errorf("timeout = %q, want empty (cleared when disabled)", p.Capabilities.Feedback.Timeout)
	}
	if p.Capabilities.Feedback.OnTimeout != "" {
		t.Errorf("on_timeout = %q, want empty (cleared when disabled)", p.Capabilities.Feedback.OnTimeout)
	}
}

func TestParse_PollTrigger_WithChecks(t *testing.T) {
	raw := `
name: poll-test
trigger:
  type: poll
  interval: 5m
  match: any
  checks:
    - tool: my-server.check_items
      input:
        repo: gleipnir
      path: "$.status"
      equals: degraded
    - tool: my-server.check_items
      path: "$.count"
      greater_than: 10
capabilities:
  tools:
    - tool: my-server.check_items
agent:
  task: process poll result
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.Trigger.Type != model.TriggerTypePoll {
		t.Errorf("trigger.type = %q, want %q", p.Trigger.Type, model.TriggerTypePoll)
	}
	if p.Trigger.Interval != 5*time.Minute {
		t.Errorf("trigger.interval = %v, want 5m", p.Trigger.Interval)
	}
	if p.Trigger.Match != model.MatchAny {
		t.Errorf("trigger.match = %q, want %q", p.Trigger.Match, model.MatchAny)
	}
	if len(p.Trigger.Checks) != 2 {
		t.Fatalf("len(checks) = %d, want 2", len(p.Trigger.Checks))
	}

	c0 := p.Trigger.Checks[0]
	if c0.Tool != "my-server.check_items" {
		t.Errorf("checks[0].tool = %q, want %q", c0.Tool, "my-server.check_items")
	}
	if c0.Input["repo"] != "gleipnir" {
		t.Errorf("checks[0].input[repo] = %v, want %q", c0.Input["repo"], "gleipnir")
	}
	if c0.Path != "$.status" {
		t.Errorf("checks[0].path = %q, want %q", c0.Path, "$.status")
	}
	if c0.Comparator != "equals" {
		t.Errorf("checks[0].comparator = %q, want %q", c0.Comparator, "equals")
	}
	if c0.Value != "degraded" {
		t.Errorf("checks[0].value = %v, want %q", c0.Value, "degraded")
	}

	c1 := p.Trigger.Checks[1]
	if c1.Comparator != "greater_than" {
		t.Errorf("checks[1].comparator = %q, want %q", c1.Comparator, "greater_than")
	}
	if c1.Value != 10 {
		t.Errorf("checks[1].value = %v, want 10", c1.Value)
	}
}

func TestParse_PollTrigger_DefaultMatch(t *testing.T) {
	// When match is omitted it must default to "all".
	raw := `
name: poll-default-match
trigger:
  type: poll
  interval: 5m
  checks:
    - tool: s.check
      path: "$.status"
      equals: ok
capabilities:
  tools:
    - tool: s.check
agent:
  task: do it
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Trigger.Match != model.MatchAll {
		t.Errorf("trigger.match = %q, want %q (default)", p.Trigger.Match, model.MatchAll)
	}
}

func TestParse_PollTrigger_BadInterval(t *testing.T) {
	// An unparseable interval leaves Interval as zero; the validator catches it.
	raw := `
name: poll-bad
trigger:
  type: poll
  interval: not-a-duration
  checks:
    - tool: s.check
      path: "$.status"
      equals: ok
capabilities:
  tools:
    - tool: s.check
agent:
  task: do it
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Trigger.Interval != 0 {
		t.Errorf("expected Interval=0 for bad duration, got %v", p.Trigger.Interval)
	}
}

func TestParse_CustomPreamble(t *testing.T) {
	raw := `
name: test
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  preamble: Custom preamble text
  task: do it
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Agent.Preamble != "Custom preamble text" {
		t.Errorf("preamble = %q, want %q", p.Agent.Preamble, "Custom preamble text")
	}
}

// TestParse_WebhookAuth covers trigger.auth parsing for webhook policies.
func TestParse_WebhookAuth(t *testing.T) {
	cases := []struct {
		name     string
		yaml     string
		wantAuth model.WebhookAuthMode
	}{
		{
			name: "absent auth defaults to hmac",
			yaml: `
name: open
trigger:
  type: webhook
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
`,
			wantAuth: model.WebhookAuthHMAC,
		},
		{
			name: "explicit auth: bearer is preserved",
			yaml: `
name: bearer
trigger:
  type: webhook
  auth: bearer
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
`,
			wantAuth: model.WebhookAuthBearer,
		},
		{
			name: "explicit auth: hmac is preserved",
			yaml: `
name: hmac
trigger:
  type: webhook
  auth: hmac
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
`,
			wantAuth: model.WebhookAuthHMAC,
		},
		{
			name: "explicit auth: none is preserved",
			yaml: `
name: none
trigger:
  type: webhook
  auth: none
capabilities:
  tools:
    - tool: s.t
agent:
  task: do it
`,
			wantAuth: model.WebhookAuthNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse(tc.yaml, "anthropic", "claude-sonnet-4-6")
			if err != nil {
				t.Fatalf("Parse() error: %v", err)
			}
			if p.Trigger.WebhookAuth != tc.wantAuth {
				t.Errorf("WebhookAuth = %q, want %q", p.Trigger.WebhookAuth, tc.wantAuth)
			}
		})
	}
}

func TestParse_CronTrigger(t *testing.T) {
	raw := `
name: cron-test
trigger:
  type: cron
  cron_expr: "0 9 * * 1"
capabilities:
  tools:
    - tool: srv.check
agent:
  task: cron task
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Trigger.Type != model.TriggerTypeCron {
		t.Errorf("trigger.type = %q, want %q", p.Trigger.Type, model.TriggerTypeCron)
	}
	if p.Trigger.CronExpr != "0 9 * * 1" {
		t.Errorf("trigger.cron_expr = %q, want %q", p.Trigger.CronExpr, "0 9 * * 1")
	}
}

func TestParse_CronTrigger_TrimsWhitespace(t *testing.T) {
	raw := `
name: cron-whitespace
trigger:
  type: cron
  cron_expr: "  */15 * * * *  "
capabilities:
  tools:
    - tool: srv.check
agent:
  task: cron task
`
	p, err := Parse(raw, "anthropic", "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Trigger.CronExpr != "*/15 * * * *" {
		t.Errorf("trigger.cron_expr = %q, want %q (whitespace should be trimmed)", p.Trigger.CronExpr, "*/15 * * * *")
	}
}
