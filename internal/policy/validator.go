package policy

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/model"
)

// Issue pairs a canonical field path with its error message. Field is empty for
// cross-cutting errors that don't map to a single input.
type Issue struct {
	Field   string
	Message string
}

// ValidationError collects all validation failures for a policy. It is
// returned by Validate when one or more fields are invalid.
type ValidationError struct {
	Errors []Issue
}

// Error returns a semicolon-joined string suitable for logs and legacy callers
// that only read the error message. Each issue is rendered as "field: message"
// when a field is set, or just "message" for cross-cutting issues.
func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Errors))
	for _, iss := range e.Errors {
		if iss.Field != "" {
			parts = append(parts, iss.Field+": "+iss.Message)
		} else {
			parts = append(parts, iss.Message)
		}
	}
	return "policy validation failed: " + strings.Join(parts, "; ")
}

// Validate checks a ParsedPolicy for required fields, valid enum values,
// and internal consistency (e.g. replace concurrency incompatible with
// approval-required tools). Returns nil if valid.
func Validate(p *model.ParsedPolicy) error {
	var issues []Issue

	// add appends a tagged Issue. field may be empty for cross-cutting messages.
	add := func(field, format string, args ...any) {
		issues = append(issues, Issue{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if p.Name == "" {
		add("name", "name is required")
	}

	issues = append(issues, validateTrigger(p.Trigger)...)
	issues = append(issues, validateCapabilities(p.Capabilities)...)
	issues = append(issues, validateAgent(p.Agent, p.Capabilities)...)

	if len(issues) > 0 {
		return &ValidationError{Errors: issues}
	}
	return nil
}

// validateTrigger checks the trigger type enum and type-specific required fields.
func validateTrigger(t model.TriggerConfig) []Issue {
	var issues []Issue

	add := func(field, format string, args ...any) {
		issues = append(issues, Issue{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if !t.Type.Valid() {
		add("trigger.type", "trigger.type %q is invalid; must be webhook, manual, scheduled, poll, or cron", t.Type)
		return issues // can't validate type-specific fields without a valid type
	}

	switch t.Type {
	case model.TriggerTypeWebhook:
		if t.WebhookAuth != "" && !t.WebhookAuth.Valid() {
			add("trigger.auth", "trigger.auth %q is invalid; must be hmac, bearer, or none", t.WebhookAuth)
		}
		if t.Match != "" && !t.Match.Valid() {
			add("trigger.match", "trigger.match %q is invalid; must be all or any", t.Match)
		}
		for i, c := range t.Checks {
			if c.Path == "" {
				add(fmt.Sprintf("trigger.checks[%d].path", i), "trigger.checks[%d].path is required", i)
			}
			if c.Comparator == "" {
				add(fmt.Sprintf("trigger.checks[%d].comparator", i), "trigger.checks[%d] must specify exactly one comparator (equals, not_equals, greater_than, less_than, contains)", i)
			} else if !c.Comparator.Valid() {
				add(fmt.Sprintf("trigger.checks[%d].comparator", i), "trigger.checks[%d].comparator %q is invalid; must be equals, not_equals, greater_than, less_than, or contains", i, c.Comparator)
			}
		}

	case model.TriggerTypeManual:
		// No additional fields required.

	case model.TriggerTypeScheduled:
		if len(t.FireAt) == 0 {
			add("trigger.fire_at", "trigger.fire_at is required for scheduled triggers and must contain at least one timestamp")
		}
		// Validate each individual timestamp can be parsed (parser skips bad entries,
		// so an entry count mismatch signals parse failures).
		// Note: we do not validate that timestamps are in the future here, because
		// historical timestamps in existing policies are valid on read.

	case model.TriggerTypeCron:
		if t.CronExpr == "" {
			add("trigger.cron_expr", "trigger.cron_expr is required for cron triggers")
		} else {
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			if _, err := parser.Parse(t.CronExpr); err != nil {
				add("trigger.cron_expr", "trigger.cron_expr invalid expression: %v", err)
			}
		}

	case model.TriggerTypePoll:
		if t.Interval <= 0 {
			add("trigger.interval", "trigger.interval is required for poll triggers and must be a positive duration (e.g. \"5m\", \"1h\")")
		} else if t.Interval < time.Minute {
			add("trigger.interval", "trigger.interval must be at least 1m to prevent excessive polling")
		}

		if t.Match != "" && !t.Match.Valid() {
			add("trigger.match", "trigger.match %q is invalid; must be all or any", t.Match)
		}

		if len(t.Checks) == 0 {
			add("trigger.checks", "trigger.checks is required for poll triggers and must contain at least one check")
		}

		for i, c := range t.Checks {
			if c.Tool == "" {
				add(fmt.Sprintf("trigger.checks[%d].tool", i), "trigger.checks[%d].tool is required", i)
			} else if !isValidToolRef(c.Tool) {
				add(fmt.Sprintf("trigger.checks[%d].tool", i), "trigger.checks[%d].tool %q must use dot notation (server_name.tool_name)", i, c.Tool)
			}
			if c.Path == "" {
				add(fmt.Sprintf("trigger.checks[%d].path", i), "trigger.checks[%d].path is required", i)
			}
			if c.Comparator == "" {
				add(fmt.Sprintf("trigger.checks[%d].comparator", i), "trigger.checks[%d] must specify exactly one comparator (equals, not_equals, greater_than, less_than, contains)", i)
			} else if !c.Comparator.Valid() {
				add(fmt.Sprintf("trigger.checks[%d].comparator", i), "trigger.checks[%d].comparator %q is invalid; must be equals, not_equals, greater_than, less_than, or contains", i, c.Comparator)
			}
		}
	}

	return issues
}

// validateCapabilities checks that at least one capability is present (tool or
// feedback), tool references use valid dot notation, there are no duplicates,
// and approval/timeout and feedback fields are well-formed.
func validateCapabilities(c model.CapabilitiesConfig) []Issue {
	var issues []Issue

	add := func(field, format string, args ...any) {
		issues = append(issues, Issue{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if len(c.Tools) == 0 && !c.Feedback.Enabled {
		add("capabilities", "at least one capability is required (tool or feedback)")
	}

	seen := make(map[string]bool)

	for i, t := range c.Tools {
		if t.Tool == "" {
			add(fmt.Sprintf("capabilities.tools[%d].tool", i), "capabilities.tools[%d].tool is required", i)
			continue
		}
		if !isValidToolRef(t.Tool) {
			add(fmt.Sprintf("capabilities.tools[%d].tool", i), "capabilities.tools[%d].tool %q must use dot notation (server_name.tool_name)", i, t.Tool)
		}
		if seen[t.Tool] {
			add(fmt.Sprintf("capabilities.tools[%d].tool", i), "capabilities.tools[%d].tool %q is a duplicate", i, t.Tool)
		}
		seen[t.Tool] = true

		if !t.Approval.Valid() {
			add(fmt.Sprintf("capabilities.tools[%d].approval", i), "capabilities.tools[%d].approval %q is invalid; must be none or required", i, t.Approval)
		}

		if t.Approval == model.ApprovalModeRequired {
			if t.Timeout != "" {
				if _, err := time.ParseDuration(t.Timeout); err != nil {
					add(fmt.Sprintf("capabilities.tools[%d].timeout", i), "capabilities.tools[%d].timeout %q is not a valid duration: %v", i, t.Timeout, err)
				}
			}
			if !t.OnTimeout.Valid() {
				add(fmt.Sprintf("capabilities.tools[%d].on_timeout", i), "capabilities.tools[%d].on_timeout %q is invalid; must be reject", i, t.OnTimeout)
			}
		}
	}

	issues = append(issues, validateFeedback(c.Feedback)...)

	return issues
}

// validateFeedback checks the feedback config block for valid duration and
// on_timeout values. Fields are only validated when feedback is enabled; the
// parser already clears timeout/on_timeout when disabled so there is nothing
// to validate in the disabled case.
func validateFeedback(f model.FeedbackConfig) []Issue {
	if !f.Enabled {
		return nil
	}
	var issues []Issue
	add := func(field, format string, args ...any) {
		issues = append(issues, Issue{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if f.Timeout != "" {
		if _, err := time.ParseDuration(f.Timeout); err != nil {
			add("capabilities.feedback.timeout", "capabilities.feedback.timeout %q is not a valid duration", f.Timeout)
		}
	}
	if f.OnTimeout != "" && !f.OnTimeout.Valid() {
		add("capabilities.feedback.on_timeout", "capabilities.feedback.on_timeout %q is invalid; must be fail", f.OnTimeout)
	}
	return issues
}

// validateAgent checks agent config and cross-validates against capabilities.
// Specifically: replace concurrency is not valid if any tool has
// approval: required (the in-flight run cannot be safely cancelled mid-approval).
// Required-field validation for model.provider and model.name is done here;
// model-option range validation (e.g. valid temperature, max_tokens bounds)
// is done at the service layer via OptionsValidator.
func validateAgent(a model.AgentConfig, c model.CapabilitiesConfig) []Issue {
	var issues []Issue

	add := func(field, format string, args ...any) {
		issues = append(issues, Issue{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if a.Task == "" {
		add("agent.task", "agent.task is required")
	}

	if a.ModelConfig.Provider == "" {
		add("model.provider", "model.provider is required (set a default in Admin → Models or specify model.provider in policy YAML)")
	}
	if a.ModelConfig.Name == "" {
		add("model.name", "model.name is required (set a default in Admin → Models or specify model.name in policy YAML)")
	}

	// "claude-code" was a subprocess runner removed in issue #611. Policies that
	// still declare this provider must fail with an actionable error rather than
	// silently misbehaving at run time.
	if a.ModelConfig.Provider == "claude-code" {
		add("model.provider", `model.provider "claude-code" is no longer supported; remove the policy or switch to an llm provider (anthropic, google, openai, openaicompat)`)
	}

	if a.Limits.MaxTokensPerRun < 0 {
		add("agent.limits.max_tokens_per_run", "agent.limits.max_tokens_per_run must be zero (unlimited) or positive")
	}
	if a.Limits.MaxToolCallsPerRun < 0 {
		add("agent.limits.max_tool_calls_per_run", "agent.limits.max_tool_calls_per_run must be zero (unlimited) or positive")
	}

	if !a.Concurrency.Valid() {
		add("agent.concurrency", "agent.concurrency %q is invalid; must be skip, queue, parallel, or replace", a.Concurrency)
	}

	if a.QueueDepth < 0 {
		add("agent.queue_depth", "agent.queue_depth must not be negative")
	}

	// Note: cross-validating queue_depth against concurrency mode is impractical
	// because the parser defaults queue_depth to 10 when unset (Go zero value),
	// so we cannot distinguish "user explicitly set queue_depth" from "default."

	// Cross-validation: replace concurrency is incompatible with approval-required tools.
	if a.Concurrency == model.ConcurrencyReplace {
		for _, t := range c.Tools {
			if t.Approval == model.ApprovalModeRequired {
				add("agent.concurrency", "agent.concurrency \"replace\" is not valid when any tool has approval: required")
				break
			}
		}
	}

	return issues
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
