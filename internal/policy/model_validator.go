package policy

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// AnthropicModelValidator confirms a model ID is valid by calling the
// Anthropic Models API. Used as a blocking check at policy save time.
type AnthropicModelValidator struct {
	client *anthropic.Client
}

// NewAnthropicModelValidator returns a validator backed by the given Anthropic client.
func NewAnthropicModelValidator(client *anthropic.Client) *AnthropicModelValidator {
	return &AnthropicModelValidator{client: client}
}

// ValidateModel returns nil if modelID is accepted by the Anthropic API,
// or a wrapped error if not.
func (v *AnthropicModelValidator) ValidateModel(ctx context.Context, modelID string) error {
	_, err := v.client.Models.Get(ctx, modelID, anthropic.ModelGetParams{})
	if err != nil {
		return fmt.Errorf("model %q is not a valid Anthropic model: %w", modelID, err)
	}
	return nil
}
