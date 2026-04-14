package policy

import (
	"fmt"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
	"gopkg.in/yaml.v3"
)

// ValidationError collects all validation failures for a policy. It is
// returned by Validate when one or more fields are invalid.
type ValidationError struct {
	Errors []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("policy validation failed: %v", e.Errors)
}

// Validate checks a ParsedPolicy for required fields, valid enum values,
// and internal consistency (e.g. replace concurrency incompatible with
// approval-required tools). Returns nil if valid.
func Validate(p *model.ParsedPolicy) error {
	var errs []string

	if p.Name == "" {
		errs = append(errs, "name is required")
	}

	errs = append(errs, validateTrigger(p.Trigger)...)
	errs = append(errs, validateCapabilities(p.Capabilities)...)
	errs = append(errs, validateAgent(p.Agent, p.Capabilities)...)

	if len(errs) > 0 {
		return &ValidationError{Errors: errs}
	}
	return nil
}

// validateTrigger checks the trigger type enum and type-specific required fields.
func validateTrigger(t model.TriggerConfig) []string {
	var errs []string

	if !t.Type.Valid() {
		errs = append(errs, fmt.Sprintf("trigger.type %q is invalid; must be webhook, manual, scheduled, or poll", t.Type))
		return errs // can't validate type-specific fields without a valid type
	}

	switch t.Type {
	case model.TriggerTypeWebhook:
		if t.WebhookAuth != "" && !t.WebhookAuth.Valid() {
			errs = append(errs, fmt.Sprintf("trigger.auth %q is invalid; must be hmac, bearer, or none", t.WebhookAuth))
		}

	case model.TriggerTypeManual:
		// No additional fields required.

	case model.TriggerTypeScheduled:
		if len(t.FireAt) == 0 {
			errs = append(errs, "trigger.fire_at is required for scheduled triggers and must contain at least one timestamp")
		}
		// Validate each individual timestamp can be parsed (parser skips bad entries,
		// so an entry count mismatch signals parse failures).
		// Note: we do not validate that timestamps are in the future here, because
		// historical timestamps in existing policies are valid on read.

	case model.TriggerTypePoll:
		if t.Interval <= 0 {
			errs = append(errs, "trigger.interval is required for poll triggers and must be a positive duration (e.g. \"5m\", \"1h\")")
		} else if t.Interval < time.Minute {
			errs = append(errs, "trigger.interval must be at least 1m to prevent excessive polling")
		}

		if t.Match != "" && !t.Match.Valid() {
			errs = append(errs, fmt.Sprintf("trigger.match %q is invalid; must be all or any", t.Match))
		}

		if len(t.Checks) == 0 {
			errs = append(errs, "trigger.checks is required for poll triggers and must contain at least one check")
		}

		for i, c := range t.Checks {
			if c.Tool == "" {
				errs = append(errs, fmt.Sprintf("trigger.checks[%d].tool is required", i))
			} else if !isValidToolRef(c.Tool) {
				errs = append(errs, fmt.Sprintf("trigger.checks[%d].tool %q must use dot notation (server_name.tool_name)", i, c.Tool))
			}
			if c.Path == "" {
				errs = append(errs, fmt.Sprintf("trigger.checks[%d].path is required", i))
			}
			if c.Comparator == "" {
				errs = append(errs, fmt.Sprintf("trigger.checks[%d] must specify exactly one comparator (equals, not_equals, greater_than, less_than, contains)", i))
			} else if !c.Comparator.Valid() {
				errs = append(errs, fmt.Sprintf("trigger.checks[%d].comparator %q is invalid; must be equals, not_equals, greater_than, less_than, or contains", i, c.Comparator))
			}
		}
	}

	return errs
}

// validateCapabilities checks that at least one capability is present (tool or
// feedback), tool references use valid dot notation, there are no duplicates,
// and approval/timeout and feedback fields are well-formed.
func validateCapabilities(c model.CapabilitiesConfig) []string {
	var errs []string

	if len(c.Tools) == 0 && !c.Feedback.Enabled {
		errs = append(errs, "at least one capability is required (tool or feedback)")
	}

	seen := make(map[string]bool)

	for i, t := range c.Tools {
		if t.Tool == "" {
			errs = append(errs, fmt.Sprintf("capabilities.tools[%d].tool is required", i))
			continue
		}
		if !isValidToolRef(t.Tool) {
			errs = append(errs, fmt.Sprintf("capabilities.tools[%d].tool %q must use dot notation (server_name.tool_name)", i, t.Tool))
		}
		if seen[t.Tool] {
			errs = append(errs, fmt.Sprintf("capabilities.tools[%d].tool %q is a duplicate", i, t.Tool))
		}
		seen[t.Tool] = true

		if !t.Approval.Valid() {
			errs = append(errs, fmt.Sprintf("capabilities.tools[%d].approval %q is invalid; must be none or required", i, t.Approval))
		}

		if t.Approval == model.ApprovalModeRequired {
			if t.Timeout != "" {
				if _, err := time.ParseDuration(t.Timeout); err != nil {
					errs = append(errs, fmt.Sprintf("capabilities.tools[%d].timeout %q is not a valid duration: %v", i, t.Timeout, err))
				}
			}
			if !t.OnTimeout.Valid() {
				errs = append(errs, fmt.Sprintf("capabilities.tools[%d].on_timeout %q is invalid; must be reject", i, t.OnTimeout))
			}
		}
	}

	errs = append(errs, validateFeedback(c.Feedback)...)

	return errs
}

// validateFeedback checks the feedback config block for valid duration and
// on_timeout values. Fields are only validated when feedback is enabled; the
// parser already clears timeout/on_timeout when disabled so there is nothing
// to validate in the disabled case.
func validateFeedback(f model.FeedbackConfig) []string {
	if !f.Enabled {
		return nil
	}
	var errs []string
	if f.Timeout != "" {
		if _, err := time.ParseDuration(f.Timeout); err != nil {
			errs = append(errs, fmt.Sprintf("capabilities.feedback.timeout %q is not a valid duration", f.Timeout))
		}
	}
	if f.OnTimeout != "" && !f.OnTimeout.Valid() {
		errs = append(errs, fmt.Sprintf("capabilities.feedback.on_timeout %q is invalid; must be fail", f.OnTimeout))
	}
	return errs
}

// validateAgent checks agent config and cross-validates against capabilities.
// Specifically: replace concurrency is not valid if any tool has
// approval: required (the in-flight run cannot be safely cancelled mid-approval).
// Model/provider validation is handled at the service layer via OptionsValidator.
func validateAgent(a model.AgentConfig, c model.CapabilitiesConfig) []string {
	var errs []string

	if a.Task == "" {
		errs = append(errs, "agent.task is required")
	}

	// "claude-code" was a subprocess runner removed in issue #611. Policies that
	// still declare this provider must fail with an actionable error rather than
	// silently misbehaving at run time.
	if a.ModelConfig.Provider == "claude-code" {
		errs = append(errs, `model.provider "claude-code" is no longer supported; remove the policy or switch to an llm provider (anthropic, google, openai, openaicompat)`)
	}

	if a.Limits.MaxTokensPerRun <= 0 {
		errs = append(errs, "agent.limits.max_tokens_per_run must be positive")
	}
	if a.Limits.MaxToolCallsPerRun <= 0 {
		errs = append(errs, "agent.limits.max_tool_calls_per_run must be positive")
	}

	if !a.Concurrency.Valid() {
		errs = append(errs, fmt.Sprintf("agent.concurrency %q is invalid; must be skip, queue, parallel, or replace", a.Concurrency))
	}

	if a.QueueDepth < 0 {
		errs = append(errs, "agent.queue_depth must not be negative")
	}

	// Note: cross-validating queue_depth against concurrency mode is impractical
	// because the parser defaults queue_depth to 10 when unset (Go zero value),
	// so we cannot distinguish "user explicitly set queue_depth" from "default."

	// Cross-validation: replace concurrency is incompatible with approval-required tools.
	if a.Concurrency == model.ConcurrencyReplace {
		for _, t := range c.Tools {
			if t.Approval == model.ApprovalModeRequired {
				errs = append(errs, "agent.concurrency \"replace\" is not valid when any tool has approval: required")
				break
			}
		}
	}

	return errs
}

// CheckLegacyWebhookSecret parses rawYAML and returns an error if the trigger
// block contains a webhook_secret field. This field is no longer stored in
// YAML (ADR-034): the secret lives in policies.webhook_secret_encrypted.
// Called from the service save path (Create / Update) to prevent operators
// from re-introducing the legacy field.
func CheckLegacyWebhookSecret(rawYAML string) error {
	var r rawPolicy
	if err := yaml.Unmarshal([]byte(rawYAML), &r); err != nil {
		return nil // parse errors are caught earlier; ignore here
	}
	if r.Trigger.WebhookSecret == "" {
		return nil
	}
	if r.Trigger.Type == string(model.TriggerTypeWebhook) {
		return fmt.Errorf(
			"trigger.webhook_secret is no longer stored in YAML; remove the field and POST /api/v1/policies/{id}/webhook/rotate to set a secret; select auth: hmac | bearer | none to choose verification mode",
		)
	}
	return fmt.Errorf("trigger.webhook_secret is only valid for webhook triggers; remove the field")
}

// isValidToolRef checks that a tool reference uses dot notation: server_name.tool_name.
// Both parts must be non-empty.
func isValidToolRef(ref string) bool {
	parts := strings.SplitN(ref, ".", 2)
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}
