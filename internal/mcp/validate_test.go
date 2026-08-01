package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNewArgValidator_Compile(t *testing.T) {
	tests := []struct {
		name        string
		schema      json.RawMessage
		params      map[string]any
		wantErr     bool
		errContains string
		// check runs additional assertions against a successfully compiled
		// validator. Only used by cases that need to exercise Validate, not
		// just assert compile success/failure.
		check func(t *testing.T, v *ArgValidator)
	}{
		{
			name:   "valid object schema compiles",
			schema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`),
		},
		{
			name:   "empty schema object compiles",
			schema: json.RawMessage(`{}`),
		},
		{
			name:   "bare true schema compiles",
			schema: json.RawMessage(`true`),
		},
		{
			name:    "empty bytes is an error",
			schema:  json.RawMessage(``),
			wantErr: true,
		},
		{
			name:    "nil bytes is an error",
			schema:  nil,
			wantErr: true,
		},
		{
			name:    "non-schema JSON is an error",
			schema:  json.RawMessage(`"nope"`),
			wantErr: true,
		},
		{
			name:    "invalid pattern regex is an error",
			schema:  json.RawMessage(`{"type":"string","pattern":"("}`),
			wantErr: true,
		},
		{
			// SECURITY REGRESSION GATE: without the deny-all loader, this
			// $ref would make the compiler open /etc/passwd and try to parse
			// it as JSON (verified: the failure mode changes to "invalid
			// character 'r' looking for beginning of value" — i.e. it had
			// already read the file). Asserting the error names the denied
			// URL and comes from denyAllLoader's message, not a JSON parse
			// error, proves no file read occurred.
			name:        "external file ref is denied, not read",
			schema:      json.RawMessage(`{"$ref":"file:///etc/passwd"}`),
			wantErr:     true,
			errContains: "external schema reference not permitted: file:///etc/passwd",
		},
		{
			name:   "draft-07 dialect with array-form items compiles",
			schema: json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"array","items":[{"type":"string"}]}`),
		},
		{
			// Documents the fallback trigger for decision (d): a schema with
			// no "$schema" is compiled against the pinned 2020-12 draft,
			// under which array-form "items" (draft-07 tuple typing) is
			// invalid — 2020-12 uses "prefixItems" instead.
			name:    "no $schema with array-form items fails under pinned 2020-12 default",
			schema:  json.RawMessage(`{"type":"array","items":[{"type":"string"}]}`),
			wantErr: true,
		},
		{
			// Anti-deadlock property from decision (b): narrowing to params
			// drops both the excluded property from "properties" and any
			// "required" entry that names it, so the narrowed validator
			// never demands a property the key-presence gate already strips.
			name:   "narrowing to params drops excluded property and its required entry",
			schema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"]}`),
			params: map[string]any{"a": "x"},
			check: func(t *testing.T, v *ArgValidator) {
				t.Helper()
				if err := v.Validate(map[string]any{"a": 123}); err == nil {
					t.Error("wrong-typed 'a' should be rejected; narrowing does not remove type checks on retained properties")
				}
				if err := v.Validate(map[string]any{"a": "x", "b": 123}); err != nil {
					t.Errorf("'b' should be ignored (narrowed out of properties, no additionalProperties keyword): %v", err)
				}
				if err := v.Validate(map[string]any{"a": "x"}); err != nil {
					t.Errorf("required 'b' should have been dropped by narrowing: %v", err)
				}
			},
		},
		{
			// NUMERIC FIDELITY, empty params: NarrowSchema short-circuits to
			// a passthrough (narrow.go), so the compiled document is the
			// stored canonical bytes verbatim, and the schema's "const"
			// keeps arbitrary precision (jsonschema.UnmarshalJSON uses
			// json.Number). An instance equal to the exact 32-digit literal,
			// expressed as json.Number to avoid the test itself losing
			// precision, must validate.
			name:   "numeric fidelity preserved with no params (passthrough)",
			schema: json.RawMessage(`{"type":"object","properties":{"n":{"const":10000000000000000000000000000001}}}`),
			check: func(t *testing.T, v *ArgValidator) {
				t.Helper()
				exact := map[string]any{"n": json.Number("10000000000000000000000000000001")}
				if err := v.Validate(exact); err != nil {
					t.Errorf("exact 32-digit literal should satisfy the passthrough schema's const: %v", err)
				}
			},
		},
		{
			// NUMERIC FIDELITY, non-empty params: KNOWN, DISCLOSED limitation
			// of NarrowSchema (see its doc comment) — narrowing round-trips
			// the schema through json.Unmarshal/json.Marshal WITHOUT
			// UseNumber, so the 32-digit const is re-rendered to "1e+31".
			// The exact original literal no longer satisfies the compiled
			// schema; the re-rendered value does. This is a value change,
			// not cosmetic re-rendering (contrast the "1.500"/"1e-21" cases
			// documented on NarrowSchema, which are semantically identical).
			// A future fix to NarrowSchema (see the caveat in its doc
			// comment) should make this assertion fail loudly rather than
			// silently pass, which is why it is pinned here explicitly.
			name:   "numeric fidelity lost with non-empty params (disclosed NarrowSchema limitation)",
			schema: json.RawMessage(`{"type":"object","properties":{"n":{"const":10000000000000000000000000000001}}}`),
			params: map[string]any{"n": true},
			check: func(t *testing.T, v *ArgValidator) {
				t.Helper()
				exact := map[string]any{"n": json.Number("10000000000000000000000000000001")}
				if err := v.Validate(exact); err == nil {
					t.Error("exact 32-digit literal should now be REJECTED — narrowing re-rendered the const through float64")
				}
				rerendered := map[string]any{"n": json.Number("1e+31")}
				if err := v.Validate(rerendered); err != nil {
					t.Errorf("re-rendered 1e+31 constant should now be accepted: %v", err)
				}
			},
		},
		{
			// NUMERIC OVERFLOW: once params is non-empty, NarrowSchema's
			// plain json.Unmarshal into map[string]any rejects a numeric
			// literal outside float64 range outright. This is exactly the
			// failure decision (d) is designed to catch and degrade from —
			// compileArgValidator falls back to key-presence for this tool.
			name:        "numeric literal exceeding float64 range fails to compile once narrowed",
			schema:      json.RawMessage(`{"type":"object","properties":{"n":{"const":1e400}}}`),
			params:      map[string]any{"n": true},
			wantErr:     true,
			errContains: "narrowing schema",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, err := NewArgValidator(tc.schema, tc.params)
			if tc.wantErr {
				if err == nil {
					t.Fatal("NewArgValidator returned nil, want error")
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewArgValidator returned unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, v)
			}
		})
	}
}

func TestArgValidator_Validate(t *testing.T) {
	mustCompile := func(t *testing.T, schema string) *ArgValidator {
		t.Helper()
		v, err := NewArgValidator(json.RawMessage(schema), nil)
		if err != nil {
			t.Fatalf("NewArgValidator: %v", err)
		}
		return v
	}

	t.Run("wrong type", func(t *testing.T) {
		t.Parallel()
		v := mustCompile(t, `{"type":"object","properties":{"arg":{"type":"string"}}}`)
		err := v.Validate(map[string]any{"arg": float64(123)})
		var sve *SchemaViolationError
		if !errors.As(err, &sve) {
			t.Fatalf("errors.As(%v) = false, want *SchemaViolationError", err)
		}
		want := []SchemaViolation{{Field: "arg", Message: "got number, want string"}}
		if !violationsEqual(sve.Violations, want) {
			t.Errorf("Violations = %+v, want %+v", sve.Violations, want)
		}
	})

	t.Run("missing required", func(t *testing.T) {
		t.Parallel()
		v := mustCompile(t, `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
		err := v.Validate(map[string]any{})
		var sve *SchemaViolationError
		if !errors.As(err, &sve) {
			t.Fatalf("errors.As(%v) = false, want *SchemaViolationError", err)
		}
		want := []SchemaViolation{{Field: "name", Message: "missing required field: name"}}
		if !violationsEqual(sve.Violations, want) {
			t.Errorf("Violations = %+v, want %+v", sve.Violations, want)
		}
	})

	t.Run("root-level oneOf, wrong type inside a branch", func(t *testing.T) {
		t.Parallel()
		// #769 evidence: NarrowSchema/ValidateCall never touch a root-level
		// oneOf (no top-level "properties"), so the operator's params
		// narrowing is a silent no-op for such a tool (see #769). This test
		// only locks what #744 changes: the branch's own declared types are
		// now enforced pre-dispatch.
		v := mustCompile(t, `{"oneOf":[
			{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},
			{"type":"object","properties":{"b":{"type":"string"}},"required":["b"]}
		]}`)
		err := v.Validate(map[string]any{"a": float64(123)})
		var sve *SchemaViolationError
		if !errors.As(err, &sve) {
			t.Fatalf("errors.As(%v) = false, want *SchemaViolationError", err)
		}
		if len(sve.Violations) == 0 {
			t.Error("want non-empty Violations for a oneOf branch type mismatch")
		}
	})

	t.Run("root-level oneOf with additionalProperties:false branches, combined keys satisfy neither", func(t *testing.T) {
		t.Parallel()
		v := mustCompile(t, `{"oneOf":[
			{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false},
			{"type":"object","properties":{"b":{"type":"string"}},"required":["b"],"additionalProperties":false}
		]}`)
		err := v.Validate(map[string]any{"a": "x", "b": "y"})
		if err == nil {
			t.Fatal("Validate returned nil; want rejection — input satisfies neither branch")
		}
	})

	t.Run("root-level oneOf with overlapping branches, input satisfying both is rejected", func(t *testing.T) {
		t.Parallel()
		noneMatched := mustCompile(t, `{"oneOf":[
			{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false},
			{"type":"object","properties":{"b":{"type":"string"}},"required":["b"],"additionalProperties":false}
		]}`)
		noneMatchedErr := noneMatched.Validate(map[string]any{"a": "x", "b": "y"})
		if noneMatchedErr == nil {
			t.Fatal("expected the none-matched case to be rejected")
		}

		overlap := mustCompile(t, `{"oneOf":[
			{"type":"object","properties":{"a":{"type":"string"}}},
			{"type":"object","properties":{"b":{"type":"string"}}}
		]}`)
		overlapErr := overlap.Validate(map[string]any{})
		if overlapErr == nil {
			t.Fatal("Validate returned nil; want rejection — an empty object satisfies both branches, violating oneOf's exactly-one requirement")
		}

		// Locks the exactly-one semantics distinctly from the none-matched
		// case: the messages must differ, or a regression that collapses
		// "matched too many" into "matched none" would pass silently.
		if noneMatchedErr.Error() == overlapErr.Error() {
			t.Errorf("expected distinct messages for 'none matched' vs 'multiple matched', both rendered as %q", noneMatchedErr.Error())
		}
	})

	t.Run("nested object wrong type", func(t *testing.T) {
		t.Parallel()
		v := mustCompile(t, `{"type":"object","properties":{"outer":{"type":"object","properties":{"inner":{"type":"string"}}}}}`)
		err := v.Validate(map[string]any{"outer": map[string]any{"inner": float64(123)}})
		var sve *SchemaViolationError
		if !errors.As(err, &sve) {
			t.Fatalf("errors.As(%v) = false, want *SchemaViolationError", err)
		}
		want := []SchemaViolation{{Field: "outer.inner", Message: "got number, want string"}}
		if !violationsEqual(sve.Violations, want) {
			t.Errorf("Violations = %+v, want %+v", sve.Violations, want)
		}
	})

	t.Run("additionalProperties false rejects unexpected field", func(t *testing.T) {
		t.Parallel()
		v := mustCompile(t, `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`)
		err := v.Validate(map[string]any{"a": "x", "zzz": "y"})
		var sve *SchemaViolationError
		if !errors.As(err, &sve) {
			t.Fatalf("errors.As(%v) = false, want *SchemaViolationError", err)
		}
		want := []SchemaViolation{{Field: "zzz", Message: "unexpected field: zzz"}}
		if !violationsEqual(sve.Violations, want) {
			t.Errorf("Violations = %+v, want %+v", sve.Violations, want)
		}
	})

	t.Run("additionalProperties absent tolerates extra key", func(t *testing.T) {
		t.Parallel()
		// Locks decisions (b2)/(h): strictness is the schema author's
		// responsibility. Unknown-key rejection continues to come solely
		// from the ADR-017 key-presence gate (ValidateCall), not this gate.
		v := mustCompile(t, `{"type":"object","properties":{"a":{"type":"string"}}}`)
		if err := v.Validate(map[string]any{"a": "x", "zzz": "y"}); err != nil {
			t.Errorf("Validate returned %v, want nil — no additionalProperties keyword means extra keys are allowed", err)
		}
	})

	t.Run("valid input", func(t *testing.T) {
		t.Parallel()
		v := mustCompile(t, `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`)
		if err := v.Validate(map[string]any{"a": "x"}); err != nil {
			t.Errorf("Validate returned %v, want nil", err)
		}
	})

	t.Run("integer type accepts whole float64, rejects fractional", func(t *testing.T) {
		t.Parallel()
		v := mustCompile(t, `{"type":"object","properties":{"n":{"type":"integer"}}}`)
		if err := v.Validate(map[string]any{"n": float64(2)}); err != nil {
			t.Errorf("float64(2) should satisfy \"type\":\"integer\": %v", err)
		}
		if err := v.Validate(map[string]any{"n": float64(2.5)}); err == nil {
			t.Error("float64(2.5) should violate \"type\":\"integer\"")
		}
	})

	t.Run("multi-violation message is deterministic across repeated calls", func(t *testing.T) {
		t.Parallel()
		v := mustCompile(t, `{"type":"object","properties":{"arg":{"type":"string"},"name":{"type":"string"}},"required":["name"]}`)
		input := map[string]any{"arg": float64(123)}

		first := v.Validate(input)
		if first == nil {
			t.Fatal("Validate returned nil; want a multi-violation error")
		}
		for i := 0; i < 20; i++ {
			got := v.Validate(input)
			if got.Error() != first.Error() {
				t.Fatalf("call %d: message %q != call 0's message %q — sort is not stable", i, got.Error(), first.Error())
			}
		}
	})

	t.Run("zero-value ArgValidator returns an error instead of panicking", func(t *testing.T) {
		t.Parallel()
		// entry.argValidator != nil at the call site (agent.go) only checks
		// the pointer, not that it went through NewArgValidator — an
		// &ArgValidator{} built outside this package (e.g. by a test double)
		// must not panic.
		v := &ArgValidator{}
		if err := v.Validate(map[string]any{"a": "x"}); err == nil {
			t.Error("Validate on a zero-value ArgValidator returned nil, want an error")
		}
	})
}

// violationsEqual compares two SchemaViolation slices order-sensitively —
// Validate's output is already sorted, so callers assert on that order
// directly rather than re-sorting here.
func violationsEqual(got, want []SchemaViolation) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
