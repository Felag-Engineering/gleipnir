package openai

import (
	"encoding/json"
	"fmt"
)

// openaiThinkingState holds the round-trip state for a single OpenAI reasoning
// item. It is serialized into ThinkingBlock.ProviderState and deserialized when
// building subsequent Responses API requests.
type openaiThinkingState struct {
	ID               string `json:"id,omitempty"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
}

// marshalThinkingState encodes s into a JSON RawMessage for storage in
// ThinkingBlock.ProviderState. Returns an error wrapping any json.Marshal failure.
func marshalThinkingState(s openaiThinkingState) (json.RawMessage, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal thinking state: %w", err)
	}
	return json.RawMessage(b), nil
}

// unmarshalThinkingState decodes b into an openaiThinkingState. When b is nil
// or empty, it returns a zero struct and nil error (pass-through: no round-trip
// state). Any json.Unmarshal failure is wrapped and returned.
func unmarshalThinkingState(b json.RawMessage) (openaiThinkingState, error) {
	if len(b) == 0 {
		return openaiThinkingState{}, nil
	}
	var s openaiThinkingState
	if err := json.Unmarshal(b, &s); err != nil {
		return openaiThinkingState{}, fmt.Errorf("openai: unmarshal thinking state: %w", err)
	}
	return s, nil
}
