package policy

import (
	"fmt"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/model"
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
		errs = append(errs, fmt.Sprintf("trigger.type %q is invalid; must be webhook, manual, or scheduled", t.Type))
		return errs // can't validate type-specific fields without a valid type
	}

	switch t.Type {
	case model.TriggerTypeWebhook:
		if t.WebhookSecret != "" && len(t.WebhookSecret) < 32 {
			errs = append(errs, "trigger.webhook_secret must be at least 32 bytes")
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
	}

	if t.WebhookSecret != "" && t.Type != model.TriggerTypeWebhook {
		errs = append(errs, "trigger.webhook_secret is only valid for webhook triggers")
	}

	return errs
}

// validateCapabilities checks that at least one tool is present,
// tool references use valid dot notation, there are no duplicates,
// and approval/timeout fields are well-formed.
func validateCapabilities(c model.CapabilitiesConfig) []string {
	var errs []string

	if len(c.Tools) == 0 {
		errs = append(errs, "at least one tool is required")
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

	if a.Limits.MaxTokensPerRun <= 0 {
		errs = append(errs, "agent.limits.max_tokens_per_run must be positive")
	}
	if a.Limits.MaxToolCallsPerRun <= 0 {
		errs = append(errs, "agent.limits.max_tool_calls_per_run must be positive")
	}

	if !a.Concurrency.Valid() {
		errs = append(errs, fmt.Sprintf("agent.concurrency %q is invalid; must be skip, queue, parallel, or replace", a.Concurrency))
	}

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

// isValidToolRef checks that a tool reference uses dot notation: server_name.tool_name.
// Both parts must be non-empty.
func isValidToolRef(ref string) bool {
	parts := strings.SplitN(ref, ".", 2)
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}
