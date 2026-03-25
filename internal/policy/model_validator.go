package policy

import (
	"context"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// AnthropicModelValidator confirms a model ID is valid by calling the
// Anthropic Models API. Failures are reported as non-blocking warnings by the
// Service — the local allowlist in Validate() already rejects unknown model IDs
// before this runs, so auth/network failures here are safe to surface as warnings.
type AnthropicModelValidator struct {
	client *anthropic.Client
}

// NewAnthropicModelValidator returns a validator backed by the given Anthropic client.
func NewAnthropicModelValidator(client *anthropic.Client) *AnthropicModelValidator {
	return &AnthropicModelValidator{client: client}
}

// ValidateModel returns nil if modelID is accepted by the Anthropic API.
// Auth failures (401/403) produce a user-friendly message explaining the key
// is missing or invalid. Other errors are wrapped with context.
func (v *AnthropicModelValidator) ValidateModel(ctx context.Context, modelID string) error {
	_, err := v.client.Models.Get(ctx, modelID, anthropic.ModelGetParams{})
	if err == nil {
		return nil
	}

	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) && (apiErr.StatusCode == 401 || apiErr.StatusCode == 403) {
		return fmt.Errorf("Anthropic API key is not configured or invalid (HTTP %d); model %q was not verified against the API", apiErr.StatusCode, modelID)
	}

	return fmt.Errorf("could not verify model %q with Anthropic API: %w", modelID, err)
}
