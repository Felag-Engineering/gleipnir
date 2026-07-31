package llm

// NO t.Parallel() anywhere in this file — several tests swap the
// package-level translateForFeatures seam, and CLAUDE.md forbids parallel
// tests that mutate shared package state.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// stubWire is a minimal ProviderWire double with a settable SchemaFeatures
// declaration and a captured request, used to test ProviderAdapter's
// prepareRequest choreography in isolation from any real provider.
type stubWire struct {
	features SchemaFeatureSet

	gotReq MessageRequest
	calls  int // number of times Call/Stream reached the wire
}

func (s *stubWire) Call(_ context.Context, req MessageRequest) (*MessageResponse, error) {
	s.calls++
	s.gotReq = req
	return &MessageResponse{}, nil
}

func (s *stubWire) Stream(_ context.Context, req MessageRequest) (<-chan MessageChunk, error) {
	s.calls++
	s.gotReq = req
	ch := make(chan MessageChunk)
	close(ch)
	return ch, nil
}

func (s *stubWire) ListModels(_ context.Context) ([]ModelInfo, error)   { return nil, nil }
func (s *stubWire) ValidateModelName(_ context.Context, _ string) error { return nil }
func (s *stubWire) ValidateOptions(_ map[string]any) error              { return nil }
func (s *stubWire) InvalidateModelCache()                               {}
func (s *stubWire) ClassifyError(_ error) string                        { return "connection" }
func (s *stubWire) ProviderName() string                                { return "stub" }
func (s *stubWire) SchemaFeatures() SchemaFeatureSet                    { return s.features }

// adapterEntryPoints table-drives the two call shapes prepareRequest guards:
// CreateMessage (named err return, plain assignment) and StreamMessage (no
// named err return, short variable declaration) — the plan-review correction
// that flagged the `:=` vs `=` distinction.
type adapterEntryPoint struct {
	name string
	call func(a *ProviderAdapter, ctx context.Context, req MessageRequest) error
}

var adapterEntryPoints = []adapterEntryPoint{
	{"CreateMessage", func(a *ProviderAdapter, ctx context.Context, req MessageRequest) error {
		_, err := a.CreateMessage(ctx, req)
		return err
	}},
	{"StreamMessage", func(a *ProviderAdapter, ctx context.Context, req MessageRequest) error {
		_, err := a.StreamMessage(ctx, req)
		return err
	}},
}

func TestAdapter_FullSupport_NoCopyNoTranslate(t *testing.T) {
	origTranslate := translateForFeatures
	var calls int
	translateForFeatures = func(canonical json.RawMessage, f SchemaFeatureSet) (json.RawMessage, bool, error) {
		calls++
		return origTranslate(canonical, f)
	}
	t.Cleanup(func() { translateForFeatures = origTranslate })

	tools := []ToolDefinition{
		{Name: "a", Description: "tool a", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "b", Description: "tool b", InputSchema: json.RawMessage(`{"type":"string"}`)},
	}

	for _, ep := range adapterEntryPoints {
		t.Run(ep.name, func(t *testing.T) {
			calls = 0
			w := &stubWire{features: FullSchemaSupport()}
			a := NewAdapter(w)
			req := MessageRequest{Model: "m", Tools: tools}

			if err := ep.call(a, context.Background(), req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got := w.gotReq
			if len(got.Tools) != len(tools) {
				t.Fatalf("wire saw %d tools, want %d", len(got.Tools), len(tools))
			}
			if &got.Tools[0] != &tools[0] {
				t.Error("wire did not receive the caller's exact Tools backing array")
			}
			for i := range tools {
				if &got.Tools[i].InputSchema[0] != &tools[i].InputSchema[0] {
					t.Errorf("tool %d InputSchema bytes were re-allocated on the full-support fast path", i)
				}
			}
			if calls != 0 {
				t.Errorf("translateForFeatures called %d times, want 0 on the full-support fast path", calls)
			}
		})
	}
}

func TestAdapter_Lossy_CopiesToolSliceAndLeavesCallerUntouched(t *testing.T) {
	tool0Schema := json.RawMessage(`{"type":"object","title":"schema0"}`)
	tool1Schema := json.RawMessage(`{"type":"object","title":"schema1"}`)
	simplifiedTool1Schema := json.RawMessage(`{"type":"object","title":"schema1-simplified"}`)

	origTranslate := translateForFeatures
	translateForFeatures = func(canonical json.RawMessage, _ SchemaFeatureSet) (json.RawMessage, bool, error) {
		if bytes.Equal(canonical, tool1Schema) {
			return simplifiedTool1Schema, true, nil
		}
		return canonical, false, nil
	}
	t.Cleanup(func() { translateForFeatures = origTranslate })

	for _, ep := range adapterEntryPoints {
		t.Run(ep.name, func(t *testing.T) {
			callerTools := []ToolDefinition{
				{Name: "tool0", Description: "desc0", InputSchema: tool0Schema},
				{Name: "tool1", Description: "desc1", InputSchema: tool1Schema},
			}
			w := &stubWire{features: SchemaFeatureSet{}} // restricted: not full
			a := NewAdapter(w)
			req := MessageRequest{Model: "m", Tools: callerTools}

			if err := ep.call(a, context.Background(), req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// (a) the caller's original slice is byte-for-byte unchanged.
			if &callerTools[1].InputSchema[0] != &tool1Schema[0] {
				t.Error("caller's tool[1].InputSchema was overwritten in place — mutate-in-place bug")
			}
			if !bytes.Equal(callerTools[1].InputSchema, tool1Schema) {
				t.Errorf("caller's tool[1].InputSchema = %s, want unchanged %s", callerTools[1].InputSchema, tool1Schema)
			}

			// (c) the wire's slice is a different backing array from the caller's.
			got := w.gotReq
			if &got.Tools[0] == &callerTools[0] {
				t.Error("wire received the caller's original Tools backing array, want a copy")
			}

			// (b) the wire saw the rewritten schema for tool[1] and the original
			// bytes for tool[0].
			if !bytes.Equal(got.Tools[0].InputSchema, tool0Schema) {
				t.Errorf("wire's tool[0].InputSchema = %s, want unchanged %s", got.Tools[0].InputSchema, tool0Schema)
			}
			if !bytes.Equal(got.Tools[1].InputSchema, simplifiedTool1Schema) {
				t.Errorf("wire's tool[1].InputSchema = %s, want simplified %s", got.Tools[1].InputSchema, simplifiedTool1Schema)
			}

			// (d) both tools' Name/Description survived the copy.
			if got.Tools[0].Name != "tool0" || got.Tools[0].Description != "desc0" {
				t.Errorf("tool[0] Name/Description not preserved: %+v", got.Tools[0])
			}
			if got.Tools[1].Name != "tool1" || got.Tools[1].Description != "desc1" {
				t.Errorf("tool[1] Name/Description not preserved: %+v", got.Tools[1])
			}
		})
	}
}

func TestAdapter_TranslationError_ShortCircuits(t *testing.T) {
	sentinelErr := errors.New("boom")
	origTranslate := translateForFeatures
	translateForFeatures = func(_ json.RawMessage, _ SchemaFeatureSet) (json.RawMessage, bool, error) {
		return nil, false, sentinelErr
	}
	t.Cleanup(func() { translateForFeatures = origTranslate })

	tools := []ToolDefinition{{Name: "explode", InputSchema: json.RawMessage(`{"type":"object"}`)}}

	for _, ep := range adapterEntryPoints {
		t.Run(ep.name, func(t *testing.T) {
			w := &stubWire{features: SchemaFeatureSet{}}
			a := NewAdapter(w)
			req := MessageRequest{Model: "m", Tools: tools}

			err := ep.call(a, context.Background(), req)
			if err == nil {
				t.Fatal("err = nil, want an error wrapping the translation failure")
			}
			if !errors.Is(err, sentinelErr) {
				t.Errorf("err = %v, want it to wrap %v", err, sentinelErr)
			}
			if !strings.Contains(err.Error(), "explode") {
				t.Errorf("err = %v, want it to name the offending tool %q", err, "explode")
			}
			if w.calls != 0 {
				t.Errorf("wire was called %d times despite the translation failure, want 0", w.calls)
			}
			// Metric non-emission (gleipnir_llm_request_duration_seconds /
			// gleipnir_llm_errors_total) follows structurally from returning here
			// before CreateMessage's metrics-defer is installed — see adapter.go.
			// Asserted by code inspection, not a registry scrape.
		})
	}
}

// TestAdapter_LossyEmptySchema_Rejected exercises the defensive guard against
// a buggy translator: a lossy result with an empty schema must be rejected
// as an error rather than silently handed to the wire, which would present a
// schema-less tool to the model.
func TestAdapter_LossyEmptySchema_Rejected(t *testing.T) {
	origTranslate := translateForFeatures
	translateForFeatures = func(_ json.RawMessage, _ SchemaFeatureSet) (json.RawMessage, bool, error) {
		return json.RawMessage{}, true, nil
	}
	t.Cleanup(func() { translateForFeatures = origTranslate })

	tools := []ToolDefinition{{Name: "explode", InputSchema: json.RawMessage(`{"type":"object"}`)}}

	for _, ep := range adapterEntryPoints {
		t.Run(ep.name, func(t *testing.T) {
			w := &stubWire{features: SchemaFeatureSet{}}
			a := NewAdapter(w)
			req := MessageRequest{Model: "m", Tools: tools}

			err := ep.call(a, context.Background(), req)
			if err == nil {
				t.Fatal("err = nil, want an error rejecting the empty simplified schema")
			}
			if !strings.Contains(err.Error(), "explode") {
				t.Errorf("err = %v, want it to name the offending tool %q", err, "explode")
			}
			if w.calls != 0 {
				t.Errorf("wire was called %d times despite the empty simplified schema, want 0", w.calls)
			}
		})
	}
}

func TestAdapter_NoTools_SkipsTranslation(t *testing.T) {
	origTranslate := translateForFeatures
	var calls int
	translateForFeatures = func(canonical json.RawMessage, f SchemaFeatureSet) (json.RawMessage, bool, error) {
		calls++
		return origTranslate(canonical, f)
	}
	t.Cleanup(func() { translateForFeatures = origTranslate })

	for _, ep := range adapterEntryPoints {
		t.Run(ep.name, func(t *testing.T) {
			calls = 0
			w := &stubWire{features: SchemaFeatureSet{}} // restricted, but no tools to translate
			a := NewAdapter(w)
			req := MessageRequest{Model: "m"}

			if err := ep.call(a, context.Background(), req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if calls != 0 {
				t.Errorf("translateForFeatures called %d times, want 0 when Tools is empty", calls)
			}
		})
	}
}

func TestFakeWire_SchemaFeatures_Full(t *testing.T) {
	if !(&FakeWire{}).SchemaFeatures().IsFull() {
		t.Error("FakeWire.SchemaFeatures() is not full support")
	}
}
