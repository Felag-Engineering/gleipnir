// Package policy implements YAML parsing, structural validation, system prompt
// rendering, and the service layer for policy lifecycle operations.
package policy

import (
	"fmt"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
	"gopkg.in/yaml.v3"
)

const (
	defaultMaxTokensPerRun    = 20000
	defaultMaxToolCallsPerRun = 50
	defaultModel              = "claude-sonnet-4-6"
	// DefaultProvider is the LLM provider used when the policy omits the
	// provider field. Exported so tests can reference it without hard-coding.
	DefaultProvider = "anthropic"

	// MaxPolicyYAMLBytes is the maximum allowed size of a raw policy YAML blob.
	// Enforced before unmarshalling to prevent billion-laughs style DoS attacks.
	MaxPolicyYAMLBytes = 64 * 1024 // 64 KiB
)

// ParseError wraps a YAML decode failure so callers can distinguish malformed
// YAML from validation or storage errors without inspecting error strings.
type ParseError struct {
	Cause error
}

func (e *ParseError) Error() string { return fmt.Sprintf("parse policy yaml: %v", e.Cause) }
func (e *ParseError) Unwrap() error { return e.Cause }

// Parse parses a raw YAML policy blob into a ParsedPolicy.
// It applies sensible defaults for optional fields but does not validate
// the result — call Validate separately.
func Parse(raw string) (*model.ParsedPolicy, error) {
	if len(raw) > MaxPolicyYAMLBytes {
		return nil, fmt.Errorf("policy YAML exceeds maximum size (%d bytes > %d bytes)", len(raw), MaxPolicyYAMLBytes)
	}

	var r rawPolicy
	if err := yaml.Unmarshal([]byte(raw), &r); err != nil {
		return nil, &ParseError{Cause: err}
	}

	p := &model.ParsedPolicy{
		Name:        r.Name,
		Description: r.Description,
	}

	p.Trigger = convertTrigger(r.Trigger)
	p.Capabilities = convertCapabilities(r.Capabilities)
	p.Agent = convertAgent(r.Agent)

	return p, nil
}

// convertTrigger maps the raw YAML trigger block to a typed TriggerConfig.
// Scheduled-specific fields are only populated when the trigger type is "scheduled".
func convertTrigger(r rawTrigger) model.TriggerConfig {
	tc := model.TriggerConfig{
		Type:          model.TriggerType(r.Type),
		WebhookSecret: r.WebhookSecret,
	}

	if tc.Type == model.TriggerTypeScheduled {
		for _, s := range r.FireAt {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				// Skip unparseable entries; validator will catch them.
				continue
			}
			tc.FireAt = append(tc.FireAt, t.UTC())
		}
	}

	return tc
}

// convertCapabilities maps raw YAML capability entries to typed config.
// Defaults: approval → none, on_timeout → reject (only for approval: required).
func convertCapabilities(r rawCapabilities) model.CapabilitiesConfig {
	cc := model.CapabilitiesConfig{
		Feedback: r.Feedback,
	}

	for _, t := range r.Tools {
		approval := model.ApprovalMode(t.Approval)
		if approval == "" {
			approval = model.ApprovalModeNone
		}

		// Only populate timeout/on_timeout when approval is required.
		// For approval: none, these fields are ignored at runtime.
		var timeout string
		var onTimeout model.OnTimeout
		if approval == model.ApprovalModeRequired {
			timeout = t.Timeout
			onTimeout = model.OnTimeout(t.OnTimeout)
			if onTimeout == "" {
				onTimeout = model.OnTimeoutReject
			}
		}

		cc.Tools = append(cc.Tools, model.ToolCapability{
			Tool:      t.Tool,
			Approval:  approval,
			Timeout:   timeout,
			OnTimeout: onTimeout,
			Params:    t.Params,
		})
	}

	return cc
}

// convertAgent maps raw YAML agent config to typed AgentConfig.
// Defaults: model → claude-sonnet-4-6, provider → anthropic, max_tokens_per_run → 20000, max_tool_calls_per_run → 50, concurrency → skip.
func convertAgent(r rawAgent) model.AgentConfig {
	ac := model.AgentConfig{
		Preamble: strings.TrimSpace(r.Preamble),
		Task:     strings.TrimSpace(r.Task),
	}

	if r.Model == "" {
		ac.Model = defaultModel
	} else {
		ac.Model = r.Model
	}

	if r.Provider == "" {
		ac.Provider = DefaultProvider
	} else {
		ac.Provider = r.Provider
	}

	ac.Limits.MaxTokensPerRun = r.Limits.MaxTokensPerRun
	if ac.Limits.MaxTokensPerRun == 0 {
		ac.Limits.MaxTokensPerRun = defaultMaxTokensPerRun
	}

	ac.Limits.MaxToolCallsPerRun = r.Limits.MaxToolCallsPerRun
	if ac.Limits.MaxToolCallsPerRun == 0 {
		ac.Limits.MaxToolCallsPerRun = defaultMaxToolCallsPerRun
	}

	ac.Concurrency = model.ConcurrencyPolicy(r.Concurrency)
	if ac.Concurrency == "" {
		ac.Concurrency = model.ConcurrencySkip
	}

	return ac
}

// rawPolicy is the intermediate YAML representation used during parsing.
// Field names match the policy schema documented in schemas/policy.yaml.
type rawPolicy struct {
	Name         string          `yaml:"name"`
	Description  string          `yaml:"description"`
	Trigger      rawTrigger      `yaml:"trigger"`
	Capabilities rawCapabilities `yaml:"capabilities"`
	Agent        rawAgent        `yaml:"agent"`
}

type rawTrigger struct {
	Type          string   `yaml:"type"`
	FireAt        []string `yaml:"fire_at"`        // scheduled only, RFC3339 timestamps
	WebhookSecret string   `yaml:"webhook_secret"` // webhook only
}

type rawCapabilities struct {
	Tools    []rawTool `yaml:"tools"`
	Feedback []string  `yaml:"feedback"`
}

type rawTool struct {
	Tool      string         `yaml:"tool"`
	Approval  string         `yaml:"approval"`
	Timeout   string         `yaml:"timeout"`
	OnTimeout string         `yaml:"on_timeout"`
	Params    map[string]any `yaml:"params"`
}

type rawAgent struct {
	Model       string    `yaml:"model"`
	Provider    string    `yaml:"provider"`
	Preamble    string    `yaml:"preamble"`
	Task        string    `yaml:"task"`
	Limits      rawLimits `yaml:"limits"`
	Concurrency string    `yaml:"concurrency"`
}

type rawLimits struct {
	MaxTokensPerRun    int `yaml:"max_tokens_per_run"`
	MaxToolCallsPerRun int `yaml:"max_tool_calls_per_run"`
}
