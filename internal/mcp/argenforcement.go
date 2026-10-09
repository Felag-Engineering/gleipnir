package mcp

import (
	"bytes"
	"encoding/json"
)

// ArgEnforcement reports how exactly a tool's call arguments are checked
// before dispatch (#744). It is the operator-visible mirror of the fail-open
// branches in internal/execution/agent's compileArgValidator.
type ArgEnforcement string

const (
	// ArgEnforcementExact: the canonical schema compiles, so every call is
	// validated against the full schema.
	ArgEnforcementExact ArgEnforcement = "exact"
	// ArgEnforcementNoSchema: the tool declares no input schema at all, so
	// there is nothing to enforce beyond the policy's own parameter scoping.
	ArgEnforcementNoSchema ArgEnforcement = "no_schema"
	// ArgEnforcementNoCanonicalSchema: a schema was declared but
	// normalization failed at discovery, so no canonical form is stored.
	// Remedy: the server must publish a schema that normalizes (e.g. no
	// duplicate keys); re-discovering won't help until it does.
	ArgEnforcementNoCanonicalSchema ArgEnforcement = "no_canonical_schema"
	// ArgEnforcementUncompilable: a canonical form exists but does not
	// compile as a JSON Schema (bad $ref, invalid pattern, unsupported
	// dialect, ...). Remedy: the server must fix the schema's content.
	ArgEnforcementUncompilable ArgEnforcement = "schema_uncompilable"
)

// Reduced reports whether only the ADR-017 key-presence check applies.
func (e ArgEnforcement) Reduced() bool {
	return e != ArgEnforcementExact
}

// ClassifyArgEnforcement derives a tool's enforcement state from its stored
// raw and canonical schemas, using the same compile step the run-start gate
// uses (NewArgValidator), with no policy params applied.
//
// This describes the tool's UNSCOPED schema and is NOT a prediction of what a
// given policy's run will get. Narrowing can change whether a schema compiles
// in both directions: it can drop the one property carrying an invalid
// pattern (uncompilable -> exact), and it can drop the target of a "$ref" such
// as "#/properties/x" (exact -> uncompilable). Use
// ClassifyArgEnforcementWithParams for a specific grant; the tests in
// argenforcement_test.go pin both counterexamples.
func ClassifyArgEnforcement(raw, canonical json.RawMessage) ArgEnforcement {
	return ClassifyArgEnforcementWithParams(raw, canonical, nil)
}

// ClassifyArgEnforcementWithParams is ClassifyArgEnforcement for one policy
// grant: it compiles NarrowSchema(canonical, params), exactly what the
// run-start gate compiles for that grant (ADR-017).
func ClassifyArgEnforcementWithParams(raw, canonical json.RawMessage, params map[string]any) ArgEnforcement {
	if len(bytes.TrimSpace(canonical)) == 0 {
		if len(bytes.TrimSpace(raw)) == 0 {
			return ArgEnforcementNoSchema
		}
		return ArgEnforcementNoCanonicalSchema
	}
	if _, err := NewArgValidator(canonical, params); err != nil {
		return ArgEnforcementUncompilable
	}
	return ArgEnforcementExact
}
