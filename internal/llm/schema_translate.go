package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ErrUnsupportedSchemaFeature is returned when a wire declares it cannot
// represent a JSON Schema keyword that the canonical schema uses. The
// simplifying transformations (discriminated oneOf → enum, permissive union
// with prose variants) land in issue #739; until then this pass fails closed
// rather than forwarding a construct the wire will silently mangle.
var ErrUnsupportedSchemaFeature = errors.New("llm: schema feature not supported by provider")

// TranslateForFeatures adapts canonical — a tool's InputSchema in canonical
// JSON Schema form — for a wire that declares f.
//
// Contract for (out, lossy, err): when err is non-nil, out and lossy carry no
// meaning and must not be used. When err is nil, out is always a non-empty,
// valid JSON Schema document that the caller may present to the wire; lossy
// reports whether out is merely a byte-identical view of canonical (false —
// safe to treat the two as the same schema) or a genuinely different,
// simplified rendering (true — issue #739 is the first caller that returns
// true; ProviderAdapter.prepareRequest is the consumer that copies-on-write
// and logs when it sees lossy == true).
//
// canonical is READ-ONLY: this function never writes into the bytes it is
// given, it only ever returns canonical itself or a distinct slice. That
// matters beyond byte-identity — the same backing array reached here is the
// run's schema of record, which the runtime also reads later to enforce
// policy parameter scoping against the tool call the model actually makes.
// A hypothetical in-place edit here would silently corrupt what enforcement
// checks against, not just what the model is shown.
//
// When f is full support, canonical is returned unmodified WITHOUT being
// parsed: a json.Unmarshal→json.Marshal round-trip would sort object keys and
// re-render numbers, breaking byte-identity, and would also relocate today's
// invalid-JSON errors out of the wires' own request building into this pass,
// changing the error text callers see. Both are unacceptable.
//
// For a restricted wire, the canonical schema is parsed and walked; a schema
// that uses only constructs f supports still gets byte-identical passthrough
// — this is not a stub, it is the real "compatible schema" path. A schema
// that uses an unsupported construct fails closed with
// ErrUnsupportedSchemaFeature rather than being forwarded and silently
// mangled by the wire.
//
// "Compatible" is scoped to the features SchemaFeatureSet models. Passthrough
// here does NOT promise the wire transmits the schema intact — a wire may still
// drop unmodelled value constraints (pattern, minimum, maxLength, ...) when it
// builds its own request type. See the SchemaFeatureSet doc comment; the
// returned lossy bool answers "did THIS pass rewrite anything", not "is what
// the model sees equivalent to the canonical schema".
func TranslateForFeatures(canonical json.RawMessage, f SchemaFeatureSet) (json.RawMessage, bool, error) {
	if f.IsFull() {
		return canonical, false, nil
	}
	if len(canonical) == 0 {
		return canonical, false, nil
	}

	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.UseNumber() // preserve numeric literals for #739's eventual re-marshal path
	var node any
	if err := dec.Decode(&node); err != nil {
		return canonical, false, fmt.Errorf("llm: parsing canonical schema: %w", err)
	}
	if dec.More() {
		return canonical, false, fmt.Errorf("llm: parsing canonical schema: unexpected trailing data after JSON value")
	}

	m, ok := node.(map[string]any)
	if !ok {
		// Boolean schema (true/false) or null: no keywords to gate.
		return canonical, false, nil
	}

	if kw, path, found := firstUnsupported(m, f, nil); found {
		return canonical, false, fmt.Errorf("%w: %q at %q", ErrUnsupportedSchemaFeature, kw, path)
	}
	return canonical, false, nil
}

// gate pairs a JSON Schema keyword with the SchemaFeatureSet accessor that
// permits it. It is a package-level type (rather than a literal local to
// firstUnsupported) precisely so featureGates can be declared once as a
// package-level table: TestFeatureGates_CoverAllSchemaFeatureSetFields
// reflects over SchemaFeatureSet's fields and asserts every one is wired to
// at least one gate.allowed closure here — a field added to the struct with
// no matching gate would otherwise silently fail open the moment some wire
// declares it false.
type gate struct {
	key     string
	allowed func(SchemaFeatureSet) bool
}

// featureGates lists every keyword that directly gates on a SchemaFeatureSet
// field, in a fixed order so a schema with several violations always reports
// the same one regardless of Go's randomized map iteration.
//
// $ref, $dynamicRef, and $recursiveRef are grouped with $defs/definitions
// under the single Defs flag: all three are ways to reference a schema
// defined elsewhere, and a wire that cannot represent $defs-style references
// cannot represent any of them.
var featureGates = []gate{
	{"$ref", func(f SchemaFeatureSet) bool { return f.Defs }},
	{"$dynamicRef", func(f SchemaFeatureSet) bool { return f.Defs }},
	{"$recursiveRef", func(f SchemaFeatureSet) bool { return f.Defs }},
	{"$defs", func(f SchemaFeatureSet) bool { return f.Defs }},
	{"definitions", func(f SchemaFeatureSet) bool { return f.Defs }},
	{"allOf", func(f SchemaFeatureSet) bool { return f.AllOf }},
	{"anyOf", func(f SchemaFeatureSet) bool { return f.AnyOf }},
	{"oneOf", func(f SchemaFeatureSet) bool { return f.OneOf }},
	{"not", func(f SchemaFeatureSet) bool { return f.Not }},
	{"const", func(f SchemaFeatureSet) bool { return f.Const }},
	{"format", func(f SchemaFeatureSet) bool { return f.Formats }},
}

// mapOfSchemasKeywords are keywords whose value is a JSON object mapping
// names to subschemas, walked in sorted key order for determinism:
//   - properties, patternProperties: JSON Schema (all drafts)
//   - $defs, definitions: the 2020-12 and draft-07 spellings of the same thing
//   - dependentSchemas: 2019-09+ split of draft-07's "dependencies"
//   - dependencies: draft-07's keyword, whose per-name value is EITHER an
//     array of required-property-name strings (not a subschema — skipped by
//     recurseMapKeys, which only follows map-shaped children) OR a schema
//     (the form recursed here)
var mapOfSchemasKeywords = []string{
	"properties", "patternProperties", "$defs", "definitions",
	"dependentSchemas", "dependencies",
}

// singleSchemaOrBoolKeywords are keywords whose value is either a single
// subschema or a boolean (true/false, meaning "any"/"none" — not a subschema,
// so skipped). additionalProperties is JSON Schema's original member of this
// family; unevaluatedProperties/unevaluatedItems (2019-09+) and
// additionalItems (draft-07, meaningful only when "items" is the array/tuple
// form) follow the same shape.
var singleSchemaOrBoolKeywords = []string{
	"additionalProperties", "unevaluatedProperties", "unevaluatedItems", "additionalItems",
}

// singleSchemaKeywords are keywords whose value is always a single subschema
// (never a bool, never an array).
var singleSchemaKeywords = []string{
	"contains", "propertyNames", "if", "then", "else", "not", "contentSchema",
}

// arrayOfSchemasKeywords are keywords whose value is an array of subschemas,
// walked in array order.
var arrayOfSchemasKeywords = []string{"allOf", "anyOf", "oneOf"}

// firstUnsupported recursively walks a decoded JSON Schema node looking for
// the first keyword f does not declare support for. It is schema-AWARE, not a
// blind key scan: a property literally named "format" or "oneOf" under
// "properties" is a property name, not a keyword, and must not false-positive
// — only keyword positions are checked.
//
// The subschema-recursion positions below are the full set of JSON Schema
// 2020-12 and draft-07 keywords whose value can contain a nested
// oneOf/anyOf/allOf/not/const/format/$ref/$defs violation: properties,
// patternProperties, $defs, definitions, dependentSchemas, dependencies,
// additionalProperties, unevaluatedProperties, unevaluatedItems,
// additionalItems, items (schema form or draft-07 tuple-array form),
// prefixItems, contains, propertyNames, if, then, else, not, contentSchema,
// allOf, anyOf, oneOf. Every other keyword in both drafts is either a plain
// value constraint (type, enum, minLength, pattern, uniqueItems,
// maxProperties, required, dependentRequired, multipleOf, ...), inert
// metadata (title, description, default, examples, deprecated, readOnly,
// writeOnly, $id, $schema, $comment, $vocabulary), an anchor DECLARATION
// rather than a reference resolution ($anchor, $dynamicAnchor,
// $recursiveAnchor — companions to $dynamicRef/$recursiveRef but not
// themselves constraint-bearing), or a non-schema annotation
// (contentEncoding, contentMediaType) — none of those can carry a nested
// schema, so none of them need a recursion position here.
//
// Both the gated-keyword check order and the subschema-recursion order are
// fixed so that a schema with multiple violations always reports the same
// one. This walker is the artifact issue #739 reuses to decide WHERE to
// transform.
//
// path accumulates JSON Pointer segments as a slice, not a concatenated
// string: building the pointer is deferred to pointerString, which is called
// only at the point a violation is actually found. A schema with no
// violations — the overwhelming common case — therefore never pays the cost
// of assembling a path string at every node it visits.
func firstUnsupported(node any, f SchemaFeatureSet, path []string) (keyword, at string, found bool) {
	m, ok := node.(map[string]any)
	if !ok {
		return "", "", false
	}

	for _, g := range featureGates {
		if _, present := m[g.key]; present && !g.allowed(f) {
			return g.key, pointerString(path, g.key), true
		}
	}

	for _, key := range mapOfSchemasKeywords {
		if kw, at, found := recurseMapKeys(m, key, f, path); found {
			return kw, at, true
		}
	}

	for _, key := range singleSchemaOrBoolKeywords {
		v, present := m[key]
		if !present {
			continue
		}
		sub, ok := v.(map[string]any)
		if !ok {
			continue // boolean form (true/false): no nested schema to walk
		}
		if kw, at, found := firstUnsupported(sub, f, append(path, key)); found {
			return kw, at, true
		}
	}

	if v, present := m["items"]; present {
		switch items := v.(type) {
		case map[string]any:
			if kw, at, found := firstUnsupported(items, f, append(path, "items")); found {
				return kw, at, true
			}
		case []any:
			if kw, at, found := recurseArray(items, "items", f, path); found {
				return kw, at, true
			}
		}
	}
	if v, present := m["prefixItems"]; present {
		if arr, ok := v.([]any); ok {
			if kw, at, found := recurseArray(arr, "prefixItems", f, path); found {
				return kw, at, true
			}
		}
	}

	for _, key := range singleSchemaKeywords {
		v, present := m[key]
		if !present {
			continue
		}
		sub, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if kw, at, found := firstUnsupported(sub, f, append(path, key)); found {
			return kw, at, true
		}
	}
	for _, key := range arrayOfSchemasKeywords {
		v, present := m[key]
		if !present {
			continue
		}
		arr, ok := v.([]any)
		if !ok {
			continue
		}
		if kw, at, found := recurseArray(arr, key, f, path); found {
			return kw, at, true
		}
	}

	return "", "", false
}

// recurseMapKeys walks a map-of-schemas keyword (see mapOfSchemasKeywords) in
// sorted key order so the result is deterministic regardless of Go's
// randomized map iteration. Entries whose value is not itself a schema
// (draft-07's "dependencies" allows an array-of-property-names form) are
// silently skipped, matching TranslateForFeatures's stance that only
// schema-bearing positions are walked.
func recurseMapKeys(m map[string]any, key string, f SchemaFeatureSet, path []string) (keyword, at string, found bool) {
	v, present := m[key]
	if !present {
		return "", "", false
	}
	sub, ok := v.(map[string]any)
	if !ok {
		return "", "", false
	}
	names := make([]string, 0, len(sub))
	for name := range sub {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		child, ok := sub[name].(map[string]any)
		if !ok {
			continue
		}
		childPath := append(append(path, key), escapeJSONPointerToken(name))
		if kw, at, found := firstUnsupported(child, f, childPath); found {
			return kw, at, true
		}
	}
	return "", "", false
}

// recurseArray walks an array-of-schemas keyword ("items" tuple form,
// "prefixItems", "allOf", "anyOf", "oneOf") in array order.
func recurseArray(arr []any, key string, f SchemaFeatureSet, path []string) (keyword, at string, found bool) {
	for i, v := range arr {
		sub, ok := v.(map[string]any)
		if !ok {
			continue
		}
		childPath := append(append(path, key), strconv.Itoa(i))
		if kw, at, found := firstUnsupported(sub, f, childPath); found {
			return kw, at, true
		}
	}
	return "", "", false
}

// pointerString renders path with key appended as an RFC 6901 JSON Pointer,
// e.g. pointerString([]string{"properties","x"}, "oneOf") = "/properties/x/oneOf".
// It is called only once, at the point firstUnsupported has actually found a
// violation, which is what keeps the walk itself free of per-node string
// concatenation.
func pointerString(path []string, key string) string {
	return "/" + strings.Join(append(path, key), "/")
}

// jsonPointerEscaper implements RFC 6901 JSON Pointer reference-token
// escaping: '~' becomes '~0' and '/' becomes '~1'. strings.NewReplacer scans
// the input once and, at each position, applies whichever single rule
// matches — unlike two sequential strings.Replace calls, which would risk
// re-escaping a '~1' that the '/' rule just produced, a NewReplacer pass
// never revisits output it has already written, so the order the two rules
// are listed in below does not affect the result. It is a package-level var,
// not a per-call allocation, because *strings.Replacer is safe for
// concurrent use and building one is the same cost every time.
var jsonPointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// escapeJSONPointerToken escapes name for use as an RFC 6901 JSON Pointer
// reference token.
func escapeJSONPointerToken(name string) string {
	return jsonPointerEscaper.Replace(name)
}
