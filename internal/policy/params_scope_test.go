package policy

import (
	"encoding/json"
	"strings"
	"testing"
)

// validateParamsScope emits warnings, never blocking issues.
//
// These assertions deliberately pin the *semantic claim* each warning makes
// rather than the full prose. Two reasons: the messages are long enough that
// exact-match assertions become a copy-paste ritual nobody re-reads, and the
// claim is the part that must not silently change. Rewording a warning is
// fine; a warning that stops telling the operator what will actually happen
// at runtime is a regression.
//
// The claim for the unnarrowable shapes CHANGED with #769. It used to be
// "every argument key is permitted" — a real security hole, since scoping was
// enforced from the narrowed schema and these shapes narrow to nothing.
// mcp.ValidateCall now enforces the allowlist from the params block itself, so
// the tool IS restricted; what remains is that the agent is shown a schema
// nobody could narrow, so it may spend a run attempting a property that is
// then refused. That wasted run is the consequence an operator must still be
// told about.
const (
	claimShownUnnarrowed = "have the call fail the run"
	claimStillRestricted = "restricted at dispatch"
	claimRuntime         = "applied at runtime"
	claimNarrowsNone     = "narrows nothing"
	claimPartial         = "does not reach properties nested inside"
)

func TestValidateParamsScope(t *testing.T) {
	const toolRef = "github.list_repos"

	tests := []struct {
		name      string
		toolIndex int
		params    map[string]any
		canonical json.RawMessage
		wantCount int
		wantPaths []string // field-path prefixes, in order
		wantClaim string   // must appear in every returned warning
	}{
		{
			name:      "no params + nil canonical -> silent",
			params:    nil,
			canonical: nil,
			wantCount: 0,
		},
		{
			name:      "no params + branching canonical -> silent",
			params:    nil,
			canonical: json.RawMessage(`{"oneOf":[{"properties":{"a":{}}}]}`),
			wantCount: 0,
		},
		{
			name:      "nil canonical -> unverified, still applied at runtime",
			params:    map[string]any{"a": 1},
			canonical: nil,
			wantCount: 1,
			wantPaths: []string{"capabilities.tools[0].params"},
			wantClaim: claimRuntime,
		},
		{
			name:      "empty canonical -> unverified, still applied at runtime",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(""),
			wantCount: 1,
			wantPaths: []string{"capabilities.tools[0].params"},
			wantClaim: claimRuntime,
		},
		{
			name:      "whitespace-only canonical -> unverified, still applied at runtime",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage("   "),
			wantCount: 1,
			wantPaths: []string{"capabilities.tools[0].params"},
			wantClaim: claimRuntime,
		},
		{
			name:      "canonical is a JSON boolean -> not-an-object",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`true`),
			wantCount: 1,
			wantPaths: []string{"capabilities.tools[0].params"},
			wantClaim: "not a JSON object",
		},
		{
			name:      "canonical is malformed JSON -> not-an-object",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"properties":`),
			wantCount: 1,
			wantPaths: []string{"capabilities.tools[0].params"},
			wantClaim: "not a JSON object",
		},
		{
			// Narrowing no-ops for this shape, so the agent sees the whole
			// schema; the params allowlist still bounds dispatch (#769).
			name:      "no top-level properties -> unnarrowable",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"type":"object"}`),
			wantCount: 1,
			wantPaths: []string{"capabilities.tools[0].params"},
			wantClaim: claimShownUnnarrowed,
		},
		{
			name:      "root oneOf without properties -> unnarrowable",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"oneOf":[{"properties":{"a":{}}}]}`),
			wantCount: 1,
			wantPaths: []string{"capabilities.tools[0].params"},
			wantClaim: claimShownUnnarrowed,
		},
		{
			// Root properties ARE narrowed and enforced here; only
			// branch-nested properties escape. Claiming "unrestricted" would
			// be false.
			name:      "root oneOf with properties -> partial, not unrestricted",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"properties":{"a":{}},"oneOf":[{"properties":{"b":{}}}]}`),
			wantCount: 1,
			wantPaths: []string{"capabilities.tools[0].params"},
			wantClaim: claimPartial,
		},
		{
			name:      "root allOf without properties -> unrestricted",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"allOf":[{"properties":{"a":{}}}]}`),
			wantCount: 1,
			wantPaths: []string{"capabilities.tools[0].params"},
			wantClaim: claimShownUnnarrowed,
		},
		{
			name:      "root $ref without properties -> unrestricted",
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"$ref":"#/$defs/X"}`),
			wantCount: 1,
			wantPaths: []string{"capabilities.tools[0].params"},
			wantClaim: claimShownUnnarrowed,
		},
		{
			name:      "all keys are known properties -> silent",
			params:    map[string]any{"a": 1, "b": 2},
			canonical: json.RawMessage(`{"properties":{"a":{},"b":{},"c":{}}}`),
			wantCount: 0,
		},
		{
			name:      "one unknown key -> one per-key warning",
			params:    map[string]any{"a": 1, "zzz": 2},
			canonical: json.RawMessage(`{"properties":{"a":{}}}`),
			wantCount: 1,
			wantPaths: []string{"capabilities.tools[0].params.zzz"},
			wantClaim: claimNarrowsNone,
		},
		{
			// Sorted, so warning order is stable regardless of Go map
			// iteration order.
			name:      "multiple unknown keys -> sorted per-key warnings",
			params:    map[string]any{"zeta": 1, "alpha": 2, "mid": 3},
			canonical: json.RawMessage(`{"properties":{"kept":{}}}`),
			wantCount: 3,
			wantPaths: []string{
				"capabilities.tools[0].params.alpha",
				"capabilities.tools[0].params.mid",
				"capabilities.tools[0].params.zeta",
			},
			wantClaim: claimNarrowsNone,
		},
		{
			name:      "tool index appears in the path",
			toolIndex: 7,
			params:    map[string]any{"a": 1},
			canonical: json.RawMessage(`{"type":"object"}`),
			wantCount: 1,
			wantPaths: []string{"capabilities.tools[7].params"},
			wantClaim: claimShownUnnarrowed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validateParamsScope(tc.toolIndex, toolRef, tc.params, tc.canonical)

			if len(got) != tc.wantCount {
				t.Fatalf("warning count = %d, want %d\ngot: %#v", len(got), tc.wantCount, got)
			}
			for i, w := range got {
				if want := tc.wantPaths[i] + ":"; !strings.HasPrefix(w, want) {
					t.Errorf("warning[%d] should start with %q, got:\n  %s", i, want, w)
				}
				if !strings.Contains(w, tc.wantClaim) {
					t.Errorf("warning[%d] should assert %q, got:\n  %s", i, tc.wantClaim, w)
				}
				if !strings.Contains(w, toolRef) {
					t.Errorf("warning[%d] should name the tool %q, got:\n  %s", i, toolRef, w)
				}
			}
		})
	}
}

// TestValidateParamsScope_UnenforceableCasesSaySo is the security-relevant
// assertion in this file. In these two shapes ADR-017's structural guarantee
// genuinely does not hold at runtime, and the warning is the operator's only
// signal. If someone softens the wording, this fails.
func TestValidateParamsScope_UnenforceableCasesSaySo(t *testing.T) {
	unenforceable := []struct {
		name      string
		canonical json.RawMessage
	}{
		{"no top-level properties", json.RawMessage(`{"type":"object"}`)},
		{"root branch, no properties", json.RawMessage(`{"oneOf":[{"properties":{"a":{}}}]}`)},
	}

	for _, tc := range unenforceable {
		t.Run(tc.name, func(t *testing.T) {
			got := validateParamsScope(0, "srv.tool", map[string]any{"a": 1}, tc.canonical)
			if len(got) != 1 {
				t.Fatalf("want exactly 1 warning, got %d: %#v", len(got), got)
			}
			w := got[0]
			if !strings.Contains(w, claimShownUnnarrowed) {
				t.Errorf("warning must state the run-failing consequence, got:\n  %s", w)
			}
			// The operator must also be told scoping still holds — a warning
			// that only described the downside would read as "unenforced" and
			// push them toward a workaround they no longer need.
			if !strings.Contains(w, claimStillRestricted) {
				t.Errorf("warning must state that dispatch is still restricted, got:\n  %s", w)
			}
		})
	}
}

// Warnings are plain strings with no Issue.Field, so each embeds its own path.
// It must appear exactly once — the duplicated-prefix defect the #745 plan
// review caught would bite here too if a caller added its own prefix.
func TestValidateParamsScope_PathAppearsExactlyOnce(t *testing.T) {
	got := validateParamsScope(3, "srv.tool", map[string]any{"nope": 1},
		json.RawMessage(`{"properties":{"yes":{}}}`))
	if len(got) != 1 {
		t.Fatalf("want 1 warning, got %d: %#v", len(got), got)
	}
	const path = "capabilities.tools[3].params.nope"
	if n := strings.Count(got[0], path); n != 1 {
		t.Errorf("path %q appears %d times, want exactly 1:\n  %s", path, n, got[0])
	}
}

// The tool index belongs in the path and nowhere else, so two policies that
// differ only in tool position produce warnings differing only in that path.
func TestValidateParamsScope_IndexOnlyAffectsPath(t *testing.T) {
	canonical := json.RawMessage(`{"type":"object","properties":{"a":{}}}`)
	params := map[string]any{"foo": 1}

	at0 := validateParamsScope(0, "github.list_repos", params, canonical)
	at2 := validateParamsScope(2, "github.list_repos", params, canonical)
	if len(at0) != 1 || len(at2) != 1 {
		t.Fatalf("expected exactly 1 warning each, got %d and %d", len(at0), len(at2))
	}
	if at0[0] == at2[0] {
		t.Errorf("expected the path to differ per toolIndex, both were %q", at0[0])
	}
	// Strip each path prefix; the remaining message must be identical.
	tail := func(s string) string {
		_, rest, _ := strings.Cut(s, ": ")
		return rest
	}
	if tail(at0[0]) != tail(at2[0]) {
		t.Errorf("message text must not vary with toolIndex:\n  %q\n  %q", tail(at0[0]), tail(at2[0]))
	}
}
