package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrSchemaLimitExceeded is returned when a canonical schema (or the schema
// simplifySchema produces from it) exceeds one of the fixed bounds below. It
// propagates out of TranslateForFeatures exactly like ErrUnsupportedSchemaFeature,
// so an oversized, too-deep, or too-large schema fails the request closed
// rather than forwarding a malformed or gigantic document to the provider.
var ErrSchemaLimitExceeded = errors.New("llm: schema exceeds simplification limit")

// Bounds on the schema TranslateForFeatures will simplify, following the
// internal/schemanorm precedent (see that package's doc "Bounds" section):
// every bound is non-disableable — there is no constructor argument or
// environment variable that widens or removes one — and a violation is
// always a hard error, never a silent truncation. Real MCP tool schemas are
// tiny (tens to a few hundred KB at most); these limits sit orders of
// magnitude above that, matching schemanorm's own defaults where the same
// concern applies (byte size, nesting depth, node count).
//
// Unlike schemanorm — whose byte-level reordering cannot amplify beyond
// roughly 2x input size, so it carries no separate output bound — this pass
// can legitimately grow the document (merged properties, a synthesized
// discriminator enum, prose describing each collapsed variant), so
// maxSchemaOutputBytes exists as an independent, explicit backstop for that
// growth. It is not a tolerance for unbounded growth: mergeFirstVariant's
// description-doubling bug (which made output size double per nesting level)
// is fixed on its own merits below; this cap is the fail-closed fallback for
// that whole class of bug, not a substitute for fixing it.
const (
	// maxSchemaInputBytes caps len(canonical), checked before any decoding —
	// matches schemanorm.DefaultMaxBytes.
	maxSchemaInputBytes = 1 << 20 // 1,048,576
	// maxSchemaDepth caps object/array nesting depth in the decoded tree —
	// matches schemanorm.DefaultMaxDepth. Real tool schemas bottom out around
	// 4-15 levels.
	maxSchemaDepth = 64
	// maxSchemaNodes caps the total number of JSON values (objects, arrays,
	// and scalars) in the decoded tree — matches schemanorm.DefaultMaxNodes.
	// This is also what bounds the number of oneOf/anyOf variants a schema
	// may declare: a schema with N variants has well over N nodes, so an
	// adversarial variant count is rejected here before collapseBranch (and
	// its per-variant dedup work) ever runs.
	maxSchemaNodes = 10000
	// maxSchemaOutputBytes caps the re-marshalled output, checked after
	// simplifySchema and marshalSchema both succeed.
	maxSchemaOutputBytes = 4 << 20 // 4,194,304 — a generous multiple of maxSchemaInputBytes
)

// checkSchemaInputSize bounds canonical's raw byte length before any
// decoding happens — the only bound that protects against a single
// oversized input, mirroring schemanorm.NormalizeWithLimits's MaxBytes check.
func checkSchemaInputSize(canonical json.RawMessage) error {
	if len(canonical) > maxSchemaInputBytes {
		return fmt.Errorf("%w: input is %d bytes, exceeds limit of %d", ErrSchemaLimitExceeded, len(canonical), maxSchemaInputBytes)
	}
	return nil
}

// checkSchemaShape bounds the decoded tree's nesting depth and total node
// count before simplifySchema walks it.
func checkSchemaShape(m map[string]any) error {
	nodes := 0
	return walkSchemaShape(m, 1, &nodes)
}

// walkSchemaShape recursively visits v, incrementing *nodes for every value
// (map, slice, or scalar) and failing closed the instant depth exceeds
// maxSchemaDepth or *nodes exceeds maxSchemaNodes — before descending
// further, so neither bound can be defeated by continuing to recurse past it,
// and the recursion itself never goes deeper than maxSchemaDepth even for an
// adversarial document.
func walkSchemaShape(v any, depth int, nodes *int) error {
	if depth > maxSchemaDepth {
		return fmt.Errorf("%w: nesting depth exceeds limit of %d", ErrSchemaLimitExceeded, maxSchemaDepth)
	}
	*nodes++
	if *nodes > maxSchemaNodes {
		return fmt.Errorf("%w: node count exceeds limit of %d", ErrSchemaLimitExceeded, maxSchemaNodes)
	}

	switch val := v.(type) {
	case map[string]any:
		for _, child := range val {
			if err := walkSchemaShape(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range val {
			if err := walkSchemaShape(child, depth+1, nodes); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkSchemaOutputSize bounds the schema simplifySchema produces, once
// re-marshalled. See the const block above for why this pass needs an output
// bound that schemanorm does not.
func checkSchemaOutputSize(out json.RawMessage) error {
	if len(out) > maxSchemaOutputBytes {
		return fmt.Errorf("%w: simplified output is %d bytes, exceeds limit of %d", ErrSchemaLimitExceeded, len(out), maxSchemaOutputBytes)
	}
	return nil
}

// simplifySchema rewrites m in place, eliminating the JSON Schema constructs
// this pass knows how to eliminate (oneOf, anyOf, const) wherever f declares
// them unsupported, and reports whether it changed anything.
//
// Mutating the decoded tree in place is safe: TranslateForFeatures decodes
// canonical into a freshly allocated tree that aliases none of canonical's
// bytes and owns it exclusively for the remainder of the call. The "canonical
// is READ-ONLY" contract documented on TranslateForFeatures is about the
// input *byte slice*, which this function never touches — it is the decoded
// copy that gets rewritten.
//
// Recursion happens bottom-up: every child position is simplified first, so a
// nested oneOf inside a oneOf's own variant is already collapsed by the time
// that variant is examined as part of the parent's merge. The set of
// recursion positions below is exactly the set firstUnsupported walks
// (mapOfSchemasKeywords, singleSchemaOrBoolKeywords, singleSchemaKeywords,
// arrayOfSchemasKeywords, plus the items/prefixItems special cases) — the two
// walkers share those package-level tables so they cannot drift apart.
//
// After children are simplified, the node's own branch keywords collapse in a
// fixed order (oneOf, then anyOf), each only when the matching feature flag
// is false, and finally the node's own const folds when f.Const is false.
func simplifySchema(m map[string]any, f SchemaFeatureSet) bool {
	changed := false

	for _, key := range mapOfSchemasKeywords {
		if simplifyChildMap(m, key, f) {
			changed = true
		}
	}

	for _, key := range singleSchemaOrBoolKeywords {
		if simplifySingleSchemaChild(m, key, f) {
			changed = true
		}
	}

	if v, present := m["items"]; present {
		switch items := v.(type) {
		case map[string]any:
			if simplifySchema(items, f) {
				changed = true
			}
		case []any:
			if simplifyChildArray(items, f) {
				changed = true
			}
		}
	}
	if v, present := m["prefixItems"]; present {
		if arr, ok := v.([]any); ok {
			if simplifyChildArray(arr, f) {
				changed = true
			}
		}
	}

	for _, key := range singleSchemaKeywords {
		if simplifySingleSchemaChild(m, key, f) {
			changed = true
		}
	}

	for _, key := range arrayOfSchemasKeywords {
		if simplifyChildArray(schemaArray(m, key), f) {
			changed = true
		}
	}

	// Captured once, before either branch keyword collapses: mergeProperties
	// must scope Finding 3A's intersection against the properties m declared
	// BEFORE simplification touched it, never against a "properties" key a
	// SIBLING collapse on this same node may already have written. A node
	// can legally carry both "oneOf" and "anyOf" (TestSimplify_OneOfAndAnyOf_BothPresent);
	// with the fixed collapse order (oneOf first), reading m["properties"]
	// fresh inside mergeProperties's scope check would make oneOf's own
	// merged output look like "the parent's own ADR-017 scoping" to anyOf's
	// collapse, incorrectly rejecting anyOf's variant-contributed names too.
	scopeProperties, hasScopeProperties := m["properties"].(map[string]any)

	if !f.OneOf {
		if _, present := m["oneOf"]; present {
			collapseBranch(m, "oneOf", scopeProperties, hasScopeProperties)
			changed = true
		}
	}
	if !f.AnyOf {
		if _, present := m["anyOf"]; present {
			collapseBranch(m, "anyOf", scopeProperties, hasScopeProperties)
			changed = true
		}
	}
	if !f.Const {
		if _, present := m["const"]; present {
			foldConst(m)
			changed = true
		}
	}

	return changed
}

// simplifyChildMap simplifies every schema-valued entry of m[key] and reports
// whether any of them changed. It mirrors recurseMapKeys's shape-handling: an
// entry that is not a map[string]any (draft-07's "dependencies" allows an
// array-of-property-names form instead of a subschema) is left untouched.
func simplifyChildMap(m map[string]any, key string, f SchemaFeatureSet) bool {
	v, present := m[key]
	if !present {
		return false
	}
	sub, ok := v.(map[string]any)
	if !ok {
		return false
	}
	changed := false
	for _, child := range sub {
		if childMap, ok := child.(map[string]any); ok {
			if simplifySchema(childMap, f) {
				changed = true
			}
		}
	}
	return changed
}

// simplifySingleSchemaChild simplifies m[key] when present and map-shaped. It
// covers both singleSchemaOrBoolKeywords (whose boolean form carries no
// nested schema and is skipped) and singleSchemaKeywords (which are never
// boolean, so the same map-shape check applies without loss).
func simplifySingleSchemaChild(m map[string]any, key string, f SchemaFeatureSet) bool {
	v, present := m[key]
	if !present {
		return false
	}
	sub, ok := v.(map[string]any)
	if !ok {
		return false
	}
	return simplifySchema(sub, f)
}

// simplifyChildArray simplifies each map-shaped element of arr and reports
// whether any of them changed. Non-map entries (a boolean schema) are left
// untouched, mirroring recurseArray's shape-handling.
func simplifyChildArray(arr []any, f SchemaFeatureSet) bool {
	changed := false
	for _, v := range arr {
		if sub, ok := v.(map[string]any); ok {
			if simplifySchema(sub, f) {
				changed = true
			}
		}
	}
	return changed
}

// schemaArray returns m[key] as a []any, or nil if absent or not an array.
func schemaArray(m map[string]any, key string) []any {
	v, present := m[key]
	if !present {
		return nil
	}
	arr, _ := v.([]any)
	return arr
}

// collapseBranch rewrites m to remove keyword ("oneOf" or "anyOf"), replacing
// whatever it carried with an equivalent (discriminated enum) or approximate
// (permissive union / first-variant fallback) representation on m itself.
//
// scopeProperties/hasScopeProperties is simplifySchema's snapshot of m's own
// "properties" from BEFORE either branch keyword collapsed at this node —
// see the call site's comment for why a fresh read here would be wrong when
// m carries both "oneOf" and "anyOf". It is threaded through to
// mergeObjectVariants unchanged, regardless of which keyword this particular
// call is collapsing.
//
// It is TOTAL: every path below ends by deleting keyword from m via the
// deferred delete, so a restricted wire that declares the keyword false never
// reaches ErrUnsupportedSchemaFeature for it — this pass always eliminates
// the keyword before schema_translate.go's gate runs.
func collapseBranch(m map[string]any, keyword string, scopeProperties map[string]any, hasScopeProperties bool) {
	defer delete(m, keyword)

	rawVariants, ok := m[keyword].([]any)
	if !ok || len(rawVariants) == 0 {
		return
	}

	var objVariants []map[string]any
	for _, v := range rawVariants {
		if obj, ok := v.(map[string]any); ok {
			objVariants = append(objVariants, obj)
		}
	}
	if len(objVariants) == 0 {
		// Nothing representable (all entries were boolean schemas): the
		// presentation widens, and enforcement — which never consults this
		// pass's output — is unaffected.
		return
	}

	// oneOf:[X] is a generator artifact, not a meaningful choice; prose about
	// "variants" would be pure noise for a single variant.
	noProse := len(objVariants) == 1

	switch classifyVariants(objVariants) {
	case variantKindObject:
		mergeObjectVariants(m, keyword, objVariants, noProse, scopeProperties, hasScopeProperties)
	case variantKindScalar:
		mergeScalarVariants(m, keyword, objVariants, noProse)
	default:
		mergeFirstVariant(m, keyword, objVariants, noProse)
	}
}

// variantKind classifies a branch keyword's object-shaped variants so
// collapseBranch can pick the right merge strategy.
type variantKind int

const (
	// variantKindObject: every variant is object-shaped (or declares no type
	// but carries properties) — merge into a permissive union object.
	variantKindObject variantKind = iota
	// variantKindScalar: every variant shares one non-object JSON type —
	// merge into a single scalar schema, flattening tags into enum when possible.
	variantKindScalar
	// variantKindFirst: mixed variant types, or no type and no properties
	// anywhere — fall back to presenting only the first variant's shape.
	variantKindFirst
)

// classifyVariants applies the deterministic, membership/size-only rules that
// pick a merge strategy: no name heuristics, only what the variants actually
// declare.
func classifyVariants(objVariants []map[string]any) variantKind {
	typeSet := map[string]struct{}{}
	hasProps := false
	for _, v := range objVariants {
		if t, ok := v["type"].(string); ok {
			typeSet[t] = struct{}{}
		}
		if props, ok := v["properties"].(map[string]any); ok && len(props) > 0 {
			hasProps = true
		}
	}

	_, declaresObject := typeSet["object"]
	subsetOfObject := len(typeSet) == 0 || (len(typeSet) == 1 && declaresObject)
	exactlyObject := len(typeSet) == 1 && declaresObject

	switch {
	case subsetOfObject && (hasProps || exactlyObject):
		return variantKindObject
	case len(typeSet) == 1:
		return variantKindScalar
	default:
		return variantKindFirst
	}
}

// mergeObjectVariants implements the "permissive union (merged property
// superset)" strategy: every variant's properties are folded into one
// properties map, required becomes exact rather than a guess (the
// intersection across variants, unioned with the parent's own required), a
// discriminating tag property (if found and in scope — see the scope check
// below) is flattened into an exact enum, and — unless there was only one
// variant — prose describing the collapsed variants is appended to the
// node's description.
func mergeObjectVariants(m map[string]any, keyword string, objVariants []map[string]any, noProse bool, scopeProperties map[string]any, hasScopeProperties bool) {
	if _, ok := m["type"]; !ok {
		m["type"] = "object"
	}

	merged := mergeProperties(m, objVariants, scopeProperties, hasScopeProperties)

	discName, discValues, hasDisc := findDiscriminator(objVariants)
	if hasDisc && hasScopeProperties {
		if _, allowed := scopeProperties[discName]; !allowed {
			// round-2 Finding 1: mergeProperties's own scope intersection
			// (Finding 3A, above) only guards variant-contributed names
			// copied verbatim from a variant's "properties" map. The
			// discriminator entry bypassed that guard entirely — it is
			// synthesized separately, from every variant, and was
			// unconditionally injected below as "the single exception to
			// parent wins", so a discName the parent never declared
			// (ADR-017 scoping excluded it) was shown to, and REQUIRED of,
			// the model regardless. Treat an out-of-scope discriminator
			// exactly like any other variant-only name: drop it here, and
			// fall through to the plain (non-discriminated) prose below so
			// it is not named there either.
			hasDisc = false
			discName = ""
		}
	}
	if hasDisc {
		// The single exception to "parent wins": this entry is derived from
		// every variant and is exact, not a guess, so it replaces whatever
		// the parent (or a variant) declared for the same name — UNLESS the
		// parent's own enum for discName shares no value with the derived
		// set, in which case discriminatorSchema returns nil and the
		// parent's own entry, already sitting in merged from
		// mergeProperties's copy above, is left exactly as declared
		// (round-2 Finding 4).
		if schema := discriminatorSchema(m, objVariants, discName, discValues); schema != nil {
			if merged == nil {
				merged = map[string]any{}
			}
			merged[discName] = schema
		}
	}
	if len(merged) > 0 {
		m["properties"] = merged
	}

	if req := mergeRequired(m, objVariants, merged); len(req) > 0 {
		m["required"] = req
	} else {
		// round-2 Finding 3: mergeRequired can legitimately compute "nothing
		// is required" (for example every name that would have been
		// required was itself stripped from merged by Finding 3A's scope
		// intersection). Without this delete, m's OWN pre-merge "required" —
		// already folded into mergeRequired's starting set before that
		// filter ran — would survive untouched on m: exactly the stale,
		// uninvokable "required" name mergeRequired's own filter exists to
		// remove.
		delete(m, "required")
	}

	if noProse {
		return
	}
	appendDescription(m, variantProse(keyword, objVariants, discName, false))
}

// mergeProperties returns the properties map to attach to m: m's CURRENT
// properties (verbatim — the parent's own entries always win over variants;
// read fresh from m so a second collapse at the same node, e.g. anyOf
// following oneOf, correctly builds on the first collapse's output) plus
// every variant's properties in variant array order, where the FIRST variant
// to declare a given name wins among variants. Returns nil when the result
// would be empty so the caller does not assign an empty map.
//
// scopeProperties/hasScopeProperties — simplifySchema's snapshot of m's OWN
// "properties" from before ANY branch keyword collapsed at this node, never
// re-derived from m's current state — is the scoping boundary: a variant's
// contribution is only accepted for a name that ALSO appears in that
// snapshot, i.e. the union is intersected down to the schema's originally
// declared property names, never widened past them. mcp.NarrowSchema
// (ADR-017 parameter scoping) filters only the top-level properties/required
// of the canonical schema and leaves oneOf/anyOf variants untouched, so a
// variant can legally still carry a property name a policy's params block
// never granted; merging that name back in here would show the model a
// property the policy intentionally excluded, even though dispatch-time
// enforcement (which checks the schema of record, not this pass's output)
// still rejects any call that uses it — a violation of ADR-017's stated
// structural guarantee rather than a bypass, but a real one (security review
// Finding 3, Case A). When the schema declared no properties of its own,
// hasScopeProperties is false and there is nothing to scope against, so the
// full union stands.
func mergeProperties(m map[string]any, objVariants []map[string]any, scopeProperties map[string]any, hasScopeProperties bool) map[string]any {
	merged := map[string]any{}
	if existing, ok := m["properties"].(map[string]any); ok {
		for name, v := range existing {
			merged[name] = v
		}
	}
	for _, variant := range objVariants {
		props, ok := variant["properties"].(map[string]any)
		if !ok {
			continue
		}
		for name, v := range props {
			if _, present := merged[name]; present {
				continue
			}
			if hasScopeProperties {
				if _, allowed := scopeProperties[name]; !allowed {
					continue
				}
			}
			merged[name] = v
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

// mergeRequired computes the required list for a permissive union: the
// parent's own required names, unioned with the names required in EVERY
// variant. This is exact, not a widening — a property required in every
// branch is required unconditionally — which is why the variant side uses an
// intersection rather than a union: a permissive union must not carry a name
// that is only required in SOME variants.
//
// The result is then narrowed to the names present in merged — the
// properties map mergeObjectVariants is about to attach to m (already
// intersected against the parent's own declared properties above, and with
// the discriminator entry, if any, folded in). mergeProperties's Finding-3A
// intersection, independent of this function, can already have dropped a
// variant-only name from the presented properties; requiring a name with no
// corresponding properties entry would make the tool permanently
// uninvokable, since dispatch-time enforcement checks the schema of record's
// own required/properties, not this pass's presentation copy (security
// review Finding 5). Returns nil when the result would be empty.
func mergeRequired(m map[string]any, objVariants []map[string]any, merged map[string]any) []any {
	result := requiredSet(m["required"])

	var variantIntersection map[string]struct{}
	for i, variant := range objVariants {
		req := requiredSet(variant["required"])
		if i == 0 {
			variantIntersection = req
			continue
		}
		for name := range variantIntersection {
			if _, ok := req[name]; !ok {
				delete(variantIntersection, name)
			}
		}
	}
	for name := range variantIntersection {
		result[name] = struct{}{}
	}

	for name := range result {
		if _, ok := merged[name]; !ok {
			delete(result, name)
		}
	}

	if len(result) == 0 {
		return nil
	}
	names := make([]string, 0, len(result))
	for name := range result {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]any, len(names))
	for i, name := range names {
		out[i] = name
	}
	return out
}

// requiredSet decodes a "required" value (a []any of strings, per JSON
// Schema) into a set. A missing or malformed "required" is treated as the
// empty set, which is the correct input to mergeRequired's intersection: a
// variant that declares no required properties requires nothing in every
// branch.
func requiredSet(v any) map[string]struct{} {
	arr, _ := v.([]any)
	set := make(map[string]struct{}, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			set[s] = struct{}{}
		}
	}
	return set
}

// findDiscriminator looks for a property name that every variant tags with a
// distinct string value, so the branch can be flattened into an exact enum
// instead of an approximate permissive union.
//
//   - Requires len(objVariants) >= 2: with 0 or 1 variants there is nothing
//     to discriminate between.
//   - A property name is a candidate iff it appears in the properties map of
//     EVERY variant AND its subschema in every variant is a tag schema (see
//     isTagSchema) — a const whose value is a string, or a non-empty enum
//     whose elements are all strings. Both spellings are accepted regardless
//     of f.Const, because bottom-up recursion has already folded const into a
//     single-element enum whenever f.Const is false.
//   - Variants disagreeing on the property name (one tags "kind", another
//     tags "type") never share a candidate name, so there is no discriminator
//     — a plain permissive union with prose is used instead.
//   - If several names qualify, the lexicographically first is used.
//     Deterministic, with no "type"/"kind" name heuristics.
//   - The returned values are the concatenation of every variant's tag values
//     in variant array order, de-duplicated keeping the first occurrence.
func findDiscriminator(objVariants []map[string]any) (name string, values []any, ok bool) {
	if len(objVariants) < 2 {
		return "", nil, false
	}

	candidates := tagPropertyNames(objVariants[0])
	for _, variant := range objVariants[1:] {
		names := tagPropertyNames(variant)
		for candidate := range candidates {
			if _, present := names[candidate]; !present {
				delete(candidates, candidate)
			}
		}
	}
	if len(candidates) == 0 {
		return "", nil, false
	}

	sorted := make([]string, 0, len(candidates))
	for candidate := range candidates {
		sorted = append(sorted, candidate)
	}
	sort.Strings(sorted)
	name = sorted[0]

	for _, variant := range objVariants {
		props, _ := variant["properties"].(map[string]any)
		sub, _ := props[name].(map[string]any)
		values = append(values, tagValues(sub)...)
	}
	return name, dedupJSONValues(values), true
}

// tagPropertyNames returns the set of variant's property names whose
// subschema is a tag schema (see isTagSchema).
func tagPropertyNames(variant map[string]any) map[string]struct{} {
	names := map[string]struct{}{}
	props, ok := variant["properties"].(map[string]any)
	if !ok {
		return names
	}
	for name, v := range props {
		if sub, ok := v.(map[string]any); ok && isTagSchema(sub) {
			names[name] = struct{}{}
		}
	}
	return names
}

// isTagSchema reports whether sub is a "const" whose value is a JSON string,
// or a non-empty "enum" whose elements are ALL strings. A numeric or boolean
// tag disqualifies the property from being a discriminator candidate — the
// emitted discriminator schema always claims "type":"string", so it must only
// be built from data that is actually a string.
func isTagSchema(sub map[string]any) bool {
	if v, ok := sub["const"]; ok {
		_, isString := v.(string)
		return isString
	}
	arr, ok := sub["enum"].([]any)
	if !ok || len(arr) == 0 {
		return false
	}
	for _, v := range arr {
		if _, ok := v.(string); !ok {
			return false
		}
	}
	return true
}

// tagValues returns sub's tag values in schema order: a single value for
// "const", or the "enum" array as-is. It is used both by findDiscriminator
// (where isTagSchema has already restricted values to strings) and by
// flattenTagValues (where values may be of any JSON type), so it makes no
// assumption about value type.
func tagValues(sub map[string]any) []any {
	if v, ok := sub["const"]; ok {
		return []any{v}
	}
	if arr, ok := sub["enum"].([]any); ok {
		return arr
	}
	return nil
}

// discriminatorSchema builds the exact enum schema for the discriminator
// property, or nil when the override must not happen at all (round-2 Finding
// 4 — see below). When it does build a schema, it starts from the parent's
// own (pre-merge) declaration for name, if any, so constraints beyond "enum"
// (a "pattern", "format", "minLength", ...) survive the override instead of
// being silently discarded (round-2 Finding 5) — "type" and "enum" are the
// only parts this function's derivation actually overrides. The description
// carried over is the parent's own (pre-merge) declaration of that property
// if present, else the first variant's tag subschema's.
//
// The discriminator override is documented elsewhere as "the single
// exception to parent wins" — it replaces whatever the parent (or a variant)
// declared for name, because it is derived from every variant and is exact,
// not a guess. But "exact" only holds for a property the parent left
// unconstrained: if the parent's OWN declaration for name already carries an
// "enum", that enum is the actual constraint the tool enforces, and replacing
// it outright would WIDEN it — a parent enum:["read"] must not become
// enum:["read","delete"] just because one oneOf variant tags "delete"
// (security review Finding 4). intersectWithParentEnum narrows the derived
// values down to the ones the parent already allows in that case; when the
// parent declares no enum for name (the common case — R2's "parent directly
// declares the discriminator property" test has a plain "type":"string" with
// no enum), there is nothing to narrow against, so the derived values apply
// in full, exactly as before.
//
// round-2 Finding 4: when the parent DOES declare an enum and the narrowed
// intersection comes back empty (the parent's allowed values share nothing
// with any variant's tag — for example two branch keywords collapsing at
// the same node, oneOf then anyOf, discriminating on the same name with
// disjoint tag sets), an empty "enum" is not "no constraint" downstream: the
// Google wire (internal/llm/google/schema.go) treats an empty or absent
// "enum" as an UNCONSTRAINED string, which would widen the presented schema
// past the parent's own declaration — a different flavor of the same
// widening this function otherwise prevents. Returning nil tells the caller
// to skip the assignment entirely, leaving whatever mergeProperties already
// copied from the parent's own declared properties (untouched) in place.
func discriminatorSchema(m map[string]any, objVariants []map[string]any, name string, values []any) map[string]any {
	narrowed, hadParentEnum := intersectWithParentEnum(m, name, values)
	if hadParentEnum && len(narrowed) == 0 {
		return nil
	}

	schema := map[string]any{}
	if parentSchema, ok := parentPropertySchema(m, name); ok {
		for k, v := range parentSchema {
			schema[k] = v
		}
	}
	schema["type"] = "string"
	schema["enum"] = narrowed
	if desc := discriminatorDescription(m, objVariants, name); desc != "" {
		schema["description"] = desc
	}
	return schema
}

// intersectWithParentEnum narrows values down to the ones also present in
// m's own declared enum for name, reporting whether m declared one at all —
// the caller (discriminatorSchema) needs to distinguish "no parent enum, so
// nothing to narrow against" from "parent enum present, but the narrowed
// result happens to be empty" (round-2 Finding 4); only the latter must
// suppress the override entirely. When m declares no enum for name, values
// is returned unchanged and hadParentEnum is false. Membership is a
// marshal-once map lookup (like dedupJSONValues), not a marshal-per-comparison
// scan, for the same reason BLOCKER 2 replaced containsJSONValue elsewhere in
// this file.
func intersectWithParentEnum(m map[string]any, name string, values []any) (narrowed []any, hadParentEnum bool) {
	parentEnum, ok := parentPropertyEnum(m, name)
	if !ok {
		return values, false
	}

	allowed := make(map[string]struct{}, len(parentEnum))
	for _, v := range parentEnum {
		key, err := marshalSchema(v)
		if err != nil {
			continue
		}
		allowed[string(key)] = struct{}{}
	}

	narrowed = make([]any, 0, len(values))
	for _, v := range values {
		key, err := marshalSchema(v)
		if err != nil {
			continue
		}
		if _, ok := allowed[string(key)]; ok {
			narrowed = append(narrowed, v)
		}
	}
	return narrowed, true
}

// parentPropertySchema returns m.properties[name] itself, or ok == false if
// either step of that path is absent or not the expected shape.
func parentPropertySchema(m map[string]any, name string) (sub map[string]any, ok bool) {
	props, ok := m["properties"].(map[string]any)
	if !ok {
		return nil, false
	}
	sub, ok = props[name].(map[string]any)
	return sub, ok
}

// parentPropertyEnum returns m.properties[name].enum, or ok == false if any
// step of that path is absent or not the expected shape.
//
// This only recognizes the "enum" spelling, not "const". That is safe today
// only because simplifySchema recurses bottom-up and folds a node's own
// "const" into a single-element "enum" (foldConst) before that node's own
// oneOf/anyOf ever collapses — by the time a Google-wire schema reaches this
// function, a parent property declared via "const" has already become
// enum-shaped. A future wire that declares
// SchemaFeatureSet{OneOf: false, Const: true} (oneOf eliminable, const left
// alone) would reach this function with a parent property still in "const"
// form, silently skip the Finding-4 narrowing this function exists for, and
// re-open the widening bug it fixes. If that combination is ever declared,
// this needs a const-aware lookup (or an explicit rejection), not a silent
// pass-through.
func parentPropertyEnum(m map[string]any, name string) (enum []any, ok bool) {
	sub, ok := parentPropertySchema(m, name)
	if !ok {
		return nil, false
	}
	enum, ok = sub["enum"].([]any)
	return enum, ok
}

func discriminatorDescription(m map[string]any, objVariants []map[string]any, name string) string {
	if desc := propertyDescription(m, name); desc != "" {
		return desc
	}
	if len(objVariants) == 0 {
		return ""
	}
	return propertyDescription(objVariants[0], name)
}

// propertyDescription returns node.properties[name].description, or "" if
// any step of that path is absent or not the expected shape.
func propertyDescription(node map[string]any, name string) string {
	props, ok := node["properties"].(map[string]any)
	if !ok {
		return ""
	}
	sub, ok := props[name].(map[string]any)
	if !ok {
		return ""
	}
	desc, _ := sub["description"].(string)
	return desc
}

// mergeScalarVariants implements the strategy for variants that all share one
// non-object JSON type: fold a discriminating tag directly into an exact
// "enum" when every variant provides one, otherwise fall back to prose.
func mergeScalarVariants(m map[string]any, keyword string, objVariants []map[string]any, noProse bool) {
	if _, ok := m["type"]; !ok {
		if t := variantsType(objVariants); t != "" {
			m["type"] = t
		}
	}

	if _, hasOwnEnum := m["enum"]; !hasOwnEnum {
		if flattenTagValues(m, objVariants) {
			// This is the pure scalar form of "discriminated oneOf flattens
			// to an enum": the result is exact, so no prose is needed even
			// when there is more than one variant.
			return
		}
	}

	if noProse {
		return
	}
	appendDescription(m, variantProse(keyword, objVariants, "", false))
}

// variantsType returns the single "type" value shared by every variant, or ""
// if none declare one. classifyVariants has already established this set has
// at most one member before routing to mergeScalarVariants.
func variantsType(objVariants []map[string]any) string {
	for _, v := range objVariants {
		if t, ok := v["type"].(string); ok {
			return t
		}
	}
	return ""
}

// flattenTagValues sets m["enum"] to the de-duplicated, first-appearance
// union of every variant's tag values and reports whether it did so. It only
// applies when EVERY variant is a tag schema (const or non-empty enum, of any
// JSON value type — unlike the discriminator, a scalar union does not need to
// claim a single derived type for the tag itself, since m already has one).
func flattenTagValues(m map[string]any, objVariants []map[string]any) bool {
	for _, v := range objVariants {
		if !hasTagValues(v) {
			return false
		}
	}

	var values []any
	for _, v := range objVariants {
		values = append(values, tagValues(v)...)
	}
	m["enum"] = dedupJSONValues(values)
	return true
}

// hasTagValues reports whether sub is a "const" or a non-empty "enum",
// regardless of value type.
func hasTagValues(sub map[string]any) bool {
	if _, ok := sub["const"]; ok {
		return true
	}
	arr, ok := sub["enum"].([]any)
	return ok && len(arr) > 0
}

// dedupJSONValues returns values with duplicates removed, keeping the first
// occurrence of each. Duplicates are identified by JSON-value equality (via
// marshalSchema) rather than Go's ==, which would panic comparing the
// map/slice values a JSON Schema enum is legally allowed to carry. Each value
// is marshalled exactly ONCE, into a map keyed on the marshalled bytes — a
// prior implementation instead compared every candidate against every
// already-kept value by marshalling BOTH sides on each comparison
// (containsJSONValue/jsonValueEqual), which is O(N^2) full JSON encodes for N
// values. The security review measured that at 15.4 seconds for 8,000
// variants, held per tool per LLM request inside the agent loop's
// concurrency-limited run path.
func dedupJSONValues(values []any) []any {
	seen := make(map[string]struct{}, len(values))
	out := make([]any, 0, len(values))
	for _, v := range values {
		key, err := marshalSchema(v)
		if err != nil {
			// marshalSchema only fails on a value encoding/json cannot
			// marshal at all; every value here was itself decoded FROM JSON
			// by TranslateForFeatures (map[string]any/[]any/string/bool/
			// json.Number/nil), so this is unreachable in practice. Keep the
			// value rather than silently dropping it on an error path that
			// should never execute.
			out = append(out, v)
			continue
		}
		if _, ok := seen[string(key)]; ok {
			continue
		}
		seen[string(key)] = struct{}{}
		out = append(out, v)
	}
	return out
}

// mergeFirstVariant implements the fallback strategy for mixed variant types
// (for example a oneOf mixing a string variant and an array variant): copy
// every top-level key of the first variant into m for keys m does not
// already declare (the parent's own declaration always wins), and append
// prose making clear that only the first variant's shape is shown. This
// keeps collapseBranch total even for shapes the pass cannot merge exactly.
//
// "description" is deliberately excluded from the copy whenever prose is
// about to be appended: describeVariant(objVariants[0]), called below via
// variantProse, already surfaces that exact text as the first bullet, so
// copying it here too would duplicate it. Because collapseBranch runs
// bottom-up, an unfixed duplication compounds across nesting levels — a
// variant's description, once doubled by one level's fallback merge, gets
// doubled again by every enclosing oneOf/anyOf that also falls back,
// producing exponential output growth from linear input. A previous version
// of this function copied "description" unconditionally; the security review
// that found this measured a 20-level chain of nested mixed-type oneOf
// blowing up from ~667 bytes of input to 188.7 MB of output. When there is no
// prose to fall back on (a single, typeless, propertyless variant, so
// noProse is true), the description is still copied so the information is
// not silently dropped — there is nothing else to convey it.
//
// round-2 Finding 2: the copy loop above can bring in variant[0]'s own
// "required" verbatim whenever m does not already declare one of its own —
// unlike "properties", "required" is copied with no relationship check to
// whatever ends up in m["properties"] afterward. That misses two ways a
// dangling name can result: m already had its own "properties" (so
// "properties" is NOT copied here, m keeps its own, but "required" from a
// propertyless variant — a bare "type":"array" with its own "required" —
// still gets copied), or variant[0] itself declares "required" for a name
// its own "properties" doesn't carry. Either way the tool ends up with a
// required name that has no properties entry, permanently uninvokable once
// dispatch-time enforcement rejects any call containing it (the same failure
// mode Finding 5's mergeRequired filter exists to prevent on the
// mergeObjectVariants path). filterRequiredAgainstProperties applies that
// same filter here.
func mergeFirstVariant(m map[string]any, keyword string, objVariants []map[string]any, noProse bool) {
	for key, v := range objVariants[0] {
		if key == "description" && !noProse {
			continue
		}
		if _, present := m[key]; !present {
			m[key] = v
		}
	}
	filterRequiredAgainstProperties(m)

	if noProse {
		return
	}
	appendDescription(m, variantProse(keyword, objVariants, "", true))
}

// filterRequiredAgainstProperties drops any name from m["required"] that is
// not also a key of m["properties"], deleting "required" entirely if that
// empties it. It is a no-op when m declares no "properties" at all — with
// nothing to check membership against, the "required" copied by
// mergeFirstVariant is left as-is, matching the copy loop's own "parent's
// own declaration always wins" rule for every other key.
func filterRequiredAgainstProperties(m map[string]any) {
	props, ok := m["properties"].(map[string]any)
	if !ok {
		return
	}
	req, ok := m["required"].([]any)
	if !ok {
		return
	}

	filtered := make([]any, 0, len(req))
	for _, v := range req {
		name, ok := v.(string)
		if !ok {
			continue
		}
		if _, present := props[name]; present {
			filtered = append(filtered, v)
		}
	}

	if len(filtered) == 0 {
		delete(m, "required")
		return
	}
	m["required"] = filtered
}

// foldConst replaces m's "const" with an equivalent single-value "enum",
// inferring "type" from the const's value when m does not already declare
// one. Only reached when f.Const is false.
func foldConst(m map[string]any) {
	v := m["const"]
	delete(m, "const")
	m["enum"] = []any{v}
	if _, hasType := m["type"]; !hasType {
		m["type"] = inferJSONType(v)
	}
}

// inferJSONType derives a JSON Schema "type" value from a decoded JSON value.
// v was decoded with json.Decoder.UseNumber, so a numeric literal arrives as
// a json.Number rather than a float64; whether it renders as "integer" or
// "number" is decided by inspecting the literal text for a decimal point or
// exponent, not by parsing it as a float (which would risk misrepresenting
// large integers).
func inferJSONType(v any) string {
	switch val := v.(type) {
	case string:
		return "string"
	case json.Number:
		if strings.ContainsAny(string(val), ".eE") {
			return "number"
		}
		return "integer"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		// v == nil (JSON null) falls here too: an untyped nil interface
		// doesn't match any of the typed cases above.
		return "null"
	}
}

// variantProse builds the full "why this schema was rewritten" block appended
// to the node's description: a lead sentence naming the collapse rule
// (discriminated oneOf, plain oneOf, anyOf, or the first-variant fallback),
// then one bullet per variant describing what it looked like before collapse.
// discName == "" means no discriminator was found.
func variantProse(keyword string, objVariants []map[string]any, discName string, isFallback bool) string {
	lead := proseLead(keyword, discName)
	if isFallback {
		lead += " Only variant 1's shape is shown."
	}

	bullets := make([]string, len(objVariants))
	for i, v := range objVariants {
		bullets[i] = "- " + variantLabel(i, v, discName) + ": " + describeVariant(v)
	}

	return lead + "\n" + strings.Join(bullets, "\n")
}

// proseLead returns the lead sentence for keyword ("oneOf" or "anyOf"),
// naming the discriminator property when one was found.
func proseLead(keyword, discName string) string {
	if discName != "" {
		return fmt.Sprintf("Exactly one of the following variants applies, selected by %q; do not mix properties from different variants.", discName)
	}
	if keyword == "oneOf" {
		return "Exactly one of the following variants applies; do not mix properties from different variants."
	}
	return "At least one of the following variants applies."
}

// variantLabel returns the bullet label for variant i (0-based): the
// variant's own tag values, comma-separated and quoted, when discName names a
// discriminator; otherwise "Variant <1-based i>".
func variantLabel(i int, variant map[string]any, discName string) string {
	if discName == "" {
		return fmt.Sprintf("Variant %d", i+1)
	}
	props, _ := variant["properties"].(map[string]any)
	sub, _ := props[discName].(map[string]any)
	tags := tagValues(sub)
	labels := make([]string, len(tags))
	for j, t := range tags {
		labels[j] = fmt.Sprintf("%q", t)
	}
	return strings.Join(labels, ", ")
}

// describeVariant renders a one-line summary of a variant for the prose
// bullet list: its own description if it has one, else its sorted property
// names if it has any, else its declared type, else "any value".
func describeVariant(v map[string]any) string {
	if desc, ok := v["description"].(string); ok && desc != "" {
		return desc
	}
	if props, ok := v["properties"].(map[string]any); ok && len(props) > 0 {
		names := make([]string, 0, len(props))
		for name := range props {
			names = append(names, name)
		}
		sort.Strings(names)
		return "properties: " + strings.Join(names, ", ")
	}
	if t, ok := v["type"].(string); ok {
		return "type: " + t
	}
	return "any value"
}

// appendDescription sets m's description to block, preserving any existing
// non-empty description above it separated by a blank line.
func appendDescription(m map[string]any, block string) {
	if existing, ok := m["description"].(string); ok && existing != "" {
		m["description"] = existing + "\n\n" + block
		return
	}
	m["description"] = block
}

// marshalSchema marshals a decoded generic tree back to compact JSON bytes,
// mirroring internal/schemanorm.marshalDeterministic — deliberately
// reimplemented rather than imported, since internal/schemanorm is a
// standalone leaf package and this pass has no other reason to depend on it.
// SetEscapeHTML(false) keeps description prose readable instead of
// backslash-u-escaping "<", ">", and "&". encoding/json sorts map[string]any
// keys byte-wise and marshals json.Number verbatim, so the output is
// deterministic and numeric literals survive the round trip even though
// simplifySchema walks the tree via Go's randomized map iteration.
func marshalSchema(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("llm: marshalling schema: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
