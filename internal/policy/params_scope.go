package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Format-string constants for ADR-017 params-scope validation issues.
//
// None of these carry a field-path prefix. ValidationError.Error()
// (validator.go) renders "iss.Field + \": \" + iss.Message" whenever Field is
// non-empty, so a Message that also embeds the path would render it twice.
// The location lives SOLELY in the Issue.Field returned by
// validateParamsScope — see that function's doc for the exact field paths.
const (
	noCanonicalMsg  = "tool %q has no stored canonical schema — schema could not be canonicalized; parameter scoping unavailable for this tool (refresh the MCP server's tools, then save again)"
	notObjectMsg    = "tool %q canonical schema is not a JSON object; parameter scoping unavailable for this tool"
	noPropertiesMsg = "tool %q declares no top-level properties; parameter scoping unavailable for this tool"
	branchMsg       = "cannot scope %q — tool %q declares a top-level %q; parameter scoping applies only to top-level properties and cannot be enforced for branching schemas"
	unknownKeyMsg   = "%q is not a top-level property of tool %q"
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

// validateParamsScope checks that every key in params is a top-level property
// of the tool's canonical schema, and rejects any params block on a tool
// whose canonical schema is missing, not a JSON object, has no top-level
// "properties", or branches at the top level (topLevelBranchKeywords).
// toolIndex/toolRef locate the offending capabilities.tools[] entry.
//
// Field paths: whole-block issues (b, c, e) use
// "capabilities.tools[<toolIndex>].params"; per-key issues (d, f) use
// "capabilities.tools[<toolIndex>].params.<key>". toolIndex never appears in
// Message — only in Field — so Message is byte-identical across policies
// that differ only in tool position.
//
// This mirrors mcp.NarrowSchema's own notion of "top-level property" (the
// root "properties" map only) — keep the two in sync. canonicalization
// (internal/schemanorm) is byte-level member reordering only, so these shape
// checks are order-insensitive and would give the same answer against the
// raw schema; canonical is required here not because the shape differs, but
// because ITS PRESENCE is the gate (a stored canonical form is the proxy for
// "this schema was accepted by schemanorm").
func validateParamsScope(toolIndex int, toolRef string, params map[string]any, canonical json.RawMessage) []Issue {
	if len(params) == 0 {
		return nil
	}

	blockField := fmt.Sprintf("capabilities.tools[%d].params", toolIndex)

	if len(bytes.TrimSpace(canonical)) == 0 {
		return []Issue{{Field: blockField, Message: fmt.Sprintf(noCanonicalMsg, toolRef)}}
	}

	var root map[string]any
	if err := json.Unmarshal(canonical, &root); err != nil {
		// Never embed the unmarshal error or schema bytes in the message.
		return []Issue{{Field: blockField, Message: fmt.Sprintf(notObjectMsg, toolRef)}}
	}

	sortedKeys := make([]string, 0, len(params))
	for k := range params {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	for _, kw := range topLevelBranchKeywords {
		if _, present := root[kw]; !present {
			continue
		}
		issues := make([]Issue, 0, len(sortedKeys))
		for _, key := range sortedKeys {
			issues = append(issues, Issue{
				Field:   fmt.Sprintf("capabilities.tools[%d].params.%s", toolIndex, key),
				Message: fmt.Sprintf(branchMsg, key, toolRef, kw),
			})
		}
		return issues
	}

	props, ok := root["properties"].(map[string]any)
	if !ok {
		return []Issue{{Field: blockField, Message: fmt.Sprintf(noPropertiesMsg, toolRef)}}
	}

	var issues []Issue
	for _, key := range sortedKeys {
		if _, exists := props[key]; exists {
			continue
		}
		issues = append(issues, Issue{
			Field:   fmt.Sprintf("capabilities.tools[%d].params.%s", toolIndex, key),
			Message: fmt.Sprintf(unknownKeyMsg, key, toolRef),
		})
	}
	return issues
}
