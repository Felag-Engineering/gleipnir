package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// googleLike returns the exact SchemaFeatureSet the Google wire declares:
// oneOf/anyOf/const are eliminable by the shared pass, everything else is
// fully supported.
func googleLike() SchemaFeatureSet {
	return SchemaFeatureSet{
		OneOf:   false,
		AnyOf:   false,
		Const:   false,
		AllOf:   true,
		Not:     true,
		Defs:    true,
		Formats: true,
	}
}

// mustMarshalSchema marshals v (typically a map[string]any/[]any literal
// describing a test schema) to JSON, failing the test on error.
func mustMarshalSchema(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling test schema: %v", err)
	}
	return b
}

// assertRewritten calls TranslateForFeatures and asserts the two invariants
// every rewritten case must satisfy: no error, and lossy == true. It decodes
// the result with json.Decoder.UseNumber (matching TranslateForFeatures's own
// decode) so callers can assert on numeric literals without float64 rounding,
// and returns the decoded tree for further assertions.
func assertRewritten(t *testing.T, in json.RawMessage, features SchemaFeatureSet) map[string]any {
	t.Helper()
	out, lossy, err := TranslateForFeatures(in, features)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !lossy {
		t.Error("lossy = false, want true")
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("unmarshalling output %s: %v", out, err)
	}
	return m
}

// properties is a small helper for reaching into a decoded schema's
// "properties" map, failing the test if the shape is wrong.
func properties(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	props, ok := m["properties"].(map[string]any)
	if !ok {
		t.Fatalf("m[\"properties\"] = %#v, want map[string]any", m["properties"])
	}
	return props
}

func TestSimplify_DiscriminatedOneOf_FlattensToEnum(t *testing.T) {
	t.Run("two object variants tagged by kind const", func(t *testing.T) {
		in := mustMarshalSchema(t, map[string]any{
			"type": "object",
			"oneOf": []any{
				map[string]any{
					"type":       "object",
					"properties": map[string]any{"kind": map[string]any{"const": "a"}, "x": map[string]any{"type": "string"}},
					"required":   []any{"kind", "x"},
				},
				map[string]any{
					"type":       "object",
					"properties": map[string]any{"kind": map[string]any{"const": "b"}, "y": map[string]any{"type": "integer"}},
					"required":   []any{"kind", "y"},
				},
			},
		})
		m := assertRewritten(t, in, googleLike())

		if _, present := m["oneOf"]; present {
			t.Error(`m["oneOf"] still present, want deleted`)
		}
		props := properties(t, m)
		wantKind := map[string]any{"type": "string", "enum": []any{"a", "b"}}
		if got := props["kind"]; !mapsEqual(got, wantKind) {
			t.Errorf(`properties["kind"] = %#v, want %#v`, got, wantKind)
		}
		if _, ok := props["x"]; !ok {
			t.Error(`properties["x"] missing`)
		}
		if _, ok := props["y"]; !ok {
			t.Error(`properties["y"] missing`)
		}
		if got, want := m["required"], []any{"kind"}; !mapsEqual(got, want) {
			t.Errorf("required = %#v, want %#v", got, want)
		}
		wantLead := `Exactly one of the following variants applies, selected by "kind"; do not mix properties from different variants.`
		if desc, _ := m["description"].(string); !strings.HasPrefix(desc, wantLead) {
			t.Errorf("description = %q, want prefix %q", desc, wantLead)
		}
	})

	t.Run("tags already folded as enum", func(t *testing.T) {
		in := mustMarshalSchema(t, map[string]any{
			"type": "object",
			"oneOf": []any{
				map[string]any{
					"type":       "object",
					"properties": map[string]any{"kind": map[string]any{"enum": []any{"a"}}, "x": map[string]any{"type": "string"}},
				},
				map[string]any{
					"type":       "object",
					"properties": map[string]any{"kind": map[string]any{"enum": []any{"b"}}, "y": map[string]any{"type": "integer"}},
				},
			},
		})
		m := assertRewritten(t, in, googleLike())

		props := properties(t, m)
		wantKind := map[string]any{"type": "string", "enum": []any{"a", "b"}}
		if got := props["kind"]; !mapsEqual(got, wantKind) {
			t.Errorf(`properties["kind"] = %#v, want %#v`, got, wantKind)
		}
	})

	t.Run("three variants, one with a two-value enum tag", func(t *testing.T) {
		in := mustMarshalSchema(t, map[string]any{
			"oneOf": []any{
				map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "a"}}},
				map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "b"}}},
				map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"enum": []any{"b", "c"}}}},
			},
		})
		m := assertRewritten(t, in, googleLike())

		props := properties(t, m)
		wantKind := map[string]any{"type": "string", "enum": []any{"a", "b", "c"}}
		if got := props["kind"]; !mapsEqual(got, wantKind) {
			t.Errorf(`properties["kind"] = %#v, want %#v (union, first-appearance order, de-duplicated)`, got, wantKind)
		}
	})

	t.Run("variants disagree on tag property name", func(t *testing.T) {
		in := mustMarshalSchema(t, map[string]any{
			"oneOf": []any{
				map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "a"}}},
				map[string]any{"type": "object", "properties": map[string]any{"type": map[string]any{"const": "b"}}},
			},
		})
		m := assertRewritten(t, in, googleLike())

		props := properties(t, m)
		// No discriminator: each variant's tagged property survives verbatim
		// (folded to enum form by the const pass) rather than being replaced
		// by a synthesized discriminator entry.
		if got := props["kind"]; !mapsEqual(got, map[string]any{"type": "string", "enum": []any{"a"}}) {
			t.Errorf(`properties["kind"] = %#v, want the unmodified (folded) variant declaration`, got)
		}
		if desc, _ := m["description"].(string); !strings.HasPrefix(desc, "Exactly one of the following variants applies; do not mix") {
			t.Errorf("description = %q, want the non-discriminated lead (no property named)", desc)
		}
	})

	t.Run("non-string tag values disqualify the discriminator", func(t *testing.T) {
		in := mustMarshalSchema(t, map[string]any{
			"oneOf": []any{
				map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": json.Number("1")}}},
				map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "b"}}},
			},
		})
		m := assertRewritten(t, in, googleLike())

		if desc, _ := m["description"].(string); !strings.HasPrefix(desc, "Exactly one of the following variants applies; do not mix") {
			t.Errorf("description = %q, want the non-discriminated lead", desc)
		}
		// No discriminator was found (a numeric tag disqualifies "kind"), so
		// each variant's own (const-folded) declaration survives verbatim
		// via first-wins — variant 1's numeric const folds to an integer
		// enum, and it is the first variant to declare "kind".
		props := properties(t, m)
		wantKind := map[string]any{"type": "integer", "enum": []any{json.Number("1")}}
		if got := props["kind"]; !mapsEqual(got, wantKind) {
			t.Errorf(`properties["kind"] = %#v, want %#v (first variant's folded declaration, not a synthesized discriminator)`, got, wantKind)
		}
	})

	t.Run("tag property missing from one variant", func(t *testing.T) {
		in := mustMarshalSchema(t, map[string]any{
			"oneOf": []any{
				map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "a"}, "x": map[string]any{"type": "string"}}},
				map[string]any{"type": "object", "properties": map[string]any{"y": map[string]any{"type": "string"}}},
			},
		})
		m := assertRewritten(t, in, googleLike())

		if desc, _ := m["description"].(string); !strings.HasPrefix(desc, "Exactly one of the following variants applies; do not mix") {
			t.Errorf("description = %q, want the non-discriminated lead", desc)
		}
	})

	t.Run("pure scalar variants fold to enum with no prose", func(t *testing.T) {
		in := json.RawMessage(`{"oneOf":[{"const":"a"},{"const":"b"}]}`)
		m := assertRewritten(t, in, googleLike())

		if got, want := m["type"], "string"; got != want {
			t.Errorf("type = %v, want %v", got, want)
		}
		if got, want := m["enum"], []any{"a", "b"}; !mapsEqual(got, want) {
			t.Errorf("enum = %#v, want %#v", got, want)
		}
		if _, present := m["description"]; present {
			t.Error(`description present, want none (enum is exact, needs no prose)`)
		}
	})

	// R2: a parent that directly declares the discriminator's property name
	// must still be overridden by the exact discriminator entry — the one
	// case where "parent wins" does not apply.
	t.Run("parent directly declares the discriminator property", func(t *testing.T) {
		in := mustMarshalSchema(t, map[string]any{
			"type":       "object",
			"properties": map[string]any{"kind": map[string]any{"type": "string", "description": "the parent's own kind"}},
			"oneOf": []any{
				map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "a"}, "x": map[string]any{"type": "string"}}},
				map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "b"}, "y": map[string]any{"type": "integer"}}},
			},
		})
		m := assertRewritten(t, in, googleLike())

		props := properties(t, m)
		want := map[string]any{"type": "string", "enum": []any{"a", "b"}, "description": "the parent's own kind"}
		if got := props["kind"]; !mapsEqual(got, want) {
			t.Errorf(`properties["kind"] = %#v, want %#v (discriminator overrides the parent's own declaration, keeping its description)`, got, want)
		}
		// Finding 3A: the parent declares only "kind" as its own property, so
		// "x" and "y" — contributed only by variants — are intersected out of
		// the merged superset rather than re-injected past whatever a
		// policy's params scoping narrowed the schema to (ADR-017).
		if _, ok := props["x"]; ok {
			t.Error(`properties["x"] present, want stripped (parent declares only "kind")`)
		}
		if _, ok := props["y"]; ok {
			t.Error(`properties["y"] present, want stripped (parent declares only "kind")`)
		}
	})
}

func TestSimplify_NonDiscriminatedOneOf_PermissiveUnion(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"type":        "object",
		"description": "Existing node description.",
		"required":    []any{"z"},
		"properties":  map[string]any{"z": map[string]any{"type": "string"}},
		"oneOf": []any{
			map[string]any{"type": "object", "description": "first variant desc", "required": []any{"x"}},
			map[string]any{"type": "object", "properties": map[string]any{"b": map[string]any{"type": "string"}, "a": map[string]any{"type": "string"}}, "required": []any{"x", "b"}},
			map[string]any{"type": "object", "required": []any{"x"}},
			map[string]any{},
		},
	})
	m := assertRewritten(t, in, googleLike())

	props := properties(t, m)
	if _, ok := props["z"]; !ok {
		t.Error(`properties["z"] missing (the parent's own property)`)
	}
	// Finding 3A: the parent declares its own "properties" (just "z"), so
	// "a" and "b" — contributed only by variant 2 — are intersected out of
	// the merged superset rather than re-injected past whatever a policy's
	// params scoping narrowed the schema to (ADR-017).
	for _, name := range []string{"a", "b"} {
		if _, ok := props[name]; ok {
			t.Errorf("properties[%q] present, want stripped (parent declares its own properties)", name)
		}
	}

	// required = parent's own ("z") union the intersection across variants
	// ("x", present in every variant that declares "required" at all — the
	// fourth variant declares none, but that only affects the intersection
	// if it disagrees, and an absent "required" means "requires nothing",
	// so "x" must NOT survive here).
	wantRequired := []any{"z"}
	if got := m["required"]; !mapsEqual(got, wantRequired) {
		t.Errorf("required = %#v, want %#v (variant 4 declares no required, so the intersection is empty)", got, wantRequired)
	}

	wantDescription := "Existing node description.\n\n" +
		"Exactly one of the following variants applies; do not mix properties from different variants.\n" +
		"- Variant 1: first variant desc\n" +
		"- Variant 2: properties: a, b\n" +
		"- Variant 3: type: object\n" +
		"- Variant 4: any value"
	if got := m["description"]; got != wantDescription {
		t.Errorf("description =\n%q\nwant\n%q", got, wantDescription)
	}
}

// TestSimplify_PropertyNameCollision_FirstWins covers both collision rules
// documented on mergeProperties: the parent's own entry always wins over
// every variant, and among variants the first to declare a name wins. Under
// Finding 3A (see mergeProperties's doc comment), any name that survives
// intersection with a scoped parent's own declared set is, by construction,
// already one of the parent's own entries — so "earlier variant wins over
// later" can only be observed when the parent declares no properties of its
// own at all. The two subtests isolate each rule accordingly.
func TestSimplify_PropertyNameCollision_FirstWins(t *testing.T) {
	variants := []any{
		map[string]any{
			"type":       "object",
			"properties": map[string]any{"shared": map[string]any{"type": "integer"}, "count": map[string]any{"type": "integer"}, "a": map[string]any{"type": "string"}},
		},
		map[string]any{
			"type":       "object",
			"properties": map[string]any{"shared": map[string]any{"type": "boolean"}, "count": map[string]any{"type": "boolean"}, "b": map[string]any{"type": "string"}},
		},
	}

	t.Run("no parent scoping: earlier variant wins over later", func(t *testing.T) {
		in := mustMarshalSchema(t, map[string]any{"type": "object", "oneOf": variants})
		m := assertRewritten(t, in, googleLike())

		props := properties(t, m)
		wantShared := map[string]any{"type": "integer"}
		if got := props["shared"]; !mapsEqual(got, wantShared) {
			t.Errorf(`properties["shared"] = %#v, want %#v (first variant wins over the second)`, got, wantShared)
		}
		wantCount := map[string]any{"type": "integer"}
		if got := props["count"]; !mapsEqual(got, wantCount) {
			t.Errorf(`properties["count"] = %#v, want %#v (first variant wins over the second)`, got, wantCount)
		}
		if _, ok := props["a"]; !ok {
			t.Error(`properties["a"] missing`)
		}
		if _, ok := props["b"]; !ok {
			t.Error(`properties["b"] missing`)
		}
	})

	t.Run("parent scoping: parent's own entry wins, variant-only names intersected out", func(t *testing.T) {
		in := mustMarshalSchema(t, map[string]any{
			"type":       "object",
			"properties": map[string]any{"shared": map[string]any{"type": "string", "description": "parent's shared"}},
			"oneOf":      variants,
		})
		m := assertRewritten(t, in, googleLike())

		props := properties(t, m)
		wantShared := map[string]any{"type": "string", "description": "parent's shared"}
		if got := props["shared"]; !mapsEqual(got, wantShared) {
			t.Errorf(`properties["shared"] = %#v, want %#v (parent wins over both variants)`, got, wantShared)
		}
		// Finding 3A: the parent declares only "shared" as its own property,
		// so "count", "a", and "b" — contributed only by variants — are
		// intersected out of the merged superset (ADR-017).
		for _, name := range []string{"count", "a", "b"} {
			if _, ok := props[name]; ok {
				t.Errorf("properties[%q] present, want stripped (parent declares only \"shared\")", name)
			}
		}
	})
}

func TestSimplify_DegenerateVariants(t *testing.T) {
	cases := []struct {
		name string
		in   json.RawMessage
	}{
		{"empty array", json.RawMessage(`{"oneOf":[]}`)},
		{"not an array", json.RawMessage(`{"oneOf":{}}`)},
		{"only boolean schemas", json.RawMessage(`{"oneOf":[true,false]}`)},
		{"single variant merges with no prose", json.RawMessage(`{"oneOf":[{"type":"object","properties":{"a":{"type":"string"}}}]}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := assertRewritten(t, tc.in, googleLike())
			if _, present := m["oneOf"]; present {
				t.Error(`m["oneOf"] still present, want deleted`)
			}
			if _, present := m["description"]; present {
				t.Errorf(`description = %#v, want none for a degenerate/single-variant collapse`, m["description"])
			}
		})
	}
}

func TestSimplify_MixedTypeVariants_FirstVariantFallback(t *testing.T) {
	in := json.RawMessage(`{"oneOf":[{"type":"string"},{"type":"array","items":{"type":"integer"}}]}`)
	m := assertRewritten(t, in, googleLike())

	if got, want := m["type"], "string"; got != want {
		t.Errorf(`type = %v, want %v (variant 1's shape)`, got, want)
	}
	if _, present := m["items"]; present {
		t.Error(`m["items"] present, want absent (only variant 1's top-level keys are copied)`)
	}
	wantDescription := "Exactly one of the following variants applies; do not mix properties from different variants." +
		" Only variant 1's shape is shown.\n" +
		"- Variant 1: type: string\n" +
		"- Variant 2: type: array"
	if got := m["description"]; got != wantDescription {
		t.Errorf("description =\n%q\nwant\n%q", got, wantDescription)
	}
}

func TestSimplify_AnyOf(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"anyOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
			map[string]any{"type": "object", "properties": map[string]any{"b": map[string]any{"type": "string"}}},
		},
	})
	m := assertRewritten(t, in, googleLike())

	if _, present := m["anyOf"]; present {
		t.Error(`m["anyOf"] still present, want deleted`)
	}
	props := properties(t, m)
	if _, ok := props["a"]; !ok {
		t.Error(`properties["a"] missing`)
	}
	if _, ok := props["b"]; !ok {
		t.Error(`properties["b"] missing`)
	}
	wantDescription := "At least one of the following variants applies.\n" +
		"- Variant 1: properties: a\n" +
		"- Variant 2: properties: b"
	if got := m["description"]; got != wantDescription {
		t.Errorf("description =\n%q\nwant\n%q", got, wantDescription)
	}
}

// R3: a single node carrying BOTH oneOf and anyOf simultaneously (legal JSON
// Schema). The fixed collapse order (oneOf first, mutating m, then anyOf
// merged against the already-mutated m) must produce one coherent union
// rather than either branch clobbering the other.
func TestSimplify_OneOfAndAnyOf_BothPresent(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}},
			map[string]any{"type": "object", "properties": map[string]any{"b": map[string]any{"type": "string"}}},
		},
		"anyOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"c": map[string]any{"type": "string"}}},
		},
	})
	m := assertRewritten(t, in, googleLike())

	if _, present := m["oneOf"]; present {
		t.Error(`m["oneOf"] still present, want deleted`)
	}
	if _, present := m["anyOf"]; present {
		t.Error(`m["anyOf"] still present, want deleted`)
	}
	if got, want := m["type"], "object"; got != want {
		t.Errorf("type = %v, want %v", got, want)
	}
	props := properties(t, m)
	for _, name := range []string{"a", "b", "c"} {
		if _, ok := props[name]; !ok {
			t.Errorf("properties[%q] missing — anyOf's single variant must merge into the already-collapsed oneOf union", name)
		}
	}
	// The anyOf collapse has only one variant, so it contributes no prose of
	// its own — the description is exactly the oneOf collapse's prose.
	wantDescription := "Exactly one of the following variants applies; do not mix properties from different variants.\n" +
		"- Variant 1: properties: a\n" +
		"- Variant 2: properties: b"
	if got := m["description"]; got != wantDescription {
		t.Errorf("description =\n%q\nwant\n%q", got, wantDescription)
	}
}

func TestSimplify_ConstFolds(t *testing.T) {
	cases := []struct {
		name string
		in   json.RawMessage
		want string
	}{
		{"string const, no type", json.RawMessage(`{"const":"x"}`), `{"enum":["x"],"type":"string"}`},
		{"integer const, type preserved", json.RawMessage(`{"type":"integer","const":3}`), `{"enum":[3],"type":"integer"}`},
		{"bare integer const inferred", json.RawMessage(`{"const":3}`), `{"enum":[3],"type":"integer"}`},
		{"bare number const inferred", json.RawMessage(`{"const":1.5}`), `{"enum":[1.5],"type":"number"}`},
		{"bare boolean const inferred", json.RawMessage(`{"const":true}`), `{"enum":[true],"type":"boolean"}`},
		{"existing enum overwritten", json.RawMessage(`{"type":"string","enum":["old"],"const":"new"}`), `{"enum":["new"],"type":"string"}`},
		{"json.Number verbatim: trailing zero preserved", json.RawMessage(`{"const":1.500}`), `{"enum":[1.500],"type":"number"}`},
		{"json.Number verbatim: large exponent preserved", json.RawMessage(`{"const":1e400}`), `{"enum":[1e400],"type":"number"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, lossy, err := TranslateForFeatures(tc.in, withoutConst())
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if !lossy {
				t.Error("lossy = false, want true")
			}
			if got := string(out); got != tc.want {
				t.Errorf("out = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestSimplify_RecursionPositions is the anti-drift test: simplifySchema must
// walk every position firstUnsupported walks. Each row plants a collapsible
// oneOf at one recursion position and asserts it is gone from the output.
func TestSimplify_RecursionPositions(t *testing.T) {
	collapsible := func() map[string]any {
		return map[string]any{"oneOf": []any{
			map[string]any{"const": "a"},
			map[string]any{"const": "b"},
		}}
	}

	cases := []struct {
		name   string
		schema map[string]any
	}{
		{"properties", map[string]any{"type": "object", "properties": map[string]any{"x": collapsible()}}},
		{"patternProperties", map[string]any{"type": "object", "patternProperties": map[string]any{"^x$": collapsible()}}},
		{"$defs", map[string]any{"$defs": map[string]any{"T": collapsible()}}},
		{"definitions", map[string]any{"definitions": map[string]any{"T": collapsible()}}},
		{"dependentSchemas", map[string]any{"dependentSchemas": map[string]any{"a": collapsible()}}},
		{"dependencies schema form", map[string]any{"dependencies": map[string]any{"a": collapsible()}}},
		{"additionalProperties", map[string]any{"type": "object", "additionalProperties": collapsible()}},
		{"unevaluatedProperties", map[string]any{"unevaluatedProperties": collapsible()}},
		{"unevaluatedItems", map[string]any{"unevaluatedItems": collapsible()}},
		{"additionalItems", map[string]any{"additionalItems": collapsible()}},
		{"items map form", map[string]any{"type": "array", "items": collapsible()}},
		{"items tuple form", map[string]any{"type": "array", "items": []any{collapsible()}}},
		{"prefixItems", map[string]any{"prefixItems": []any{collapsible()}}},
		{"contains", map[string]any{"contains": collapsible()}},
		{"propertyNames", map[string]any{"propertyNames": collapsible()}},
		{"if", map[string]any{"if": collapsible()}},
		{"then", map[string]any{"then": collapsible()}},
		{"else", map[string]any{"else": collapsible()}},
		{"not", map[string]any{"not": collapsible()}},
		{"contentSchema", map[string]any{"contentSchema": collapsible()}},
		{"allOf", map[string]any{"allOf": []any{collapsible()}}},
		{"anyOf", map[string]any{"anyOf": []any{collapsible()}}},
		{"oneOf", map[string]any{"oneOf": []any{collapsible()}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := mustMarshalSchema(t, tc.schema)
			out, lossy, err := TranslateForFeatures(in, googleLike())
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if !lossy {
				t.Error("lossy = false, want true")
			}
			if strings.Contains(string(out), `"oneOf"`) {
				t.Errorf("output still contains \"oneOf\": %s", out)
			}
		})
	}
}

func TestSimplify_NoOp_BytePassthrough(t *testing.T) {
	cases := []struct {
		name string
		in   json.RawMessage
	}{
		{"plain object", json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)},
		{"enum", json.RawMessage(`{"type":"string","enum":["a","b","c"]}`)},
		{"array of string", json.RawMessage(`{"type":"array","items":{"type":"string"}}`)},
		{
			"property literally named oneOf",
			json.RawMessage(`{"type":"object","properties":{"oneOf":{"type":"boolean"}}}`),
		},
		{
			"dependencies array-of-property-names form (not a subschema)",
			json.RawMessage(`{"type":"object","dependencies":{"a":["b","c"]}}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, lossy, err := TranslateForFeatures(tc.in, googleLike())
			assertPassthrough(t, tc.in, out, lossy, err)
		})
	}
}

// TestSimplify_SurvivingViolationStillRejected proves the gate is the last
// word: a construct simplifySchema does eliminate (oneOf) sits alongside one
// it does not (here, $ref — Defs overridden to false on top of googleLike()),
// and the surviving violation still fails closed even though the oneOf was
// successfully collapsed first.
func TestSimplify_SurvivingViolationStillRejected(t *testing.T) {
	f := googleLike()
	f.Defs = false

	in := json.RawMessage(`{"type":"object","oneOf":[{"const":"a"},{"const":"b"}],` +
		`"properties":{"ref":{"$ref":"#/$defs/T"}}}`)
	out, lossy, err := TranslateForFeatures(in, f)
	if !errors.Is(err, ErrUnsupportedSchemaFeature) {
		t.Fatalf("err = %v, want wrapping ErrUnsupportedSchemaFeature", err)
	}
	if lossy {
		t.Error("lossy = true, want false")
	}
	if !bytes.Equal(out, in) {
		t.Errorf("out = %s, want unchanged input %s", out, in)
	}
}

func TestSimplify_Deterministic(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "a"}, "x": map[string]any{"type": "string"}}},
			map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "b"}, "y": map[string]any{"type": "integer"}}},
		},
	})

	var first json.RawMessage
	for i := 0; i < 20; i++ {
		out, lossy, err := TranslateForFeatures(in, googleLike())
		if err != nil {
			t.Fatalf("iteration %d: err = %v, want nil", i, err)
		}
		if !lossy {
			t.Fatalf("iteration %d: lossy = false, want true", i)
		}
		if i == 0 {
			first = out
			continue
		}
		if !bytes.Equal(out, first) {
			t.Fatalf("iteration %d: out = %s, want byte-identical to iteration 0's %s", i, out, first)
		}
	}
}

func TestSimplify_NestedOneOfInsideVariant(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"oneOf": []any{
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"inner": map[string]any{"oneOf": []any{
						map[string]any{"const": "x"},
						map[string]any{"const": "y"},
					}},
				},
			},
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"other": map[string]any{"type": "string"}},
			},
		},
	})
	m := assertRewritten(t, in, googleLike())

	props := properties(t, m)
	wantInner := map[string]any{"type": "string", "enum": []any{"x", "y"}}
	if got := props["inner"]; !mapsEqual(got, wantInner) {
		t.Errorf(`properties["inner"] = %#v, want %#v (nested oneOf collapsed bottom-up before the parent merge)`, got, wantInner)
	}
	if _, ok := props["other"]; !ok {
		t.Error(`properties["other"] missing`)
	}
}

// TestSimplify_LimitsRejectOversizedInput proves the non-disableable bounds
// added alongside BLOCKER 1's fix (checkSchemaInputSize/checkSchemaShape,
// wired into TranslateForFeatures in schema_translate.go) each fail closed:
// canonical is returned unchanged, lossy is false, and the error wraps
// ErrSchemaLimitExceeded — the same fail-closed contract
// ErrUnsupportedSchemaFeature already has.
func TestSimplify_LimitsRejectOversizedInput(t *testing.T) {
	assertLimitRejected := func(t *testing.T, in json.RawMessage) {
		t.Helper()
		out, lossy, err := TranslateForFeatures(in, googleLike())
		if !errors.Is(err, ErrSchemaLimitExceeded) {
			t.Fatalf("err = %v, want wrapping ErrSchemaLimitExceeded", err)
		}
		if lossy {
			t.Error("lossy = true, want false")
		}
		if !bytes.Equal(out, in) {
			t.Error("out changed, want unchanged input on a limit violation")
		}
	}

	t.Run("input byte size", func(t *testing.T) {
		// Content doesn't matter — checkSchemaInputSize runs before any JSON
		// decoding, so even this invalid-JSON blob is rejected on length
		// alone rather than falling through to a parse error.
		huge := bytes.Repeat([]byte("a"), maxSchemaInputBytes+1)
		assertLimitRejected(t, huge)
	})

	t.Run("nesting depth", func(t *testing.T) {
		// A chain of maxSchemaDepth+1 nested "properties" wrappers. Node
		// count and byte size both stay tiny — only depth is exceeded.
		schema := any(map[string]any{"type": "string"})
		for i := 0; i <= maxSchemaDepth; i++ {
			schema = map[string]any{"type": "object", "properties": map[string]any{"x": schema}}
		}
		assertLimitRejected(t, mustMarshalSchema(t, schema))
	})

	t.Run("node count", func(t *testing.T) {
		// maxSchemaNodes+1 sibling properties, each a trivial leaf schema:
		// shallow (depth stays tiny), well under 1 MiB, but well over the
		// node-count bound.
		props := make(map[string]any, maxSchemaNodes+1)
		for i := 0; i <= maxSchemaNodes; i++ {
			props[fmt.Sprintf("p%d", i)] = map[string]any{"type": "string"}
		}
		assertLimitRejected(t, mustMarshalSchema(t, map[string]any{"type": "object", "properties": props}))
	})
}

// TestCheckSchemaOutputSize unit-tests the post-marshal output cap directly
// at its boundary. Driving this bound through the full TranslateForFeatures
// pipeline would require an input that survives checkSchemaInputSize (<=1
// MiB) and checkSchemaShape (<=64 deep, <=10,000 nodes) yet still simplifies
// to over 4 MiB of output — structurally difficult to construct now that
// BLOCKER 1's description-doubling is fixed and growth is linear, which is
// itself evidence the fix works. checkSchemaOutputSize's own boundary
// behavior is still worth pinning directly.
func TestCheckSchemaOutputSize(t *testing.T) {
	if err := checkSchemaOutputSize(make(json.RawMessage, maxSchemaOutputBytes)); err != nil {
		t.Errorf("err = %v, want nil at exactly the limit", err)
	}
	err := checkSchemaOutputSize(make(json.RawMessage, maxSchemaOutputBytes+1))
	if !errors.Is(err, ErrSchemaLimitExceeded) {
		t.Errorf("err = %v, want wrapping ErrSchemaLimitExceeded", err)
	}
}

// TestSimplify_DescriptionDoesNotDoubleAcrossNesting is BLOCKER 1's
// regression test. Before the fix, mergeFirstVariant copied variant[0]'s
// "description" into m AND appendDescription's prose bullet re-embedded that
// same text via describeVariant(objVariants[0]) — doubling it. Because
// collapseBranch runs bottom-up, chaining that fallback merge (each level's
// variant 0 is the previous level's already-collapsed, already-doubled node)
// compounded the doubling into exponential growth: the security review
// measured a 20-level chain exploding from ~667 bytes of input to 188.7 MB of
// output. With the fix, each level's description embeds the previous level's
// full text exactly ONCE (via the prose bullet only), so growth is linear in
// nesting depth.
func TestSimplify_DescriptionDoesNotDoubleAcrossNesting(t *testing.T) {
	const nestingDepth = 20 // matches the security review's worst-measured depth

	leaf := map[string]any{"type": "string", "description": "leaf"}
	current := any(leaf)
	for i := 0; i < nestingDepth; i++ {
		// Each level is a mixed-type oneOf (string vs boolean), routing
		// through mergeFirstVariant at every level, just as a real deeply
		// nested anyOf/oneOf from an OpenAPI generator would.
		current = map[string]any{
			"oneOf": []any{
				current,
				map[string]any{"type": "boolean"},
			},
		}
	}
	in := mustMarshalSchema(t, current)

	out, lossy, err := TranslateForFeatures(in, googleLike())
	if err != nil {
		t.Fatalf("err = %v, want nil (a %d-level mixed-type oneOf chain must stay within the fixed schema-shape limits)", err, nestingDepth)
	}
	if !lossy {
		t.Error("lossy = false, want true")
	}
	const wantMaxBytes = 1 << 15 // 32 KiB: generous for 20 linear levels; the pre-fix bug produced 188.7 MB at this depth
	if len(out) > wantMaxBytes {
		t.Errorf("len(out) = %d, want <= %d (description doubling has returned)", len(out), wantMaxBytes)
	}
	if !strings.Contains(string(out), "leaf") {
		t.Error(`out does not contain "leaf" at all — the innermost description was lost, not just de-duplicated`)
	}
}

// TestSimplify_LargeVariantCount_DedupStaysLinear is BLOCKER 2's regression
// test. Before the fix, both dedup call sites (findDiscriminator's
// object-variant path and flattenTagValues's scalar-variant path) compared
// every candidate tag value against every already-kept value by marshalling
// BOTH sides on each comparison — O(N^2) full JSON encodes for N variants.
// The security review measured 2,000 variants at 807ms and 8,000 at 15.4s,
// held per tool per LLM request inside the agent loop's concurrency-limited
// run path. Both cases here use a variant count picked to sit comfortably
// under maxSchemaNodes (so the fixed BLOCKER 1 limits do not themselves
// reject the input) while being large enough that the old O(N^2) behavior
// would make this test obviously, not just marginally, slow.
func TestSimplify_LargeVariantCount_DedupStaysLinear(t *testing.T) {
	const uniqueTags = 600

	cases := []struct {
		name           string
		variantCount   int // kept well under maxSchemaNodes at this shape's nodes/variant
		buildVariant   func(i int) map[string]any
		wantMaxElapsed time.Duration
		readEnum       func(t *testing.T, m map[string]any) []any
	}{
		{
			name:         "discriminated object variants (findDiscriminator)",
			variantCount: 2400, // ~4 nodes/variant -> ~9600 nodes, under the 10,000 limit
			buildVariant: func(i int) map[string]any {
				return map[string]any{"properties": map[string]any{"kind": map[string]any{"const": fmt.Sprintf("tag%d", i%uniqueTags)}}}
			},
			wantMaxElapsed: 500 * time.Millisecond,
			readEnum: func(t *testing.T, m map[string]any) []any {
				t.Helper()
				props := properties(t, m)
				kind, ok := props["kind"].(map[string]any)
				if !ok {
					t.Fatalf(`properties["kind"] = %#v, want map[string]any`, props["kind"])
				}
				enum, _ := kind["enum"].([]any)
				return enum
			},
		},
		{
			name:         "scalar tag variants (flattenTagValues)",
			variantCount: 3200, // ~3 nodes/variant -> ~9600 nodes, under the 10,000 limit
			buildVariant: func(i int) map[string]any {
				return map[string]any{"type": "string", "const": fmt.Sprintf("tag%d", i%uniqueTags)}
			},
			wantMaxElapsed: 700 * time.Millisecond,
			readEnum: func(t *testing.T, m map[string]any) []any {
				t.Helper()
				enum, _ := m["enum"].([]any)
				return enum
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			variants := make([]any, tc.variantCount)
			for i := range variants {
				variants[i] = tc.buildVariant(i)
			}
			in := mustMarshalSchema(t, map[string]any{"oneOf": variants})

			start := time.Now()
			m := assertRewritten(t, in, googleLike())
			elapsed := time.Since(start)

			if elapsed > tc.wantMaxElapsed {
				t.Errorf("elapsed = %v, want <= %v (dedup has gone quadratic again)", elapsed, tc.wantMaxElapsed)
			}

			enum := tc.readEnum(t, m)
			if len(enum) != uniqueTags {
				t.Errorf("len(enum) = %d, want %d (deduplicated, first-appearance order)", len(enum), uniqueTags)
			}
			for i, v := range enum {
				want := fmt.Sprintf("tag%d", i)
				if v != want {
					t.Errorf("enum[%d] = %q, want %q", i, v, want)
				}
			}
		})
	}
}

// TestSimplify_ParentProperties_IntersectsOutVariantOnlyNames is Finding 3
// Case A's regression test: mcp.NarrowSchema (ADR-017 parameter scoping)
// filters only the top-level properties/required of the canonical schema and
// leaves oneOf/anyOf variants untouched, so — mirroring the security review's
// own params:{path} example — a parent scoped down to {"path"} must not have
// "force"/"mode" re-injected by the union merge just because some variant
// happens to declare them.
func TestSimplify_ParentProperties_IntersectsOutVariantOnlyNames(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "force": map[string]any{"type": "boolean"}}},
			map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string"}}},
		},
	})
	m := assertRewritten(t, in, googleLike())

	props := properties(t, m)
	if _, ok := props["path"]; !ok {
		t.Error(`properties["path"] missing (the parent's own property)`)
	}
	for _, name := range []string{"force", "mode"} {
		if _, ok := props[name]; ok {
			t.Errorf("properties[%q] present, want stripped (parent declares only \"path\")", name)
		}
	}
}

// TestSimplify_DiscriminatorDoesNotWidenParentEnum is Finding 4's regression
// test: the discriminator override is documented as "the single exception to
// parent wins", but it must narrow to what the parent already allows, not
// widen it. A parent property {"enum":["read"]} must not become
// {"enum":["read","delete"]} just because one oneOf variant tags "delete".
func TestSimplify_DiscriminatorDoesNotWidenParentEnum(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"type":       "object",
		"properties": map[string]any{"mode": map[string]any{"type": "string", "enum": []any{"read"}}},
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "read"}, "x": map[string]any{"type": "string"}}},
			map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "delete"}, "y": map[string]any{"type": "string"}}},
		},
	})
	m := assertRewritten(t, in, googleLike())

	props := properties(t, m)
	wantMode := map[string]any{"type": "string", "enum": []any{"read"}}
	if got := props["mode"]; !mapsEqual(got, wantMode) {
		t.Errorf(`properties["mode"] = %#v, want %#v (discriminator intersected with the parent's own enum, not widened to include "delete")`, got, wantMode)
	}
}

// TestSimplify_DiscriminatorWithoutParentEnum_AppliesInFull proves the common
// case is unaffected by Finding 4's fix: when the parent declares the
// discriminator's property name without an "enum" (or does not declare it at
// all), there is nothing to intersect against, so the derived enum applies
// exactly as it did before the fix.
func TestSimplify_DiscriminatorWithoutParentEnum_AppliesInFull(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"type":       "object",
		"properties": map[string]any{"mode": map[string]any{"type": "string"}},
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "read"}}},
			map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "delete"}}},
		},
	})
	m := assertRewritten(t, in, googleLike())

	props := properties(t, m)
	wantMode := map[string]any{"type": "string", "enum": []any{"read", "delete"}}
	if got := props["mode"]; !mapsEqual(got, wantMode) {
		t.Errorf(`properties["mode"] = %#v, want %#v (no parent enum to intersect against)`, got, wantMode)
	}
}

// TestSimplify_Required_DropsNameStrippedFromProperties is Finding 5's
// regression test: both variants require "force", but the parent scopes its
// own properties to {"path"}, so Finding 3A already strips "force" from the
// merged properties. Requiring a name with no properties entry would make
// the tool permanently uninvokable once dispatch-time enforcement (which
// checks the schema of record, not this pass's presentation copy) rejects
// any call containing "force".
func TestSimplify_Required_DropsNameStrippedFromProperties(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"oneOf": []any{
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}, "force": map[string]any{"type": "boolean"}},
				"required":   []any{"force"},
			},
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}, "force": map[string]any{"type": "boolean"}},
				"required":   []any{"force"},
			},
		},
	})
	m := assertRewritten(t, in, googleLike())

	if got, present := m["required"]; present {
		t.Errorf(`required = %#v, want absent ("force" was stripped from properties, so it cannot remain required)`, got)
	}
	props := properties(t, m)
	if _, ok := props["force"]; ok {
		t.Error(`properties["force"] present, want stripped`)
	}
}

// TestSimplify_Required_SurvivesWhenPropertyPresent proves Finding 5's fix is
// a narrow filter, not a blanket rejection of "required": a name that
// legitimately survives into the merged properties (here, unscoped — the
// parent declares no properties of its own) still ends up required.
func TestSimplify_Required_SurvivesWhenPropertyPresent(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"type": "object",
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"force": map[string]any{"type": "boolean"}}, "required": []any{"force"}},
			map[string]any{"type": "object", "properties": map[string]any{"force": map[string]any{"type": "boolean"}}, "required": []any{"force"}},
		},
	})
	m := assertRewritten(t, in, googleLike())

	if got, want := m["required"], []any{"force"}; !mapsEqual(got, want) {
		t.Errorf("required = %#v, want %#v", got, want)
	}
}

// TestSimplify_DiscriminatorExcludedByScope_NotInjected is round-2 Finding
// 1's regression test (the HIGH blocker): mergeProperties's Finding-3A scope
// intersection was never applied to the discriminator entry
// mergeObjectVariants synthesizes separately, so a discriminated property a
// policy's params scoping excluded (ADR-017) was still shown, and REQUIRED,
// to the model — mirroring the security review's own {path, mode} PoC. The
// discriminator name ("mode") must be neither injected into "properties" nor
// into "required", and must not be named in the prose ("selected by ...").
func TestSimplify_DiscriminatorExcludedByScope_NotInjected(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "read"}}, "required": []any{"mode"}},
			map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "delete"}}, "required": []any{"mode"}},
		},
	})
	m := assertRewritten(t, in, googleLike())

	props := properties(t, m)
	if _, ok := props["path"]; !ok {
		t.Error(`properties["path"] missing (the parent's own property)`)
	}
	if _, ok := props["mode"]; ok {
		t.Error(`properties["mode"] present, want stripped (parent declares only "path"; the discriminator must not bypass Finding-3A scoping)`)
	}
	if got, present := m["required"]; present {
		t.Errorf(`required = %#v, want absent ("mode" is out of scope, so it cannot be required either)`, got)
	}
	// The prose must not name "mode" as a discriminator ("selected by
	// \"mode\""; variantLabel must not quote its per-variant tag values
	// either) — describeVariant's own per-variant "properties: ..." summary
	// is a separate, already-covered concern (TestSimplify_NonDiscriminatedOneOf_PermissiveUnion
	// shows a stripped name can still appear there) and is not what this
	// finding is about.
	desc, _ := m["description"].(string)
	if strings.Contains(desc, "selected by") {
		t.Errorf("description = %q, want no discriminator naming (the property is out of scope)", desc)
	}
	wantLead := "Exactly one of the following variants applies; do not mix properties from different variants."
	if !strings.HasPrefix(desc, wantLead) {
		t.Errorf("description = %q, want the non-discriminated lead %q", desc, wantLead)
	}
}

// TestSimplify_MergeFirstVariant_DropsRequiredWithoutMatchingProperty is
// round-2 Finding 2's regression test: mergeFirstVariant copies variant[0]'s
// "required" into m verbatim whenever m does not already declare one, with no
// check against what ends up in m["properties"] — unlike Finding 5's filter
// on the mergeObjectVariants path. Here the parent scopes its own properties
// to {"path"}, and the fallback variant (a propertyless "type":"array")
// declares "required":["force"], which must not survive: it would make the
// tool permanently uninvokable once dispatch-time enforcement rejects any
// call containing "force".
func TestSimplify_MergeFirstVariant_DropsRequiredWithoutMatchingProperty(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"oneOf": []any{
			map[string]any{"type": "array", "required": []any{"force"}},
			map[string]any{"type": "string"},
		},
	})
	m := assertRewritten(t, in, googleLike())

	if got, present := m["required"]; present {
		t.Errorf(`required = %#v, want absent ("force" has no corresponding properties entry)`, got)
	}
	props := properties(t, m)
	if _, ok := props["force"]; ok {
		t.Error(`properties["force"] present, want absent`)
	}
	if _, ok := props["path"]; !ok {
		t.Error(`properties["path"] missing (the parent's own property)`)
	}
}

// TestSimplify_MergeRequired_DeletesStaleParentRequired is round-2 Finding
// 3's regression test: mergeRequired can compute an empty result (every
// candidate name filtered out because it has no corresponding entry in the
// merged properties), but the assignment `if len(req) > 0 { m["required"] =
// req }` never runs the ELSE branch — so m's own pre-existing "required",
// already folded into mergeRequired's starting set, survives on m completely
// unfiltered. Here the parent declares "required":["force"] but scopes its
// own properties to {"path"}, and neither oneOf variant requires anything, so
// the merge result is empty and the stale "required" must be deleted.
func TestSimplify_MergeRequired_DeletesStaleParentRequired(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
		"required":   []any{"force"},
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
			map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		},
	})
	m := assertRewritten(t, in, googleLike())

	if got, present := m["required"]; present {
		t.Errorf(`required = %#v, want absent (the parent's stale "force" was filtered out and must not survive)`, got)
	}
}

// TestSimplify_DiscriminatorEmptyIntersection_LeavesParentPropertyUntouched
// is round-2 Finding 4's regression test: when the parent's own declared
// enum for the discriminator property shares no value with any variant's
// tag, the narrowed intersection is empty — and an empty "enum" is not "no
// constraint" to the Google wire (internal/llm/google/schema.go drops an
// empty enum, presenting an UNCONSTRAINED string instead). The fix must
// leave the parent's own declared property entry untouched rather than
// overriding it with an empty-enum schema. Two reachable shapes are covered,
// per the finding: a single branch keyword whose variants are all disjoint
// from the parent's enum, and two branch keywords (oneOf then anyOf)
// discriminating on the same property name where only the SECOND is
// disjoint.
func TestSimplify_DiscriminatorEmptyIntersection_LeavesParentPropertyUntouched(t *testing.T) {
	t.Run("single branch keyword, all variant tags disjoint from parent enum", func(t *testing.T) {
		in := mustMarshalSchema(t, map[string]any{
			"type":       "object",
			"properties": map[string]any{"mode": map[string]any{"type": "string", "enum": []any{"execute"}}},
			"oneOf": []any{
				map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "read"}}},
				map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "delete"}}},
			},
		})
		m := assertRewritten(t, in, googleLike())

		props := properties(t, m)
		wantMode := map[string]any{"type": "string", "enum": []any{"execute"}}
		if got := props["mode"]; !mapsEqual(got, wantMode) {
			t.Errorf(`properties["mode"] = %#v, want %#v (parent's own entry left untouched, not overridden with an empty enum)`, got, wantMode)
		}
	})

	t.Run("oneOf leaves the property untouched, anyOf's tags are disjoint from the parent enum", func(t *testing.T) {
		in := mustMarshalSchema(t, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"mode": map[string]any{"type": "string", "enum": []any{"read"}},
			},
			"oneOf": []any{
				// Discriminates on unrelated names, so mode passes through
				// oneOf's collapse unchanged (still scoped out, since only
				// "mode" is in the parent's own declared properties).
				map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}},
				map[string]any{"type": "object", "properties": map[string]any{"y": map[string]any{"type": "string"}}},
			},
			"anyOf": []any{
				map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "delete"}}},
				map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "execute"}}},
			},
		})
		m := assertRewritten(t, in, googleLike())

		props := properties(t, m)
		wantMode := map[string]any{"type": "string", "enum": []any{"read"}}
		if got := props["mode"]; !mapsEqual(got, wantMode) {
			t.Errorf(`properties["mode"] = %#v, want %#v (anyOf's discriminator override must not widen past the parent's own enum)`, got, wantMode)
		}
	})
}

// TestSimplify_DiscriminatorPreservesOtherParentConstraints is round-2
// Finding 5's regression test: discriminatorSchema used to rebuild the
// property from scratch as {type, enum, description}, discarding any other
// constraint the parent had already declared on that name (a "pattern",
// "format", "minLength", ...). The fix starts from the parent's own
// declaration and only adds/narrows "enum".
func TestSimplify_DiscriminatorPreservesOtherParentConstraints(t *testing.T) {
	in := mustMarshalSchema(t, map[string]any{
		"type":       "object",
		"properties": map[string]any{"mode": map[string]any{"type": "string", "pattern": "^read$"}},
		"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "read"}}},
			map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "delete"}}},
		},
	})
	m := assertRewritten(t, in, googleLike())

	props := properties(t, m)
	wantMode := map[string]any{"type": "string", "pattern": "^read$", "enum": []any{"read", "delete"}}
	if got := props["mode"]; !mapsEqual(got, wantMode) {
		t.Errorf(`properties["mode"] = %#v, want %#v (discriminator adds/narrows "enum" but keeps the parent's other declared constraints)`, got, wantMode)
	}
}

// mapsEqual compares two decoded JSON values for structural equality. Every
// call site in this file compares a value assertRewritten decoded (numbers
// arrive as json.Number, but every value under test here is a string, bool,
// map, or slice of those) against a "want" literal built from plain Go values
// of the same kinds, so a direct reflect.DeepEqual is sufficient.
func mapsEqual(got, want any) bool {
	return reflect.DeepEqual(got, want)
}
