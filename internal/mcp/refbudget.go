package mcp

import (
	"fmt"
	"strconv"
	"strings"
)

// maxSchemaExpansionPaths bounds the total number of root-to-leaf schema
// evaluation paths a schema's "$ref" graph can imply, computed statically by
// checkSchemaExpansionBudget -- never by actually expanding "$ref" and
// running the validator.
//
// santhosh-tekuri/jsonschema/v6 has no memoization at VALIDATION time: it
// walks "$ref" fresh on every occurrence. A compact "$defs" graph shaped as
// a "diamond" -- each level's "allOf" branching into the SAME next-level
// pair of "$defs" entries -- costs 2^depth leaf evaluations on every single
// call, even for a small, perfectly valid instance, because "allOf" always
// evaluates every branch (this is not an invalid-argument edge case: it
// fires on ordinary, schema-satisfying input). Measured against a 2,689-byte
// schema (~130 total nodes -- well inside schemanorm's 1 MB / depth-64 /
// 10,000-node bounds, so those bounds alone do not catch this): "$defs"
// depth 40 took over 12s to validate {"q":"hello"} and was still running
// (~2^40 steps); depth 20 took 2.0s; depth 22 took 8.8s. Validate takes no
// context.Context, so nothing -- run cancellation, the per-tool timeout,
// GLEIPNIR_DRAIN_TIMEOUT -- can interrupt an in-flight call, and the
// goroutine cannot be killed, so a background timeout wrapper would not
// help either: the CPU burn persists regardless. This must be caught here,
// before compile, by counting.
//
// 10,000 is deliberately generous: a diamond only needs depth ~14
// (2^14 = 16,384) to cross it, and schemanorm's own doc puts a REAL tool
// schema's ordinary nesting at 4-15 levels total and 20-200 nodes -- so a
// legitimate schema would need to dedicate the bulk of its own nesting
// budget purely to "$ref" diamonds to get anywhere near this limit.
const maxSchemaExpansionPaths = 10_000

// maxSchemaRefCount and maxSchemaDefsEntries are cheap, independent
// defense-in-depth caps alongside maxSchemaExpansionPaths: a flat ceiling on
// how large a "$ref"/"$defs" graph a schema may declare at all, regardless
// of what the path-count math in checkSchemaExpansionBudget works out to.
// Both are generous relative to any real tool schema, which typically uses
// at most a handful of "$ref"/"$defs" entries, if any.
const (
	maxSchemaRefCount    = 500
	maxSchemaDefsEntries = 500
)

// checkSchemaExpansionBudget statically rejects a parsed schema document
// (doc, as returned by jsonschema.UnmarshalJSON) whose "$ref" graph could
// cause an unbounded or combinatorial validation cost, BEFORE it is ever
// compiled or validated against. See maxSchemaExpansionPaths's doc comment
// for the exact library behavior this guards against.
//
// This is a topological count over the document's own structure -- it never
// follows "$ref" more than once per distinct schema location (memoized), so
// it runs in time linear in the document size even though the NUMBER it
// computes can be exponential in the document's "$ref" nesting depth.
func checkSchemaExpansionBudget(doc any) error {
	refCount, defsCount := countRefsAndDefs(doc)
	if refCount > maxSchemaRefCount {
		return fmt.Errorf("mcp: schema declares %d \"$ref\" occurrences, exceeding the limit of %d", refCount, maxSchemaRefCount)
	}
	if defsCount > maxSchemaDefsEntries {
		return fmt.Errorf("mcp: schema declares %d \"$defs\"/\"definitions\" entries, exceeding the limit of %d", defsCount, maxSchemaDefsEntries)
	}

	c := &expansionCounter{root: doc, memo: map[string]int{}, visiting: map[string]bool{}}
	total, err := c.cost("", doc)
	if err != nil {
		return fmt.Errorf("mcp: schema \"$ref\" graph rejected: %w", err)
	}
	if total > maxSchemaExpansionPaths {
		return fmt.Errorf("mcp: schema's \"$ref\" graph implies at least %d validation paths, exceeding the limit of %d", total, maxSchemaExpansionPaths)
	}
	return nil
}

// countRefsAndDefs walks the entire document (not just the applicator graph
// expansionCounter follows) and counts every "$ref" key and every entry
// inside a "$defs"/"definitions" object, at any depth -- including inside
// "$defs" itself, which expansionCounter deliberately does not descend into
// on its own (see its doc comment). This is a flat structural cap,
// independent of the path-count budget: even if the multiplicative math
// somehow does not fully capture some ref-graph shape, a schema still cannot
// wire an unbounded number of "$ref" sites or reusable definitions into a
// single document.
func countRefsAndDefs(node any) (refCount, defsCount int) {
	switch v := node.(type) {
	case map[string]any:
		if _, ok := v["$ref"]; ok {
			refCount++
		}
		for key, val := range v {
			if key == "$defs" || key == "definitions" {
				if defsMap, ok := val.(map[string]any); ok {
					defsCount += len(defsMap)
				}
			}
			r, d := countRefsAndDefs(val)
			refCount += r
			defsCount += d
		}
	case []any:
		for _, val := range v {
			r, d := countRefsAndDefs(val)
			refCount += r
			defsCount += d
		}
	}
	return refCount, defsCount
}

// expansionCounter computes, for each distinct schema location reachable
// from root, a memoized "cost": the number of leaf-schema evaluations that
// location implies if fully expanded. A leaf costs 1. A location with
// applicator children (see schemaApplicatorArrayKeys and friends below)
// costs the SUM of its children's costs, because every one of them is
// evaluated -- this is what makes an "allOf" (or a "properties" map, or any
// other AND-shaped combinator) with two occurrences of an already-expensive
// child cost 2x that child, and a chain of such doublings cost 2^depth: the
// same shape santhosh-tekuri/jsonschema/v6 walks unmemoized at validation
// time, computed here as a number instead of actually being walked.
//
// Memoization is keyed by "$ref" string / structural path, so a node
// referenced from multiple places is computed once and its cost reused --
// this is what keeps the WORK done by cost() linear in document size even
// though the VALUE it computes can be exponential in "$ref" nesting depth.
// Every intermediate value is clamped at maxSchemaExpansionPaths+1 as soon
// as it is known to exceed the budget, so no arithmetic here ever grows
// large enough to risk an int overflow, regardless of how deep an attacker
// nests the graph.
type expansionCounter struct {
	root     any
	memo     map[string]int
	visiting map[string]bool
}

// schemaApplicatorSingleKeys are JSON Schema keywords whose value is a
// single subschema applied to (a view of) the same instance.
var schemaApplicatorSingleKeys = []string{
	"not", "if", "then", "else", "contains", "propertyNames",
	"additionalProperties", "unevaluatedProperties",
	"additionalItems", "unevaluatedItems",
}

// schemaApplicatorArrayKeys are JSON Schema keywords whose value is an array
// of subschemas, all applied together.
var schemaApplicatorArrayKeys = []string{"allOf", "anyOf", "oneOf", "prefixItems"}

// schemaApplicatorMapKeys are JSON Schema keywords whose value is an object
// mapping names to subschemas, all potentially applied together.
// "$defs"/"definitions" are deliberately NOT included here: they are inert
// storage, never auto-applied to an instance -- a location inside "$defs"
// only contributes cost when something reaches it via an explicit "$ref".
// Treating "$defs" as an implicit child would inflate legitimate schemas
// that organize many reusable, non-multiplying definitions under "$defs"
// without ever chaining them into a "$ref" diamond.
var schemaApplicatorMapKeys = []string{"properties", "patternProperties", "dependentSchemas"}

// cost returns node's expansion-path count. path is a memoization/cycle-
// detection key -- for a "$ref" child it is the ref string itself (so two
// "$ref"s to the same target share one memo entry and one cycle-detection
// slot); for a structural child it is a synthetic pointer-shaped path. cost
// returns an error only for a "$ref" cycle, which implies unbounded paths
// and can never be satisfied by any finite budget.
func (c *expansionCounter) cost(path string, node any) (int, error) {
	obj, ok := node.(map[string]any)
	if !ok {
		// Boolean schemas (true/false) and any other non-object value are leaves.
		return 1, nil
	}

	if cached, ok := c.memo[path]; ok {
		return cached, nil
	}
	if c.visiting[path] {
		return 0, fmt.Errorf("cyclic $ref at %q implies unbounded validation paths", path)
	}
	c.visiting[path] = true
	defer delete(c.visiting, path)

	total := 0
	found := false
	add := func(childPath string, child any) error {
		found = true
		if total > maxSchemaExpansionPaths {
			// Already over budget -- stop growing this node's number. The
			// clamp below still fires, so skipping further children here
			// only saves work; it never changes whether this node (or any
			// ancestor summing it) ends up over budget.
			return nil
		}
		n, err := c.cost(childPath, child)
		if err != nil {
			return err
		}
		total += n
		return nil
	}

	if ref, ok := obj["$ref"].(string); ok {
		if target, ok := resolveFragment(c.root, ref); ok {
			if err := add(ref, target); err != nil {
				return 0, err
			}
		} else {
			// Not a same-document fragment we can resolve (external URL, or
			// an unresolvable pointer): treat as an opaque leaf here. Any
			// genuinely external reference is rejected at actual compile
			// time by denyAllLoader regardless of what this budget check
			// concludes.
			found = true
			total++
		}
	}

	for _, key := range schemaApplicatorSingleKeys {
		if child, ok := obj[key]; ok {
			if err := add(path+"/"+key, child); err != nil {
				return 0, err
			}
		}
	}

	for _, key := range schemaApplicatorArrayKeys {
		if arr, ok := obj[key].([]any); ok {
			for i, child := range arr {
				if err := add(path+"/"+key+"/"+strconv.Itoa(i), child); err != nil {
					return 0, err
				}
			}
		}
	}

	// "items" is either a single schema (2020-12) or an array of schemas
	// (draft-07 tuple form) -- both shapes are compiled successfully
	// depending on dialect (see validate_test.go), so both must be walked.
	if items, ok := obj["items"]; ok {
		if arr, isArr := items.([]any); isArr {
			for i, child := range arr {
				if err := add(path+"/items/"+strconv.Itoa(i), child); err != nil {
					return 0, err
				}
			}
		} else if err := add(path+"/items", items); err != nil {
			return 0, err
		}
	}

	for _, key := range schemaApplicatorMapKeys {
		if m, ok := obj[key].(map[string]any); ok {
			for name, child := range m {
				if err := add(path+"/"+key+"/"+name, child); err != nil {
					return 0, err
				}
			}
		}
	}

	if !found {
		total = 1
	} else if total > maxSchemaExpansionPaths {
		total = maxSchemaExpansionPaths + 1
	}

	c.memo[path] = total
	return total, nil
}

// resolveFragment resolves a "#/a/b/c" JSON Pointer (RFC 6901) against root.
// Returns ok=false for anything that is not a same-document fragment
// (external URLs, non-fragment refs) or that fails to resolve; the caller
// treats that as an opaque leaf for expansion-counting purposes -- Compile
// (via denyAllLoader) is what actually rejects a genuinely external "$ref".
//
// Known limitation, deliberately not handled: this does not account for
// "$id" changing the base URI a relative "$ref" resolves against. A schema
// combining "$id" with relative "$ref"s could resolve to a different target
// than the plain document-root pointer walk performed here. Real-world MCP
// tool input schemas overwhelmingly use plain "#/$defs/..." fragments (the
// PoC shape this budget targets); "$id"-based indirection is out of scope
// for this static check.
func resolveFragment(root any, ref string) (any, bool) {
	if !strings.HasPrefix(ref, "#") {
		return nil, false
	}
	pointer := strings.TrimPrefix(strings.TrimPrefix(ref, "#"), "/")
	if pointer == "" {
		return root, true
	}

	cur := root
	for _, tok := range strings.Split(pointer, "/") {
		tok = rfc6901Unescape(tok)
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[tok]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(v) {
				return nil, false
			}
			cur = v[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}
