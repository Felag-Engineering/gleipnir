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
// defaultProvider and defaultModel are used when the policy YAML omits the
// top-level model section or leaves provider/name blank.
func Parse(raw string, defaultProvider, defaultModel string) (*model.ParsedPolicy, error) {
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
	mc := resolveModelConfig(r.Model, defaultProvider, defaultModel)
	p.Agent = convertAgent(r.Agent, mc)

	return p, nil
}

// resolveModelConfig determines the ModelConfig from the top-level `model:`
// section, applying defaults for any missing fields.
func resolveModelConfig(topLevel *rawModel, defaultProvider, defaultModel string) model.ModelConfig {
	if topLevel == nil {
		return model.ModelConfig{
			Provider: defaultProvider,
			Name:     defaultModel,
		}
	}

	provider := topLevel.Provider
	if provider == "" {
		provider = defaultProvider
	}
	name := topLevel.Name
	if name == "" {
		name = defaultModel
	}
	return model.ModelConfig{
		Provider: provider,
		Name:     name,
		Options:  topLevel.Options,
	}
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
// The resolved ModelConfig is passed in from resolveModelConfig — this function
// handles everything else (preamble, task, limits, concurrency).
func convertAgent(r rawAgent, mc model.ModelConfig) model.AgentConfig {
	ac := model.AgentConfig{
		Preamble:    strings.TrimSpace(r.Preamble),
		Task:        strings.TrimSpace(r.Task),
		ModelConfig: mc,
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

	ac.QueueDepth = r.QueueDepth
	if ac.QueueDepth == 0 {
		ac.QueueDepth = model.DefaultQueueDepth
	}

	return ac
}

// rawPolicy is the intermediate YAML representation used during parsing.
// Field names match the policy schema documented in schemas/policy.yaml.
type rawPolicy struct {
	Name         string          `yaml:"name"`
	Description  string          `yaml:"description"`
	Model        *rawModel       `yaml:"model"`
	Trigger      rawTrigger      `yaml:"trigger"`
	Capabilities rawCapabilities `yaml:"capabilities"`
	Agent        rawAgent        `yaml:"agent"`
}

// rawModel holds the top-level `model:` section introduced in issue #344.
// A pointer is used in rawPolicy so we can distinguish "key absent" (nil)
// from "key present but empty" (non-nil with zero-value fields).
type rawModel struct {
	Provider string         `yaml:"provider"`
	Name     string         `yaml:"name"`
	Options  map[string]any `yaml:"options"`
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
	Preamble    string    `yaml:"preamble"`
	Task        string    `yaml:"task"`
	Limits      rawLimits `yaml:"limits"`
	Concurrency string    `yaml:"concurrency"`
	QueueDepth  int       `yaml:"queue_depth"`
}

type rawLimits struct {
	MaxTokensPerRun    int `yaml:"max_tokens_per_run"`
	MaxToolCallsPerRun int `yaml:"max_tool_calls_per_run"`
}
