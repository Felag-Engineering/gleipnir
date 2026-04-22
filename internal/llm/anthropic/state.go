package anthropic

import (
	"encoding/json"
	"fmt"
)

// anthropicThinkingState holds the round-trip state for a single Anthropic
// thinking block. It is serialized into ThinkingBlock.ProviderState and
// deserialized when building subsequent API requests.
type anthropicThinkingState struct {
	Signature    string `json:"signature,omitempty"`
	RedactedData string `json:"redacted_data,omitempty"`
}

// marshalThinkingState encodes s into a JSON RawMessage for storage in
// ThinkingBlock.ProviderState. Returns an error wrapping any json.Marshal failure.
func marshalThinkingState(s anthropicThinkingState) (json.RawMessage, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal thinking state: %w", err)
	}
	return json.RawMessage(b), nil
}

// unmarshalThinkingState decodes b into an anthropicThinkingState. When b is nil
// or empty, it returns a zero struct and nil error (pass-through: no round-trip
// state). Any json.Unmarshal failure is wrapped and returned.
func unmarshalThinkingState(b json.RawMessage) (anthropicThinkingState, error) {
	if len(b) == 0 {
		return anthropicThinkingState{}, nil
	}
	var s anthropicThinkingState
	if err := json.Unmarshal(b, &s); err != nil {
		return anthropicThinkingState{}, fmt.Errorf("anthropic: unmarshal thinking state: %w", err)
	}
	return s, nil
}
