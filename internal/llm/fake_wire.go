package llm

import (
	"context"
	"fmt"
	"sync"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

// FakeWire is a ProviderWire that returns scripted responses in sequence.
// It is the "fifth adapter" described in issue #506: it slots into NewAdapter
// so test code runs through the real metrics-defer choreography in ProviderAdapter
// instead of bypassing it with a hand-rolled mock.
//
// Wrap it with NewAdapter (or use testutil.NewFakeClient) to get an LLMClient.
//
// FakeWire is safe for concurrent use.
type FakeWire struct {
	// Responses is the scripted sequence of responses. Call advances through
	// this slice; an exhausted slice returns an error.
	Responses []*MessageResponse

	// name is the Prometheus label returned by ProviderName.
	name string

	mu       sync.Mutex
	requests []*MessageRequest
	callIdx  int
}

// NewFakeWire constructs a FakeWire that returns responses in the given order.
// The provider name (the Prometheus label) is "fake".
func NewFakeWire(responses ...*MessageResponse) *FakeWire {
	return &FakeWire{
		Responses: responses,
		name:      "fake",
	}
}

// Call returns the next scripted response and records the request.
func (f *FakeWire) Call(_ context.Context, req MessageRequest) (*MessageResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, &req)
	if f.callIdx >= len(f.Responses) {
		return nil, fmt.Errorf("FakeWire: no more responses (called %d times, have %d)", f.callIdx+1, len(f.Responses))
	}
	resp := f.Responses[f.callIdx]
	f.callIdx++
	return resp, nil
}

// Stream delegates to Call and converts the response to a chunk channel using
// StubStreamFromResponse, matching MockLLMClient.StreamMessage behavior exactly.
func (f *FakeWire) Stream(ctx context.Context, req MessageRequest) (<-chan MessageChunk, error) {
	resp, err := f.Call(ctx, req)
	if err != nil {
		return nil, err
	}
	return StubStreamFromResponse(resp), nil
}

// ListModels returns nil (no model listing in the fake wire).
func (f *FakeWire) ListModels(_ context.Context) ([]ModelInfo, error) { return nil, nil }

// ValidateModelName returns nil (no validation in the fake wire).
func (f *FakeWire) ValidateModelName(_ context.Context, _ string) error { return nil }

// ValidateOptions returns nil (no validation in the fake wire).
func (f *FakeWire) ValidateOptions(_ map[string]any) error { return nil }

// InvalidateModelCache is a no-op for the fake wire.
func (f *FakeWire) InvalidateModelCache() {}

// ClassifyError maps errors to the fixed error_type enum, matching
// MockLLMClient behavior: context errors become "timeout"; everything else
// becomes "connection".
func (f *FakeWire) ClassifyError(err error) string {
	if et, ok := ClassifyContextError(err); ok {
		return et
	}
	return metrics.ErrorTypeConnection
}

// ProviderName returns the Prometheus label for this fake provider.
func (f *FakeWire) ProviderName() string {
	if f.name != "" {
		return f.name
	}
	return "fake"
}

// SchemaFeatures declares full JSON Schema support, keeping every
// testutil.NewFakeClient consumer on the adapter's fast path — zero behavior
// change across the agent/trigger test suite.
func (f *FakeWire) SchemaFeatures() SchemaFeatureSet { return FullSchemaSupport() }

// Calls returns the number of Call invocations.
func (f *FakeWire) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callIdx
}

// Requests returns a copy of all captured requests in call order.
func (f *FakeWire) Requests() []*MessageRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*MessageRequest, len(f.requests))
	copy(out, f.requests)
	return out
}
