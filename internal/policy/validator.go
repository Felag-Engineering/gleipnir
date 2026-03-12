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
// approval-required actuators). Returns nil if valid.
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
		errs = append(errs, fmt.Sprintf("trigger.type %q is invalid; must be webhook, cron, poll, manual, or scheduled", t.Type))
		return errs // can't validate type-specific fields without a valid type
	}

	switch t.Type {
	case model.TriggerTypeWebhook:
		// No additional fields required.

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

	case model.TriggerTypeCron:
		if t.Schedule == "" {
			errs = append(errs, "trigger.schedule is required for cron triggers")
		}

	case model.TriggerTypePoll:
		if t.Poll == nil {
			errs = append(errs, "trigger poll config is required for poll triggers")
			return errs
		}
		if t.Poll.Interval == "" {
			errs = append(errs, "trigger.interval is required for poll triggers")
		} else if _, err := time.ParseDuration(t.Poll.Interval); err != nil {
			errs = append(errs, fmt.Sprintf("trigger.interval %q is not a valid duration: %v", t.Poll.Interval, err))
		}
		if t.Poll.Request.URL == "" {
			errs = append(errs, "trigger.request.url is required for poll triggers")
		}
		method := strings.ToUpper(t.Poll.Request.Method)
		if method != "GET" && method != "POST" {
			errs = append(errs, fmt.Sprintf("trigger.request.method %q is invalid; must be GET or POST", t.Poll.Request.Method))
		}
		if t.Poll.Filter == "" {
			errs = append(errs, "trigger.filter is required for poll triggers")
		}
	}

	return errs
}

// validateCapabilities checks that at least one sensor or actuator is present,
// tool references use valid dot notation, there are no duplicates across roles,
// and actuator approval/timeout fields are well-formed.
func validateCapabilities(c model.CapabilitiesConfig) []string {
	var errs []string

	if len(c.Sensors) == 0 && len(c.Actuators) == 0 {
		errs = append(errs, "at least one sensor or actuator is required")
	}

	seen := make(map[string]bool)

	for i, s := range c.Sensors {
		if s.Tool == "" {
			errs = append(errs, fmt.Sprintf("capabilities.sensors[%d].tool is required", i))
			continue
		}
		if !isValidToolRef(s.Tool) {
			errs = append(errs, fmt.Sprintf("capabilities.sensors[%d].tool %q must use dot notation (server_name.tool_name)", i, s.Tool))
		}
		if seen[s.Tool] {
			errs = append(errs, fmt.Sprintf("capabilities.sensors[%d].tool %q is a duplicate", i, s.Tool))
		}
		seen[s.Tool] = true
	}

	for i, a := range c.Actuators {
		if a.Tool == "" {
			errs = append(errs, fmt.Sprintf("capabilities.actuators[%d].tool is required", i))
			continue
		}
		if !isValidToolRef(a.Tool) {
			errs = append(errs, fmt.Sprintf("capabilities.actuators[%d].tool %q must use dot notation (server_name.tool_name)", i, a.Tool))
		}
		if seen[a.Tool] {
			errs = append(errs, fmt.Sprintf("capabilities.actuators[%d].tool %q is a duplicate", i, a.Tool))
		}
		seen[a.Tool] = true

		if !a.Approval.Valid() {
			errs = append(errs, fmt.Sprintf("capabilities.actuators[%d].approval %q is invalid; must be none or required", i, a.Approval))
		}

		if a.Approval == model.ApprovalModeRequired {
			if a.Timeout != "" {
				if _, err := time.ParseDuration(a.Timeout); err != nil {
					errs = append(errs, fmt.Sprintf("capabilities.actuators[%d].timeout %q is not a valid duration: %v", i, a.Timeout, err))
				}
			}
			if !a.OnTimeout.Valid() {
				errs = append(errs, fmt.Sprintf("capabilities.actuators[%d].on_timeout %q is invalid; must be reject or approve", i, a.OnTimeout))
			}
		}
	}

	return errs
}

// knownModels is the set of Claude model IDs supported by Gleipnir.
// Validation uses a local allowlist so callers get fast feedback without
// needing an API key. Update this list when new models are supported.
var knownModels = map[string]bool{
	"claude-opus-4-6":           true,
	"claude-sonnet-4-6":         true,
	"claude-haiku-4-5-20251001": true,
}

// validateAgent checks agent config and cross-validates against capabilities.
// Specifically: replace concurrency is not valid if any actuator has
// approval: required (the in-flight run cannot be safely cancelled mid-approval).
func validateAgent(a model.AgentConfig, c model.CapabilitiesConfig) []string {
	var errs []string

	if a.Model != "" && !knownModels[a.Model] {
		errs = append(errs, fmt.Sprintf("agent.model %q is not a supported model; must be one of: claude-opus-4-6, claude-sonnet-4-6, claude-haiku-4-5-20251001", a.Model))
	}

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

	// Cross-validation: replace concurrency is incompatible with approval-required actuators.
	if a.Concurrency == model.ConcurrencyReplace {
		for _, act := range c.Actuators {
			if act.Approval == model.ApprovalModeRequired {
				errs = append(errs, "agent.concurrency \"replace\" is not valid when any actuator has approval: required")
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
