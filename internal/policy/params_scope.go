package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Format-string constants for ADR-017 params-scope warnings.
//
// These are WARNINGS, never blocking issues: a params block does not prevent a
// policy from saving. See validateParamsScope for why, and for the deliberate
// security posture that choice represents.
//
// Unlike Issue messages (validator.go), which carry no field-path prefix
// because ValidationError.Error() renders "iss.Field + \": \" + iss.Message",
// warnings are plain strings with no Field to render. Each message therefore
// embeds its own "capabilities.tools[N].params[.key]" location as its first
// verb — the opposite of the Issue convention. Do not "fix" one to match the
// other.
//
// Each message states what actually happens AT RUNTIME for that case, which is
// not uniform — see the table in validateParamsScope's doc. A warning that said
// "scoping is not enforced" across the board would be wrong for three of these
// five, and an operator acting on a false warning is worse off than one acting
// on none.
const (
	// Narrowing still runs at runtime against the RAW schema
	// (mcp.ResolvedTool.SchemaForNarrowing falls back when canonical is
	// absent), so scoping is typically still enforced — it simply could not be
	// verified here.
	noCanonicalWarn = "%s: tool %q has no stored canonical schema, so its parameter scoping could not be verified at save time. Scoping is still applied at runtime against the tool's raw schema. Refresh the MCP server's tools and save again to have it verified."

	// A schema that fails to unmarshal here will also fail inside
	// mcp.NarrowSchema, which returns an error rather than silently passing —
	// so this surfaces loudly at runtime rather than degrading scoping.
	notObjectWarn = "%s: tool %q has a canonical schema that is not a JSON object, so its parameter scoping could not be verified at save time and tool calls are likely to fail at runtime. Refresh the MCP server's tools."

	// The genuine #769 hole: mcp.NarrowSchema returns the schema unchanged and
	// mcp.ValidateCall permits every key.
	noPropertiesWarn = "%s: tool %q declares no top-level properties, so parameter scoping does NOT restrict this tool — the agent sees the full schema and every argument key is permitted. Use approval gating if this tool needs constraining."

	// Root branch keyword WITH top-level properties: the root properties are
	// narrowed and enforced; only branch-nested properties escape.
	branchPartialWarn = "%s: tool %q declares a top-level %q. Scoping narrows and enforces its top-level properties, but does not reach properties nested inside the %q branches — the agent may be shown branch-nested properties that dispatch then rejects."

	// Root branch keyword with NO top-level properties: nothing to narrow.
	branchNoScopeWarn = "%s: tool %q declares a top-level %q and no top-level properties, so parameter scoping does NOT restrict this tool — the agent sees the full schema and every argument key is permitted. Use approval gating if this tool needs constraining."

	unknownKeyWarn = "%s: %q is not a top-level property of tool %q, so it narrows nothing. If it is the only key in this params block, the tool will accept no arguments at all."
)

// topLevelBranchKeywords are the root-level JSON Schema keywords under which
// ADR-017 params scoping cannot be structurally enforced. Alphabetical, and
// iterated in that order so the keyword reported in branchMsg is
// deterministic when a root schema carries more than one.
//
//   - "$ref": schemanorm does not resolve $ref (ADR-059); the real property
//     set lives in the referenced document, so mcp.NarrowSchema sees no
//     top-level "properties" and silently leaves the schema unchanged.
//   - "allOf": schemanorm deliberately does not flatten allOf; a branch may
//     declare properties that root-level narrowing never touches.
//   - "anyOf": narrowing does not narrow into anyOf variants — rejected even
//     when the root ALSO has top-level "properties". The key-presence gate
//     still fires in that case, but narrowing itself only ever touches the
//     root, so the schema shown to the agent still advertises variant-nested
//     properties from inside the branches — properties dispatch-time
//     validation (mcp.ValidateCall) then rejects. Showing the agent a
//     property it can never actually send is the exact defect this file
//     closes (#769).
//   - "if": "then"/"else" can introduce properties root-level narrowing
//     never touches.
//   - "not": narrowing does not compose under negation.
//   - "oneOf": same class as "anyOf".
//
// Deliberately NOT in this list: "dependentSchemas", "patternProperties",
// "additionalProperties", "unevaluatedProperties", "unevaluatedItems". None
// of these can widen the permitted key set past the narrowed root
// "properties" (the key-presence gate in mcp.NarrowSchema/ValidateCall IS
// the allowlist), and in-place narrowing only removes property annotations
// — which makes "unevaluated*: false" strictly more selective, not less.
var topLevelBranchKeywords = []string{"$ref", "allOf", "anyOf", "if", "not", "oneOf"}

// validateParamsScope reports, as non-blocking warnings, every way in which a
// params block's ADR-017 scoping will not do what its author probably expects.
// toolIndex/toolRef locate the capabilities.tools[] entry.
//
// # Why warnings and not rejections
//
// #788 originally implemented #769's "option 2 — fail loudly", rejecting these
// policies at save. That was reverted deliberately (see #769): it blocked
// legitimate saves — most importantly any tool whose server had not been
// rediscovered since the canonical_schema column landed, which is a fleet-wide
// condition an operator cannot fix from the policy editor. The chosen posture
// is #769's option 3: accept the gap, surface it, and revisit real narrowing
// later.
//
// This is a conscious security trade. ADR-017's promise — that scoping is
// structural rather than prompt-based — does NOT hold for the noProperties and
// branchNoScope cases below: mcp.NarrowSchema returns the schema unchanged and
// mcp.ValidateCall permits every key, so an agent can pass any argument the
// tool accepts. The warning is the only signal an operator gets, which is why
// each message states the runtime consequence explicitly instead of a generic
// "could not validate".
//
// # Runtime behavior per case
//
// Not uniform — mcp.ResolvedTool.SchemaForNarrowing falls back to the raw
// schema when canonical is absent, and mcp.NarrowSchema no-ops only when the
// schema it is handed has no usable top-level "properties" map:
//
//	no canonical schema   narrows the RAW schema — usually still enforced,
//	                      merely unverified here
//	canonical not JSON    mcp.NarrowSchema also fails to unmarshal and returns
//	                      an error — loud at runtime, not a silent downgrade
//	branch + properties   root properties narrowed and enforced; only
//	                      branch-nested properties escape
//	branch, no properties NOT enforced — every key permitted
//	no properties         NOT enforced — every key permitted
//	unknown key           narrows nothing; if it is the only key, the narrowed
//	                      property set is empty and the tool accepts nothing
//
// This mirrors mcp.NarrowSchema's own notion of "top-level property" (the root
// "properties" map only) — keep the two in sync. Canonicalization
// (internal/schemanorm) is byte-level member reordering only, so these shape
// checks are order-insensitive and would give the same answer against the raw
// schema.
func validateParamsScope(toolIndex int, toolRef string, params map[string]any, canonical json.RawMessage) []string {
	if len(params) == 0 {
		return nil
	}

	blockField := fmt.Sprintf("capabilities.tools[%d].params", toolIndex)

	if len(bytes.TrimSpace(canonical)) == 0 {
		return []string{fmt.Sprintf(noCanonicalWarn, blockField, toolRef)}
	}

	var root map[string]any
	if err := json.Unmarshal(canonical, &root); err != nil {
		// Never embed the unmarshal error or schema bytes in the message.
		return []string{fmt.Sprintf(notObjectWarn, blockField, toolRef)}
	}

	sortedKeys := make([]string, 0, len(params))
	for k := range params {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	props, hasProps := root["properties"].(map[string]any)

	for _, kw := range topLevelBranchKeywords {
		if _, present := root[kw]; !present {
			continue
		}
		// One warning for the block, not one per key: the condition is a
		// property of the tool's schema, not of any individual key, and a
		// per-key fan-out would bury the signal on a wide params block.
		if hasProps {
			return []string{fmt.Sprintf(branchPartialWarn, blockField, toolRef, kw, kw)}
		}
		return []string{fmt.Sprintf(branchNoScopeWarn, blockField, toolRef, kw)}
	}

	if !hasProps {
		return []string{fmt.Sprintf(noPropertiesWarn, blockField, toolRef)}
	}

	var warnings []string
	for _, key := range sortedKeys {
		if _, exists := props[key]; exists {
			continue
		}
		warnings = append(warnings, fmt.Sprintf(
			unknownKeyWarn,
			fmt.Sprintf("capabilities.tools[%d].params.%s", toolIndex, key),
			key, toolRef,
		))
	}
	return warnings
}
