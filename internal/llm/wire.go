package llm

import "context"

// ProviderWire is the interface each provider implements behind the shared
// ProviderAdapter. It owns all wire-specific work (param building, SDK calls,
// response translation, streaming) and returns neutral llm.* types.
//
// The streaming state machine stays inside each provider's Stream
// implementation — see ADR-026 and the agreed scope for issue #506.
//
// ClassifyError already exists per-provider as an unexported function;
// the interface promotes it to a method so the adapter's metrics-defer and
// the FakeWire can call it without knowing the concrete type.
//
// ProviderName returns the Prometheus label string. openaicompat returns a
// caller-configured name (defaulting to "openaicompat"), so the adapter reads
// it at call time rather than capturing it once at construction.
type ProviderWire interface {
	// Call executes a synchronous request and returns the neutral response.
	// It owns all provider-specific work: param building, SDK call, translation.
	Call(ctx context.Context, req MessageRequest) (*MessageResponse, error)

	// Stream executes a streaming request and returns the neutral chunk channel.
	// The channel is closed when the stream ends or an error occurs.
	// The per-provider accumulate-and-emit state machine lives inside this method.
	Stream(ctx context.Context, req MessageRequest) (<-chan MessageChunk, error)

	// ListModels returns the models available from this provider.
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// ValidateModelName returns nil if modelName is recognized, or a descriptive
	// error. May make a network call; results are cached by the implementation.
	ValidateModelName(ctx context.Context, modelName string) error

	// ValidateOptions validates provider-specific options from the policy YAML.
	ValidateOptions(options map[string]any) error

	// InvalidateModelCache clears any cached model list.
	InvalidateModelCache()

	// ClassifyError maps a provider SDK error to the fixed error_type enum
	// used for Prometheus metrics. The per-provider logic (errors.As ladder)
	// stays inside each wire implementation; the adapter calls this from its
	// metrics-defer so the provider string is the only thing that differs.
	ClassifyError(err error) string

	// ProviderName returns the Prometheus label for this provider.
	// openaicompat returns a caller-configured name, defaulting to "openaicompat".
	ProviderName() string
}
