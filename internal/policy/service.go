package policy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// ToolLookup checks whether a tool reference exists in the MCP registry.
// Implementations query the mcp_tools + mcp_servers tables.
type ToolLookup interface {
	// ToolExists returns true if server_name.tool_name is registered.
	ToolExists(ctx context.Context, serverName, toolName string) (bool, error)
}

// ModelValidator validates that a model ID is accepted by the Anthropic API.
// Validation is blocking: a failure prevents the policy from being saved.
type ModelValidator interface {
	ValidateModel(ctx context.Context, modelID string) error
}

// SaveResult holds the outcome of saving a policy, including any non-blocking
// warnings (e.g. unresolved tool references).
type SaveResult struct {
	Policy   model.Policy
	Warnings []string
}

// Service orchestrates policy parse → validate → store operations.
type Service struct {
	store          *db.Store
	lookup         ToolLookup     // nil if MCP registry is unavailable
	modelValidator ModelValidator // nil skips API-level model validation
}

// NewService returns a policy Service. lookup may be nil if MCP registry
// checking is not yet available — tool reference warnings will be skipped.
// modelValidator may be nil — API-level model validation will be skipped,
// though the local allowlist check in Validate still runs.
func NewService(store *db.Store, lookup ToolLookup, modelValidator ModelValidator) *Service {
	return &Service{store: store, lookup: lookup, modelValidator: modelValidator}
}

// Create parses and validates the YAML, checks tool references against the
// MCP registry (non-blocking warnings), and stores the policy.
func (s *Service) Create(ctx context.Context, rawYAML string) (*SaveResult, error) {
	parsed, err := Parse(rawYAML)
	if err != nil {
		return nil, err
	}
	if err := Validate(parsed); err != nil {
		return nil, err
	}
	if err := s.validateModel(ctx, parsed.Agent.Model); err != nil {
		return nil, err
	}

	warnings := s.checkToolRefs(ctx, parsed)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	row, err := s.store.CreatePolicy(ctx, db.CreatePolicyParams{
		ID:          model.NewULID(),
		Name:        parsed.Name,
		TriggerType: string(parsed.Trigger.Type),
		Yaml:        rawYAML,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		return nil, fmt.Errorf("create policy: %w", err)
	}

	return &SaveResult{Policy: toModelPolicy(row), Warnings: warnings}, nil
}

// Update re-parses and re-validates the YAML, checks tool references, and
// replaces the stored YAML for the given policy ID.
func (s *Service) Update(ctx context.Context, policyID string, rawYAML string) (*SaveResult, error) {
	parsed, err := Parse(rawYAML)
	if err != nil {
		return nil, err
	}
	if err := Validate(parsed); err != nil {
		return nil, err
	}
	if err := s.validateModel(ctx, parsed.Agent.Model); err != nil {
		return nil, err
	}

	warnings := s.checkToolRefs(ctx, parsed)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	row, err := s.store.UpdatePolicy(ctx, db.UpdatePolicyParams{
		ID:          policyID,
		Name:        parsed.Name,
		TriggerType: string(parsed.Trigger.Type),
		Yaml:        rawYAML,
		UpdatedAt:   now,
	})
	if err != nil {
		return nil, fmt.Errorf("update policy: %w", err)
	}

	return &SaveResult{Policy: toModelPolicy(row), Warnings: warnings}, nil
}

// toModelPolicy maps a sqlc-generated db.Policy to the domain model.Policy.
func toModelPolicy(row db.Policy) model.Policy {
	createdAt, _ := time.Parse(time.RFC3339Nano, row.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, row.UpdatedAt)
	return model.Policy{
		ID:          row.ID,
		Name:        row.Name,
		TriggerType: model.TriggerType(row.TriggerType),
		YAML:        row.Yaml,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
}

// validateModel calls the modelValidator if one is configured. Returns nil
// when modelValidator is nil (skips API-level check). Errors are blocking —
// unlike tool reference checks, a bad model ID must prevent save.
func (s *Service) validateModel(ctx context.Context, modelID string) error {
	if s.modelValidator == nil {
		return nil
	}
	return s.modelValidator.ValidateModel(ctx, modelID)
}

// checkToolRefs issues non-blocking warnings for tool references that don't
// match the MCP registry. Returns nil if lookup is unavailable.
func (s *Service) checkToolRefs(ctx context.Context, p *model.ParsedPolicy) []string {
	if s.lookup == nil {
		return nil
	}

	var warnings []string

	checkRef := func(ref string) {
		if ctx.Err() != nil {
			return
		}
		parts := strings.SplitN(ref, ".", 2)
		if len(parts) != 2 {
			return // validator already catches bad dot-notation
		}
		exists, err := s.lookup.ToolExists(ctx, parts[0], parts[1])
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not check tool %q: %v", ref, err))
			return
		}
		if !exists {
			warnings = append(warnings, fmt.Sprintf("tool %q not found in MCP registry", ref))
		}
	}

	for _, sensor := range p.Capabilities.Sensors {
		checkRef(sensor.Tool)
	}
	for _, actuator := range p.Capabilities.Actuators {
		checkRef(actuator.Tool)
	}

	if ctx.Err() != nil {
		warnings = append(warnings, fmt.Sprintf("tool reference check aborted: %v", ctx.Err()))
	}

	return warnings
}
