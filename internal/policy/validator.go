package policy

import (
	"errors"
	"fmt"

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

	if err := validateTrigger(p.Trigger); err != nil {
		errs = append(errs, err.Error())
	}

	if err := validateCapabilities(p.Capabilities); err != nil {
		errs = append(errs, err.Error())
	}

	if err := validateAgent(p.Agent, p.Capabilities); err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		return &ValidationError{Errors: errs}
	}
	return nil
}

func validateTrigger(t model.TriggerConfig) error {
	// TODO: validate trigger type and type-specific required fields
	panic("not implemented")
}

func validateCapabilities(c model.CapabilitiesConfig) error {
	// TODO: require at least one sensor or actuator; validate tool dot-notation;
	// validate approval/timeout/on_timeout fields on actuators
	panic("not implemented")
}

// validateAgent checks agent config and cross-validates against capabilities.
// Specifically: replace concurrency is not valid if any actuator has
// approval: required (the in-flight run cannot be safely cancelled mid-approval).
func validateAgent(a model.AgentConfig, c model.CapabilitiesConfig) error {
	if a.Task == "" {
		return errors.New("agent.task is required")
	}
	// TODO: validate limits, concurrency, and replace/approval incompatibility
	panic("not implemented")
}
