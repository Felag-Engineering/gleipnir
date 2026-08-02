package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
)

// ProviderAdapter satisfies LLMClient by wrapping a ProviderWire. It owns the
// choreography that is genuinely identical across all providers:
//   - the CreateMessage metrics-defer (timer, error classification, token recording)
//   - delegation of StreamMessage, ValidateOptions, ValidateModelName, ListModels,
//     and InvalidateModelCache to the wire
//   - simplifying each tool's InputSchema for the wire's declared
//     SchemaFeatures before Call/Stream (prepareRequest)
//
// The streaming state machine, error-classification errors.As ladders, and all
// wire-format translation stay inside each provider's ProviderWire implementation.
//
// Concrete provider Client types embed *ProviderAdapter and promote
// CreateMessage, StreamMessage, and SchemaFeatures from it — SchemaFeatures is
// promoted rather than forwarded because a zero-value Client is never
// registered, so it is not a reachable path (see its own doc comment below
// for the full argument). The four model/option methods (ValidateOptions,
// ValidateModelName, ListModels, InvalidateModelCache) stay as thin explicit
// forwarders on each concrete Client so zero-value clients remain safe — see
// BLOCKING-1 in the plan for issue #506.
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

// translateForFeatures is a package-level seam so adapter tests can drive the
// lossy branch before any wire declares a restricted feature set (#739 is the
// first). Production always uses TranslateForFeatures.
var translateForFeatures = TranslateForFeatures

// CreateMessage owns the metrics-defer choreography lifted verbatim from all
// four provider CreateMessage implementations. The metrics-defer block is the
// only genuinely identical code across providers; everything else stays in wire.Call.
func (a *ProviderAdapter) CreateMessage(ctx context.Context, req MessageRequest) (resp *MessageResponse, err error) {
	// prepareRequest runs before the metrics-defer is installed below: a local
	// schema-translation failure is neither an "LLM API round-trip duration"
	// nor an "LLM API failure", so it must not land in
	// gleipnir_llm_request_duration_seconds / gleipnir_llm_errors_total, and it
	// must not be run through wire.ClassifyError, which would mislabel it
	// "connection". Assigning to the named err return is safe here because the
	// defer has not been installed yet.
	req, err = a.prepareRequest(ctx, req)
	if err != nil {
		return nil, err
	}

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
	// StreamMessage has no named error return, so this is a short variable
	// declaration (`:=`), not a plain assignment — req already exists in scope
	// and err is new.
	req, err := a.prepareRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	return a.wire.Stream(ctx, req)
}

// prepareRequest simplifies every tool's InputSchema for the wire's declared
// SchemaFeatures. req is copied, never mutated in place: MessageRequest is
// passed by value, but req.Tools is a slice header that shares the CALLER's
// backing array — internal/execution/agent builds one tool slice per run and
// reuses it across every API-loop iteration, so writing into req.Tools[i]
// would permanently corrupt the run's canonical schemas after the first call.
// A fresh []ToolDefinition is allocated on first change and only that copy is
// written into; the original backing array is never touched. On the
// full-support fast path (and the no-op restricted path where nothing needs
// rewriting) no copy is allocated at all.
func (a *ProviderAdapter) prepareRequest(ctx context.Context, req MessageRequest) (MessageRequest, error) {
	if len(req.Tools) == 0 {
		return req, nil
	}
	features := a.wire.SchemaFeatures()
	if features.IsFull() {
		return req, nil
	}

	var translated []ToolDefinition
	var simplified []string
	for i, t := range req.Tools {
		out, lossy, err := translateForFeatures(t.InputSchema, features)
		if err != nil {
			return MessageRequest{}, fmt.Errorf("llm: tool %q: simplifying input schema for %s: %w", t.Name, a.wire.ProviderName(), err)
		}
		if !lossy {
			continue
		}
		// Defensive guard against a buggy translator: TranslateForFeatures's
		// contract promises a non-empty schema whenever err is nil, but a
		// translator bug returning lossy=true with an empty out would
		// otherwise silently present a schema-less tool to the model instead
		// of failing loudly.
		if len(out) == 0 {
			return MessageRequest{}, fmt.Errorf("llm: tool %q: simplified input schema for %s is empty", t.Name, a.wire.ProviderName())
		}
		if translated == nil {
			translated = make([]ToolDefinition, len(req.Tools))
			copy(translated, req.Tools)
		}
		translated[i].InputSchema = out
		simplified = append(simplified, t.Name)
	}
	if translated == nil {
		return req, nil
	}
	req.Tools = translated // req is a by-value parameter, so this rebinds only the local slice header

	logctx.Logger(ctx).DebugContext(ctx, "tool schemas simplified for provider",
		"provider", a.wire.ProviderName(), "model", req.Model, "tools", simplified)
	return req, nil
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

// SchemaFeatures reports the wire's declared JSON Schema support without
// issuing any request — a read-side accessor for callers (e.g. the Tools
// page's "simplified for this provider" computation) that need to know what
// a wire CAN represent. prepareRequest remains the only production consumer
// on the write path.
//
// Unlike the four model/option methods above, this method is deliberately
// NOT shadowed by a thin forwarder on each concrete Client
// (anthropic.Client, openai.Client, google.GeminiClient, openaicompat.Client
// — all embed *ProviderAdapter): it is promoted onto all four as-is. A
// zero-value Client is never registered in production, so
// SchemaFeatures on a zero-value Client is not a reachable path, and four
// forwarder copies would only invite drift.
func (a *ProviderAdapter) SchemaFeatures() SchemaFeatureSet {
	return a.wire.SchemaFeatures()
}
