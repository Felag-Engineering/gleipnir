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
// uses (NewArgValidator). Policy params are not applied: the state is a
// property of the tool, and narrowing can only remove properties.
func ClassifyArgEnforcement(raw, canonical json.RawMessage) ArgEnforcement {
	if len(bytes.TrimSpace(canonical)) == 0 {
		if len(bytes.TrimSpace(raw)) == 0 {
			return ArgEnforcementNoSchema
		}
		return ArgEnforcementNoCanonicalSchema
	}
	if _, err := NewArgValidator(canonical, nil); err != nil {
		return ArgEnforcementUncompilable
	}
	return ArgEnforcementExact
}
