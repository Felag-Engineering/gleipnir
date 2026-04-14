// Package policy implements YAML parsing, structural validation, system prompt
// rendering, and the service layer for policy lifecycle operations.
package policy

import (
	"fmt"
	"log/slog"
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
// Type-specific fields are only populated for their respective trigger types.
func convertTrigger(r rawTrigger) model.TriggerConfig {
	tc := model.TriggerConfig{
		Type: model.TriggerType(r.Type),
	}

	if tc.Type == model.TriggerTypeWebhook {
		if r.Auth != "" {
			tc.WebhookAuth = model.WebhookAuthMode(r.Auth)
		} else {
			tc.WebhookAuth = model.WebhookAuthHMAC
		}
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

	if tc.Type == model.TriggerTypePoll {
		if r.Interval != "" {
			d, err := time.ParseDuration(r.Interval)
			if err == nil {
				tc.Interval = d
			}
			// If parse fails, leave zero — validator will catch it.
		}

		tc.Match = model.MatchMode(r.Match)
		if tc.Match == "" {
			tc.Match = model.MatchAll
		}

		for _, rc := range r.Checks {
			check := model.PollCheck{
				Tool:  rc.Tool,
				Input: rc.Input,
				Path:  rc.Path,
			}
			// Determine which comparator field is set. The first non-nil one wins.
			// The validator enforces that exactly one is set.
			switch {
			case rc.Equals != nil:
				check.Comparator = model.ComparatorEquals
				check.Value = rc.Equals
			case rc.NotEquals != nil:
				check.Comparator = model.ComparatorNotEquals
				check.Value = rc.NotEquals
			case rc.GreaterThan != nil:
				check.Comparator = model.ComparatorGreaterThan
				check.Value = rc.GreaterThan
			case rc.LessThan != nil:
				check.Comparator = model.ComparatorLessThan
				check.Value = rc.LessThan
			case rc.Contains != nil:
				check.Comparator = model.ComparatorContains
				check.Value = rc.Contains
			}
			tc.Checks = append(tc.Checks, check)
		}
	}

	return tc
}

// convertCapabilities maps raw YAML capability entries to typed config.
// Defaults: approval → none, on_timeout → reject (only for approval: required).
func convertCapabilities(r rawCapabilities) model.CapabilitiesConfig {
	cc := model.CapabilitiesConfig{
		Feedback: convertFeedback(r.Feedback),
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

// convertFeedback maps the raw YAML feedback node to a FeedbackConfig.
// It supports the new config-block format and backward-compatible old list format.
// Old format: feedback: ["server.tool", ...] — treated as enabled: true with a
// deprecation warning logged. The old tool refs are discarded since they were MCP
// tool references that have no meaning in the new model.
func convertFeedback(node yaml.Node) model.FeedbackConfig {
	// Kind 0 means the field was absent from the YAML document.
	if node.Kind == 0 || node.Tag == "!!null" {
		return model.FeedbackConfig{Enabled: false}
	}

	switch node.Kind {
	case yaml.SequenceNode:
		// Old list format: capabilities.feedback: ["server.tool", ...]
		slog.Warn("capabilities.feedback list format is deprecated; use feedback: { enabled: true }")
		return model.FeedbackConfig{Enabled: true}

	case yaml.MappingNode:
		var rf rawFeedback
		if err := node.Decode(&rf); err != nil {
			// Malformed mapping — treat as disabled.
			return model.FeedbackConfig{Enabled: false}
		}
		if !rf.Enabled {
			// When disabled, clear timeout/on_timeout silently regardless of
			// what the operator wrote. This avoids confusing validation errors
			// for fields that have no effect.
			return model.FeedbackConfig{Enabled: false}
		}
		onTimeout := model.FeedbackOnTimeout(rf.OnTimeout)
		if onTimeout == "" {
			onTimeout = model.FeedbackOnTimeoutFail
		}
		return model.FeedbackConfig{
			Enabled:   true,
			Timeout:   rf.Timeout,
			OnTimeout: onTimeout,
		}
	}

	return model.FeedbackConfig{Enabled: false}
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
	Type          string     `yaml:"type"`
	FireAt        []string   `yaml:"fire_at"`        // scheduled only, RFC3339 timestamps
	WebhookSecret string     `yaml:"webhook_secret"` // rejected at save time; never propagated to TriggerConfig
	Auth          string     `yaml:"auth"`           // webhook only: hmac | bearer | none
	Interval      string     `yaml:"interval"`       // poll only, Go duration string (e.g. "5m")
	Match         string     `yaml:"match"`          // poll only, "all" or "any", default: "all"
	Checks        []rawCheck `yaml:"checks"`         // poll only, at least one required
}

// rawCheck is one entry in a poll trigger's checks list.
// Exactly one comparator field (Equals, NotEquals, etc.) should be set per check.
type rawCheck struct {
	Tool        string         `yaml:"tool"`
	Input       map[string]any `yaml:"input"`
	Path        string         `yaml:"path"`
	Equals      any            `yaml:"equals"`
	NotEquals   any            `yaml:"not_equals"`
	GreaterThan any            `yaml:"greater_than"`
	LessThan    any            `yaml:"less_than"`
	Contains    any            `yaml:"contains"`
}

type rawCapabilities struct {
	Tools    []rawTool `yaml:"tools"`
	Feedback yaml.Node `yaml:"feedback"`
}

// rawFeedback is the new config-block format for capabilities.feedback.
type rawFeedback struct {
	Enabled   bool   `yaml:"enabled"`
	Timeout   string `yaml:"timeout"`
	OnTimeout string `yaml:"on_timeout"`
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
