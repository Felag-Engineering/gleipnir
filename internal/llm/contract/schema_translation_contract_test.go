// NO t.Parallel() anywhere in this file: captureLogs swaps the process-global
// slog.Default() for the duration of each (sub-)test, and CLAUDE.md forbids
// parallel tests that mutate shared package state.
package contract_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
	openaisdk_option "github.com/openai/openai-go/option"
	"google.golang.org/genai"

	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/llm/anthropic"
	"github.com/felag-engineering/gleipnir/internal/llm/openai"
	"github.com/felag-engineering/gleipnir/internal/llm/openaicompat"
)

// captureLogs swaps slog.Default() for a Debug-level JSON handler for the
// duration of the test. ProviderAdapter.prepareRequest emits "tool schemas
// simplified for provider" through logctx.Logger(ctx), which falls back to
// slog.Default() — that line is emitted ONLY for tools whose translation came
// back lossy == true, so its presence is the observable form of an otherwise
// unexported flag: TranslateForFeatures's lossy return value never leaves
// internal/llm, and this package deliberately cannot import that seam (see
// the EXTERNAL TEST PACKAGE note below).
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

// sawSchemaSimplified reports whether prepareRequest logged a lossy
// translation into buf. When the line is present it also asserts that it
// names this suite's single tool ("my_server.do_thing", adapter.go logs
// t.Name — the pre-sanitization MCP name, not the wire-sanitized one) so a
// mismatch surfaces as a test failure rather than a silently-wrong true.
func sawSchemaSimplified(t *testing.T, buf *bytes.Buffer) bool {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for dec.More() {
		var line map[string]any
		if err := dec.Decode(&line); err != nil {
			t.Fatalf("decode log line: %v", err)
		}
		if line["msg"] != "tool schemas simplified for provider" {
			continue
		}
		tools, _ := line["tools"].([]any)
		for _, name := range tools {
			if name == "my_server.do_thing" {
				return true
			}
		}
		t.Fatalf(`"tool schemas simplified for provider" log line named %v, want ["my_server.do_thing"]`, tools)
	}
	return false
}

// assertSameJSON compares two JSON documents order-insensitively by decoding
// both to `any`. Object member order is not meaningful (RFC 8259) and each
// wire re-marshals through its own SDK types, so byte comparison would assert
// SDK field order, not schema content.
func assertSameJSON(t *testing.T, got, want json.RawMessage, what string) {
	t.Helper()
	var gotVal, wantVal any
	if err := json.Unmarshal(got, &gotVal); err != nil {
		t.Fatalf("unmarshal got %s: %v\n%s", what, err, got)
	}
	if err := json.Unmarshal(want, &wantVal); err != nil {
		t.Fatalf("unmarshal want %s: %v\n%s", what, err, want)
	}
	if !reflect.DeepEqual(gotVal, wantVal) {
		t.Errorf("%s mismatch:\n got:  %s\n want: %s", what, got, want)
	}
}

// --- Full-support wire probes ---
//
// anthropic, openai, and openaicompat all declare llm.FullSchemaSupport() —
// grouping here is keyed on that declaration, not on provider identity
// (ADR-026: the shared translation pass is feature-keyed, never
// provider-keyed). If a wire's SchemaFeatures() declaration ever becomes
// restricted, it must move out of this table and into a Google-shaped
// sub-test of its own.

// fullSupportProbe drives one full-support wire through a captured HTTP
// request and extracts the schema it actually presented for this suite's
// single tool, so TestContract_SchemaTranslation can assert it against the
// canonical fixture. Each wire's envelope nests the tool schema differently.
type fullSupportProbe struct {
	name       string
	respBody   string
	makeClient func(t *testing.T, srv *httptest.Server) llm.LLMClient
	toolSchema func(t *testing.T, body string) json.RawMessage
	verbatim   bool // wire forwards InputSchema as raw bytes (openaicompat only)
}

func anthropicPresentedSchema(t *testing.T, body string) json.RawMessage {
	t.Helper()
	var req struct {
		Tools []struct {
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal anthropic request body: %v\n%s", err, body)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("want 1 tool in anthropic request, got %d\n%s", len(req.Tools), body)
	}
	return req.Tools[0].InputSchema
}

func openaiPresentedSchema(t *testing.T, body string) json.RawMessage {
	t.Helper()
	var req struct {
		Tools []struct {
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal openai request body: %v\n%s", err, body)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("want 1 tool in openai request, got %d\n%s", len(req.Tools), body)
	}
	return req.Tools[0].Parameters
}

func openaicompatPresentedSchema(t *testing.T, body string) json.RawMessage {
	t.Helper()
	var req struct {
		Tools []struct {
			Function struct {
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal openaicompat request body: %v\n%s", err, body)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("want 1 tool in openaicompat request, got %d\n%s", len(req.Tools), body)
	}
	return req.Tools[0].Function.Parameters
}

var fullSupportProbes = []fullSupportProbe{
	{
		name:     "anthropic",
		respBody: anthropicTextBody(5, 3),
		makeClient: func(t *testing.T, srv *httptest.Server) llm.LLMClient {
			return anthropic.NewClient("test-key", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
		},
		toolSchema: anthropicPresentedSchema,
	},
	{
		name:     "openai",
		respBody: openaiTextBody(5, 3),
		makeClient: func(t *testing.T, srv *httptest.Server) llm.LLMClient {
			return openai.NewClient("test-key", openaisdk_option.WithHTTPClient(srv.Client()), openaisdk_option.WithBaseURL(srv.URL))
		},
		toolSchema: openaiPresentedSchema,
	},
	{
		name:     "openaicompat",
		respBody: compatTextBody(5, 3),
		makeClient: func(t *testing.T, srv *httptest.Server) llm.LLMClient {
			return openaicompat.NewClient(srv.URL, "test-key", openaicompat.WithHTTPClient(srv.Client()))
		},
		toolSchema: openaicompatPresentedSchema,
		verbatim:   true,
	},
}

// --- Case table ---

// schemaCase is one canonical InputSchema driven through all four wires.
// wantGoogleSchema and wantGoogleLossy were measured against the real Google
// wire (see the plan for #746) — they are ground truth, not aspirational.
type schemaCase struct {
	name             string
	schema           string // canonical InputSchema: compact, single-line, no '<' '>' '&' (RawMessage HTML-escaping and the openaicompat byte-identity check both depend on this)
	wantGoogleSchema string // JSON rendering of the *genai.Schema Google must build
	wantGoogleLossy  bool   // did the shared pass rewrite anything (observed via the debug log)
}

var schemaCases = []schemaCase{
	{
		// A schema with no branching keyword at all: the baseline "nothing to
		// translate" case for every wire, including Google.
		name:             "plain_object",
		schema:           `{"type":"object","properties":{"q":{"type":"string","description":"query"}},"required":["q"]}`,
		wantGoogleSchema: `{"type":"OBJECT","properties":{"q":{"type":"STRING","description":"query"}},"required":["q"]}`,
		wantGoogleLossy:  false,
	},
	{
		// The DoD's Google enum-flattening assertion. The root MUST declare
		// kind/x/y itself (not just inside the oneOf variants): mergeProperties's
		// ADR-017 scope intersection (schema_simplify.go) only accepts a
		// variant's contribution for a name the root already declared, so a
		// root missing one of these names would silently drop it from the
		// presented schema instead of merging it in.
		//
		// The discriminator "kind" becomes {"type":"STRING","enum":["a","b"],
		// "format":"enum"} (google/schema.go sets Format:"enum" whenever it
		// emits an Enum). "required" is ["kind"] only — the intersection of the
		// variants' required lists (mergeRequired), not the union: "x" and "y"
		// are each required in only one branch, so neither can be required
		// unconditionally.
		name: "oneof_discriminated",
		schema: `{"type":"object","properties":{"kind":{"type":"string"},"x":{"type":"string"},"y":{"type":"integer"}},` +
			`"oneOf":[{"type":"object","properties":{"kind":{"const":"a"},"x":{"type":"string"}},"required":["kind","x"]},` +
			`{"type":"object","properties":{"kind":{"const":"b"},"y":{"type":"integer"}},"required":["kind","y"]}]}`,
		wantGoogleSchema: `{"type":"OBJECT","description":"Exactly one of the following variants applies, selected by \"kind\"; do not mix properties from different variants.\n- \"a\": properties: kind, x\n- \"b\": properties: kind, y","properties":{"kind":{"type":"STRING","enum":["a","b"],"format":"enum"},"x":{"type":"STRING"},"y":{"type":"INTEGER"}},"required":["kind"]}`,
		wantGoogleLossy:  true,
	},
	{
		// The DoD's Google prose-union assertion: no discriminator property is
		// found (neither variant declares a "const"), so the shared pass falls
		// back to prose with "Variant N" bullet labels instead of tag values.
		// Three things are asserted here by construction:
		//   1. the pre-existing root description survives ABOVE the prose
		//      block, separated by a blank line (appendDescription);
		//   2. the bullets read "Variant 1" / "Variant 2", not tag values,
		//      because variantLabel falls back to positional labels when no
		//      discriminator is found;
		//   3. "required" disappears entirely — "a" and "b" are each required
		//      in only one variant, so the intersection is empty and the root
		//      declared no required fields of its own. This is the documented
		//      permissive-union widening (ADR-059 "lossy presentation, exact
		//      enforcement": the model is told nothing is mandatory, but
		//      dispatch-time enforcement still checks the schema of record),
		//      not a bug.
		name: "oneof_permissive_union",
		schema: `{"type":"object","description":"do a thing","properties":{"a":{"type":"string"},"b":{"type":"integer"}},` +
			`"oneOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},` +
			`{"type":"object","properties":{"b":{"type":"integer"}},"required":["b"]}]}`,
		wantGoogleSchema: `{"type":"OBJECT","description":"do a thing\n\nExactly one of the following variants applies; do not mix properties from different variants.\n- Variant 1: properties: a\n- Variant 2: properties: b","properties":{"a":{"type":"STRING"},"b":{"type":"INTEGER"}}}`,
		wantGoogleLossy:  true,
	},
	{
		// SUSPECTED PRE-EXISTING PROBLEM (test-only issue — not fixed here):
		// allOf is never flattened anywhere in the pipeline. Google declares
		// AllOf: true (google/client.go), so the shared TranslateForFeatures
		// pass leaves this schema untouched and reports lossy == false — but
		// translateJSONSchemaToGenaiSchema (google/schema.go) builds the
		// genai.Schema from only type/description/enum/required/properties/
		// items, so the entire allOf block silently vanishes, taking
		// required:["a"] and minimum:1 with it. lossy == false here means only
		// "no MODELLED feature was rewritten" (see the "READ THIS BEFORE
		// TRUSTING THE lossy FLAG" paragraph in schema_features.go) — it does
		// NOT mean the presented schema matches the canonical one. This test
		// codifies today's behavior; it is not the desired end state.
		name:             "allof_merged_canonical",
		schema:           `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}},"allOf":[{"required":["a"]},{"properties":{"b":{"minimum":1}}}]}`,
		wantGoogleSchema: `{"type":"OBJECT","properties":{"a":{"type":"STRING"},"b":{"type":"INTEGER"}}}`,
		wantGoogleLossy:  false,
	},
	{
		// SUSPECTED PRE-EXISTING PROBLEM, sharpest form: an object whose ENTIRE
		// constraint set (its one property and its one required field) lives
		// inside allOf and nowhere else. Google presents this to the model as
		// a fully unconstrained object — {"type":"OBJECT"}, no properties, no
		// required — while still reporting lossy == false, because (as in the
		// case above) allOf is a modelled-safe feature that the shared pass
		// declines to touch, and the wire-local drop isn't tracked by the
		// lossy flag at all. Safe on the three full-support wires: anthropic's
		// buildToolInputSchema and the others simply omit the absent
		// properties/required keys, so the passthrough DeepEqual still holds.
		name:             "allof_only_root",
		schema:           `{"type":"object","allOf":[{"properties":{"a":{"type":"string"}},"required":["a"]}]}`,
		wantGoogleSchema: `{"type":"OBJECT"}`,
		wantGoogleLossy:  false,
	},
	{
		// Proves the properties → items recursion positions (schema_simplify.go)
		// are walked at every depth, not just the root, and that the
		// full-support wires still carry the deep "oneOf" verbatim regardless
		// of nesting depth.
		name: "deeply_nested",
		schema: `{"type":"object","properties":{"outer":{"type":"object","properties":{"list":{"type":"array",` +
			`"items":{"type":"object","properties":{"leaf":{"type":"string"}},` +
			`"oneOf":[{"type":"object","properties":{"leaf":{"const":"p"}}},{"type":"object","properties":{"leaf":{"const":"q"}}}]}}}}}}`,
		wantGoogleSchema: `{"type":"OBJECT","properties":{"outer":{"type":"OBJECT","properties":{"list":{"type":"ARRAY","items":{"type":"OBJECT","description":"Exactly one of the following variants applies, selected by \"leaf\"; do not mix properties from different variants.\n- \"p\": properties: leaf\n- \"q\": properties: leaf","properties":{"leaf":{"type":"STRING","enum":["p","q"],"format":"enum"}}}}}}}}`,
		wantGoogleLossy:  true,
	},
}

// googleTextResponse returns a minimal successful Gemini response (plain
// text, FinishReasonStop, usage 1/1). Schema translation happens before the
// response is inspected, so its content is irrelevant here — only
// gen.lastConfig matters. Kept local to this file; the four inline response
// literals already in wire_contract_test.go are left as-is.
func googleTextResponse() *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content:      &genai.Content{Parts: []*genai.Part{{Text: "hello"}}},
			FinishReason: genai.FinishReasonStop,
		}},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 1, CandidatesTokenCount: 1},
	}
}

// TestContract_SchemaTranslation drives every canonical schemaCase through
// all four real ProviderWires and asserts each wire's outgoing request body
// matches its declared SchemaFeatureSet: verbatim passthrough for the three
// full-support wires, and the exact enum-flattened / prose-union rendering
// for Google, the only restricted wire.
//
// This suite does NOT re-test what internal/llm/schema_simplify_test.go
// already covers (the simplifier's own unit tests) — its job is the
// cross-wire end-to-end rendering, not the simplifier's internals. It also
// must not be read as an enforcement test (ADR-059 / spec §10): dispatch-time
// enforcement runs against the stored canonical schema and never consults
// what a wire presents to the model.
func TestContract_SchemaTranslation(t *testing.T) {
	for _, tc := range schemaCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range fullSupportProbes {
				t.Run(p.name, func(t *testing.T) {
					logs := captureLogs(t)
					var lastReq string
					srv := captureServer(t, p.respBody, &lastReq)
					client := p.makeClient(t, srv)

					if _, err := client.CreateMessage(context.Background(), requestWithTool(json.RawMessage(tc.schema))); err != nil {
						t.Fatalf("CreateMessage: %v", err)
					}

					// A full-support declaration means the canonical schema
					// reaches the wire unrewritten.
					assertSameJSON(t, p.toolSchema(t, lastReq), json.RawMessage(tc.schema), "presented tool schema")

					// openaicompat forwards InputSchema as json.RawMessage
					// (openaicompat/translate.go), so byte-identity is directly
					// assertable there — a stronger check than decoded equality.
					if p.verbatim && !strings.Contains(lastReq, tc.schema) {
						t.Errorf("outbound request must forward the canonical schema byte-identical; body=%s", lastReq)
					}

					// prepareRequest's IsFull() fast path must never reach
					// TranslateForFeatures at all for a full-support wire.
					if sawSchemaSimplified(t, logs) {
						t.Errorf("full-support wire must not log a lossy schema simplification")
					}
				})
			}

			// Google is asserted separately because its observation channel
			// genuinely differs in kind: anthropic/openai/openaicompat go over
			// HTTP (captureServer), Google goes through the in-process
			// fakeGenerator because google.NewClientWithGenerator is the wire's
			// only test seam. This mirrors how the existing suite already
			// splits Google out (TestContract_ContinuityState_Google, and
			// Google's exclusion from TestContract_ToolSchemaPassthrough_FullSupport).
			t.Run("google", func(t *testing.T) {
				logs := captureLogs(t)
				gen := &fakeGenerator{response: googleTextResponse()}
				client := newGoogleClient(gen)

				if _, err := client.CreateMessage(context.Background(), requestWithTool(json.RawMessage(tc.schema))); err != nil {
					t.Fatalf("CreateMessage: %v", err)
				}
				if gen.lastConfig == nil || len(gen.lastConfig.Tools) != 1 {
					t.Fatalf("expected exactly 1 genai.Tool, got config=%+v", gen.lastConfig)
				}
				decls := gen.lastConfig.Tools[0].FunctionDeclarations
				if len(decls) != 1 {
					t.Fatalf("expected exactly 1 FunctionDeclaration, got %d", len(decls))
				}

				got, err := json.Marshal(decls[0].Parameters)
				if err != nil {
					t.Fatalf("marshal genai.Schema: %v", err)
				}
				assertSameJSON(t, got, json.RawMessage(tc.wantGoogleSchema), "genai.Schema presented to Gemini")

				if got := sawSchemaSimplified(t, logs); got != tc.wantGoogleLossy {
					t.Errorf("lossy translation logged = %v, want %v", got, tc.wantGoogleLossy)
				}
			})
		})
	}
}
