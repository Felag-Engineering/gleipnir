package llm

import (
	"context"
	"time"
)

// ProviderAdapter satisfies LLMClient by wrapping a ProviderWire. It owns the
// choreography that is genuinely identical across all providers:
//   - the CreateMessage metrics-defer (timer, error classification, token recording)
//   - delegation of StreamMessage, ValidateOptions, ValidateModelName, ListModels,
//     and InvalidateModelCache to the wire
//
// The streaming state machine, error-classification errors.As ladders, and all
// wire-format translation stay inside each provider's ProviderWire implementation.
//
// Concrete provider Client types embed *ProviderAdapter and promote only
// CreateMessage and StreamMessage from it. The four model/option methods
// (ValidateOptions, ValidateModelName, ListModels, InvalidateModelCache) stay as
// thin explicit forwarders on each concrete Client so zero-value clients remain
// safe — see BLOCKING-1 in the plan for issue #506.
//
// The adapter itself implements the full LLMClient interface so FakeWire can be
// wrapped directly (without a concrete provider type in front).
type ProviderAdapter struct {
	wire ProviderWire
}

// NewAdapter wraps w in a ProviderAdapter that satisfies LLMClient.
func NewAdapter(w ProviderWire) *ProviderAdapter {
	return &ProviderAdapter{wire: w}
}

// Compile-time check: ProviderAdapter satisfies the full LLMClient interface.
// This matters for the FakeWire path where no concrete provider type is present.
var _ LLMClient = (*ProviderAdapter)(nil)

// CreateMessage owns the metrics-defer choreography lifted verbatim from all
// four provider CreateMessage implementations. The metrics-defer block is the
// only genuinely identical code across providers; everything else stays in wire.Call.
func (a *ProviderAdapter) CreateMessage(ctx context.Context, req MessageRequest) (resp *MessageResponse, err error) {
	start := time.Now()
	provider := a.wire.ProviderName()
	defer func() {
		ObserveRequestDuration(provider, req.Model, time.Since(start))
		if err != nil {
			RecordError(provider, a.wire.ClassifyError(err))
			return
		}
		if resp != nil {
			RecordTokens(provider, req.Model, resp.Usage)
		}
	}()
	resp, err = a.wire.Call(ctx, req)
	return
}

// StreamMessage delegates directly to the wire. No metrics-defer is added here
// because none of the four original providers wrap StreamMessage in a defer.
func (a *ProviderAdapter) StreamMessage(ctx context.Context, req MessageRequest) (<-chan MessageChunk, error) {
	return a.wire.Stream(ctx, req)
}

// ValidateOptions delegates to the wire. Concrete provider Clients shadow this
// with a thin forwarder that calls the same logic without going through the
// embedded adapter — this ensures zero-value clients remain safe.
func (a *ProviderAdapter) ValidateOptions(options map[string]any) error {
	return a.wire.ValidateOptions(options)
}

// ValidateModelName delegates to the wire.
func (a *ProviderAdapter) ValidateModelName(ctx context.Context, modelName string) error {
	return a.wire.ValidateModelName(ctx, modelName)
}

// ListModels delegates to the wire.
func (a *ProviderAdapter) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return a.wire.ListModels(ctx)
}

// InvalidateModelCache delegates to the wire.
func (a *ProviderAdapter) InvalidateModelCache() {
	a.wire.InvalidateModelCache()
}
