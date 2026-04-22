package policy

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// Sentinel errors returned by webhook secret methods, exported so handler code
// can map them to HTTP status codes without string matching.
var (
	// ErrNoSuchPolicy is returned when a policy ID is not found in the DB.
	ErrNoSuchPolicy = errors.New("policy not found")

	// ErrNotWebhookTrigger is returned when a secret operation is called on a
	// policy whose trigger type is not webhook.
	ErrNotWebhookTrigger = errors.New("policy is not a webhook trigger")

	// ErrEncryptionUnavailable is returned when the encryption key is not
	// configured, preventing secret operations.
	ErrEncryptionUnavailable = errors.New("encryption key not configured")

	// ErrNoSecret is returned by GetWebhookSecret when no secret has been rotated yet.
	ErrNoSecret = errors.New("no webhook secret stored")
)

// SecretCipher is satisfied by an adapter that wraps admin.Encrypt and
// admin.Decrypt. Defined as an interface so the policy package does not import
// internal/admin directly.
type SecretCipher interface {
	EncryptWebhookSecret(plaintext string) (ciphertext string, err error)
	DecryptWebhookSecret(ciphertext string) (plaintext string, err error)
}

// ToolLookup checks whether a tool reference exists in the MCP registry.
// Implementations query the mcp_tools + mcp_servers tables.
type ToolLookup interface {
	// ToolExists returns true if server_name.tool_name is registered.
	ToolExists(ctx context.Context, serverName, toolName string) (bool, error)
}

// ModelValidator validates that a model name is recognized by the named provider.
// Validation is non-blocking: a failure is reported as a warning in SaveResult
// rather than preventing the policy from being saved. An unrecognized model
// name might be a newly released model not yet in the provider's allowlist.
// *llm.ProviderRegistry satisfies this interface via its ValidateModelName method.
type ModelValidator interface {
	ValidateModelName(ctx context.Context, provider, modelName string) error
}

// OptionsValidator looks up a provider and validates its options.
// Defined as an interface so the policy package does not import internal/llm directly.
// *llm.ProviderRegistry satisfies this interface via its ValidateProviderOptions method.
type OptionsValidator interface {
	ValidateProviderOptions(provider string, options map[string]any) error
}

// SettingsReader reads the system-wide default provider and model from the
// admin settings store. The admin.Handler satisfies this interface via its
// GetSystemDefault method.
type SettingsReader interface {
	GetSystemDefault(ctx context.Context) (provider, model string, err error)
}

// SaveResult holds the outcome of saving a policy, including any non-blocking
// warnings (e.g. unresolved tool references).
type SaveResult struct {
	Policy   model.Policy
	Warnings []string
}

// Service orchestrates policy parse → validate → store operations.
type Service struct {
	store            *db.Store
	lookup           ToolLookup       // nil if MCP registry is unavailable
	modelValidator   ModelValidator   // nil skips model name validation
	optionsValidator OptionsValidator // nil skips provider options validation
	settings         SettingsReader   // nil falls back to compiled defaults
	encrypter        SecretCipher     // nil means encryption not configured
}

// NewService returns a policy Service. lookup may be nil if MCP registry
// checking is not yet available — tool reference warnings will be skipped.
// modelValidator may be nil — model name validation will be skipped.
// optionsValidator may be nil — provider options validation will be skipped.
// settings may be nil — model defaults will be unset, causing policies that
// omit the model block to fail validation with a clear error.
func NewService(store *db.Store, lookup ToolLookup, modelValidator ModelValidator, optionsValidator OptionsValidator, settings SettingsReader) *Service {
	return &Service{
		store:            store,
		lookup:           lookup,
		modelValidator:   modelValidator,
		optionsValidator: optionsValidator,
		settings:         settings,
	}
}

// Create parses and validates the YAML, checks tool references against the
// MCP registry (non-blocking warnings), and stores the policy.
func (s *Service) Create(ctx context.Context, rawYAML string) (*SaveResult, error) {
	if err := CheckLegacyWebhookSecret(rawYAML); err != nil {
		return nil, &ValidationError{Errors: []Issue{{Field: "trigger.webhook_secret", Message: err.Error()}}}
	}
	provider, modelName := s.resolveDefaults(ctx)
	parsed, err := Parse(rawYAML, provider, modelName)
	if err != nil {
		return nil, err
	}
	if err := Validate(parsed); err != nil {
		return nil, err
	}
	if err := s.validateProviderOptions(parsed); err != nil {
		return nil, &ValidationError{Errors: []Issue{{Field: "model", Message: err.Error()}}}
	}
	// Collect warnings before any blocking checks so model validation failures
	// can be appended without returning an error.
	var warnings []string

	if err := s.validateModel(ctx, parsed); err != nil {
		warnings = append(warnings, err.Error())
	}

	// For new scheduled policies, reject if all fire_at times are in the past —
	// the scheduler would have nothing to do immediately. Existing policies
	// that have been updated to have past times are handled by Update().
	if parsed.Trigger.Type == model.TriggerTypeScheduled {
		now := time.Now().UTC()
		hasFuture := false
		for _, t := range parsed.Trigger.FireAt {
			if t.After(now) {
				hasFuture = true
				break
			}
		}
		if !hasFuture {
			return nil, &ValidationError{Errors: []Issue{{
				Field:   "trigger.fire_at",
				Message: "all timestamps are in the past; scheduled policy must have at least one future fire time",
			}}}
		}
	}

	warnings = append(warnings, s.checkToolRefs(ctx, parsed)...)

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

	p, err := toModelPolicy(row)
	if err != nil {
		return nil, fmt.Errorf("create policy: %w", err)
	}
	return &SaveResult{Policy: p, Warnings: warnings}, nil
}

// Update re-parses and re-validates the YAML, checks tool references, and
// replaces the stored YAML for the given policy ID.
func (s *Service) Update(ctx context.Context, policyID string, rawYAML string) (*SaveResult, error) {
	if err := CheckLegacyWebhookSecret(rawYAML); err != nil {
		return nil, &ValidationError{Errors: []Issue{{Field: "trigger.webhook_secret", Message: err.Error()}}}
	}
	provider, modelName := s.resolveDefaults(ctx)
	parsed, err := Parse(rawYAML, provider, modelName)
	if err != nil {
		return nil, err
	}
	if err := Validate(parsed); err != nil {
		return nil, err
	}
	if err := s.validateProviderOptions(parsed); err != nil {
		return nil, &ValidationError{Errors: []Issue{{Field: "model", Message: err.Error()}}}
	}

	var warnings []string

	if err := s.validateModel(ctx, parsed); err != nil {
		warnings = append(warnings, err.Error())
	}

	warnings = append(warnings, s.checkToolRefs(ctx, parsed)...)

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

	// For scheduled policies: if the update includes future fire times, clear
	// paused_at so the scheduler picks it up again. If all times are past,
	// ensure it stays paused.
	if parsed.Trigger.Type == model.TriggerTypeScheduled {
		nowTime := time.Now().UTC()
		hasFuture := false
		for _, t := range parsed.Trigger.FireAt {
			if t.After(nowTime) {
				hasFuture = true
				break
			}
		}
		if hasFuture {
			if err := s.store.ClearPolicyPausedAt(ctx, policyID); err != nil {
				return nil, fmt.Errorf("clear policy paused_at: %w", err)
			}
		} else {
			pausedAt := nowTime.Format(time.RFC3339Nano)
			if err := s.store.SetPolicyPausedAt(ctx, db.SetPolicyPausedAtParams{
				PausedAt: &pausedAt,
				ID:       policyID,
			}); err != nil {
				return nil, fmt.Errorf("set policy paused_at: %w", err)
			}
		}
	}

	result, err := toModelPolicy(row)
	if err != nil {
		return nil, fmt.Errorf("update policy: %w", err)
	}
	return &SaveResult{Policy: result, Warnings: warnings}, nil
}

// SetPolicyPausedAt marks a scheduled policy as paused after exhausting all fire times.
func (s *Service) SetPolicyPausedAt(ctx context.Context, policyID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.store.SetPolicyPausedAt(ctx, db.SetPolicyPausedAtParams{
		PausedAt: &now,
		ID:       policyID,
	}); err != nil {
		return fmt.Errorf("set policy paused_at: %w", err)
	}
	return nil
}

// ClearPolicyPausedAt removes the paused state from a scheduled policy.
func (s *Service) ClearPolicyPausedAt(ctx context.Context, policyID string) error {
	if err := s.store.ClearPolicyPausedAt(ctx, policyID); err != nil {
		return fmt.Errorf("clear policy paused_at: %w", err)
	}
	return nil
}

// generateWebhookSecret returns a cryptographically random 64-character
// lowercase hex string (32 bytes of entropy).
func generateWebhookSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// WithWebhookSecretEncrypter sets the encrypter used for rotate/reveal operations.
// When nil (the default), those operations return ErrEncryptionUnavailable.
func (s *Service) WithWebhookSecretEncrypter(e SecretCipher) {
	s.encrypter = e
}

// RotateWebhookSecret generates a fresh 64-hex secret, encrypts it, and persists
// it to policies.webhook_secret_encrypted. The plaintext is returned to the caller
// for immediate display and MUST NOT be logged or stored anywhere else.
func (s *Service) RotateWebhookSecret(ctx context.Context, policyID string) (string, error) {
	row, err := s.store.GetPolicy(ctx, policyID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNoSuchPolicy
		}
		return "", fmt.Errorf("get policy: %w", err)
	}
	if row.TriggerType != string(model.TriggerTypeWebhook) {
		return "", ErrNotWebhookTrigger
	}
	if s.encrypter == nil {
		return "", ErrEncryptionUnavailable
	}

	plaintext, err := generateWebhookSecret()
	if err != nil {
		return "", fmt.Errorf("generate webhook secret: %w", err)
	}

	ciphertext, err := s.encrypter.EncryptWebhookSecret(plaintext)
	if err != nil {
		return "", fmt.Errorf("encrypt webhook secret: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.store.SetPolicyWebhookSecret(ctx, db.SetPolicyWebhookSecretParams{
		Ciphertext: &ciphertext,
		UpdatedAt:  now,
		ID:         policyID,
	}); err != nil {
		return "", fmt.Errorf("store webhook secret: %w", err)
	}

	return plaintext, nil
}

// GetWebhookSecret decrypts and returns the stored webhook secret for policyID.
// Returns ErrNoSecret when no secret has been stored yet.
// Returns ErrEncryptionUnavailable when the encryption key is not configured.
func (s *Service) GetWebhookSecret(ctx context.Context, policyID string) (string, error) {
	if s.encrypter == nil {
		return "", ErrEncryptionUnavailable
	}

	ciphertext, err := s.store.GetPolicyWebhookSecret(ctx, policyID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNoSuchPolicy
		}
		return "", fmt.Errorf("get webhook secret: %w", err)
	}
	if ciphertext == nil {
		return "", ErrNoSecret
	}

	plaintext, err := decryptSecret(s.encrypter, *ciphertext)
	if err != nil {
		return "", fmt.Errorf("decrypt webhook secret: %w", err)
	}
	return plaintext, nil
}

// decryptSecret decrypts a ciphertext using the SecretCipher. Both encrypt
// and decrypt are declared on the same interface so no type assertion is needed.
func decryptSecret(e SecretCipher, ciphertext string) (string, error) {
	return e.DecryptWebhookSecret(ciphertext)
}

// toModelPolicy maps a sqlc-generated db.Policy to the domain model.Policy.
func toModelPolicy(row db.Policy) (model.Policy, error) {
	createdAt, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
	if err != nil {
		return model.Policy{}, fmt.Errorf("parse created_at %q: %w", row.CreatedAt, err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, row.UpdatedAt)
	if err != nil {
		return model.Policy{}, fmt.Errorf("parse updated_at %q: %w", row.UpdatedAt, err)
	}
	p := model.Policy{
		ID:          row.ID,
		Name:        row.Name,
		TriggerType: model.TriggerType(row.TriggerType),
		YAML:        row.Yaml,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
	if row.PausedAt != nil {
		t, err := time.Parse(time.RFC3339Nano, *row.PausedAt)
		if err != nil {
			return model.Policy{}, fmt.Errorf("parse paused_at %q: %w", *row.PausedAt, err)
		}
		p.PausedAt = &t
	}
	return p, nil
}

// validateProviderOptions calls the optionsValidator if one is configured.
// Returns nil when optionsValidator is nil (skips check). The caller treats
// any returned error as a blocking validation error — invalid options will
// fail at run time, so catching them at save time is the whole point.
func (s *Service) validateProviderOptions(parsed *model.ParsedPolicy) error {
	if s.optionsValidator == nil {
		return nil
	}
	return s.optionsValidator.ValidateProviderOptions(
		parsed.Agent.ModelConfig.Provider,
		parsed.Agent.ModelConfig.Options,
	)
}

// validateModel calls the modelValidator if one is configured. Returns nil
// when modelValidator is nil (skips check). The caller treats any returned
// error as a non-blocking warning — see ModelValidator doc comment.
func (s *Service) validateModel(ctx context.Context, parsed *model.ParsedPolicy) error {
	if s.modelValidator == nil {
		return nil
	}
	return s.modelValidator.ValidateModelName(
		ctx,
		parsed.Agent.ModelConfig.Provider,
		parsed.Agent.ModelConfig.Name,
	)
}

// resolveDefaults returns the default provider and model from DB-stored system
// settings. When settings are not configured or the lookup fails, ("", "") is
// returned so that policy.Parse leaves ModelConfig blank — policy.Validate
// will then surface a clear "model.provider is required" error rather than
// silently using a hard-coded fallback.
func (s *Service) resolveDefaults(ctx context.Context) (string, string) {
	if s.settings == nil {
		return "", ""
	}
	provider, modelName, err := s.settings.GetSystemDefault(ctx)
	if err != nil || provider == "" {
		return "", ""
	}
	return provider, modelName
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

	for _, t := range p.Capabilities.Tools {
		checkRef(t.Tool)
	}

	if ctx.Err() != nil {
		warnings = append(warnings, fmt.Sprintf("tool reference check aborted: %v", ctx.Err()))
	}

	return warnings
}
