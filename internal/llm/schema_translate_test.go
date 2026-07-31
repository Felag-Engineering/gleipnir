package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// assertPassthrough checks the three invariants every passthrough case must
// satisfy: no error, lossy is false, and the returned bytes are the exact
// same backing array as the input (not merely equal content) — proving no
// re-marshal happened.
func assertPassthrough(t *testing.T, in json.RawMessage, out json.RawMessage, lossy bool, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if lossy {
		t.Error("lossy = true, want false")
	}
	if !bytes.Equal(out, in) {
		t.Errorf("out = %s, want %s", out, in)
	}
	if len(in) > 0 {
		if &out[0] != &in[0] {
			t.Error("out does not share the input's backing array — a re-marshal occurred")
		}
	}
}

func TestTranslateForFeatures_FullSupport_Passthrough(t *testing.T) {
	cases := []struct {
		name string
		in   json.RawMessage
	}{
		{"plain object", json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)},
		{
			"feature-rich",
			json.RawMessage(`{"type":"object","properties":{"target":{"oneOf":[{"type":"string"},{"$ref":"#/$defs/T"}]}},` +
				`"$defs":{"T":{"type":"string","format":"date-time","const":"fixed"}},"allOf":[{"type":"object"}]}`),
		},
		{"nil", json.RawMessage(nil)},
		{"empty slice", json.RawMessage{}},
		{"invalid JSON", json.RawMessage(`{not json`)},
		{"boolean schema", json.RawMessage(`true`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, lossy, err := TranslateForFeatures(tc.in, FullSchemaSupport())
			assertPassthrough(t, tc.in, out, lossy, err)
		})
	}
}

func TestTranslateForFeatures_Restricted_CompatibleSchemaPassesThrough(t *testing.T) {
	restricted := SchemaFeatureSet{}
	cases := []struct {
		name string
		in   json.RawMessage
	}{
		{
			"type/properties/required",
			json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}},"required":["name"]}`),
		},
		{"enum", json.RawMessage(`{"type":"string","enum":["a","b","c"]}`)},
		{"items", json.RawMessage(`{"type":"array","items":{"type":"string"}}`)},
		{
			"property literally named format",
			json.RawMessage(`{"type":"object","properties":{"format":{"type":"string","description":"the file format"}}}`),
		},
		{
			"property literally named oneOf",
			json.RawMessage(`{"type":"object","properties":{"oneOf":{"type":"boolean"}}}`),
		},
		{
			"dependencies array-of-property-names form (not a subschema)",
			json.RawMessage(`{"type":"object","dependencies":{"a":["b","c"]}}`),
		},
		{"additionalProperties boolean form", json.RawMessage(`{"type":"object","additionalProperties":false}`)},
		{"unevaluatedProperties boolean form", json.RawMessage(`{"type":"object","unevaluatedProperties":false}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, lossy, err := TranslateForFeatures(tc.in, restricted)
			assertPassthrough(t, tc.in, out, lossy, err)
		})
	}
}

func TestTranslateForFeatures_Restricted_UnsupportedKeyword(t *testing.T) {
	cases := []struct {
		name     string
		schema   json.RawMessage
		features SchemaFeatureSet
		wantKW   string
		wantPath string
	}{
		{"$ref", json.RawMessage(`{"$ref":"#/foo"}`), withoutDefs(), "$ref", "/$ref"},
		{"$dynamicRef", json.RawMessage(`{"$dynamicRef":"#foo"}`), withoutDefs(), "$dynamicRef", "/$dynamicRef"},
		{"$recursiveRef", json.RawMessage(`{"$recursiveRef":"#"}`), withoutDefs(), "$recursiveRef", "/$recursiveRef"},
		{"$defs", json.RawMessage(`{"$defs":{"T":{"type":"string"}}}`), withoutDefs(), "$defs", "/$defs"},
		{"definitions", json.RawMessage(`{"definitions":{"T":{"type":"string"}}}`), withoutDefs(), "definitions", "/definitions"},
		{"allOf", json.RawMessage(`{"allOf":[{"type":"string"}]}`), withoutAllOf(), "allOf", "/allOf"},
		{"anyOf", json.RawMessage(`{"anyOf":[{"type":"string"}]}`), withoutAnyOf(), "anyOf", "/anyOf"},
		{"oneOf", json.RawMessage(`{"oneOf":[{"type":"string"}]}`), withoutOneOf(), "oneOf", "/oneOf"},
		{"not", json.RawMessage(`{"not":{"type":"string"}}`), withoutNot(), "not", "/not"},
		{"const", json.RawMessage(`{"const":"fixed"}`), withoutConst(), "const", "/const"},
		{"format", json.RawMessage(`{"type":"string","format":"date-time"}`), withoutFormats(), "format", "/format"},
		{
			"nested under properties/items",
			json.RawMessage(`{"type":"object","properties":{"x":{"type":"array","items":{"oneOf":[{"type":"string"},{"type":"integer"}]}}}}`),
			withoutOneOf(),
			"oneOf",
			"/properties/x/items/oneOf",
		},
		{
			"nested under $defs",
			json.RawMessage(`{"$defs":{"T":{"type":"string","format":"date-time"}}}`),
			withoutFormats(),
			"format",
			"/$defs/T/format",
		},
		{
			"property name needing JSON Pointer escaping",
			json.RawMessage(`{"type":"object","properties":{"a/b~c":{"oneOf":[{"type":"string"}]}}}`),
			withoutOneOf(),
			"oneOf",
			"/properties/a~1b~0c/oneOf",
		},
		{
			"nested under dependentSchemas",
			json.RawMessage(`{"dependentSchemas":{"a":{"format":"date-time"}}}`),
			withoutFormats(),
			"format",
			"/dependentSchemas/a/format",
		},
		{
			"nested under dependencies schema form",
			json.RawMessage(`{"dependencies":{"a":{"format":"date-time"}}}`),
			withoutFormats(),
			"format",
			"/dependencies/a/format",
		},
		{
			"nested under unevaluatedProperties",
			json.RawMessage(`{"unevaluatedProperties":{"type":"string","format":"date-time"}}`),
			withoutFormats(),
			"format",
			"/unevaluatedProperties/format",
		},
		{
			"nested under unevaluatedItems",
			json.RawMessage(`{"unevaluatedItems":{"format":"date-time"}}`),
			withoutFormats(),
			"format",
			"/unevaluatedItems/format",
		},
		{
			"nested under additionalItems",
			json.RawMessage(`{"additionalItems":{"format":"date-time"}}`),
			withoutFormats(),
			"format",
			"/additionalItems/format",
		},
		{
			"nested under contentSchema",
			json.RawMessage(`{"contentSchema":{"format":"date-time"}}`),
			withoutFormats(),
			"format",
			"/contentSchema/format",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, lossy, err := TranslateForFeatures(tc.schema, tc.features)
			if !errors.Is(err, ErrUnsupportedSchemaFeature) {
				t.Fatalf("err = %v, want wrapping ErrUnsupportedSchemaFeature", err)
			}
			if !strings.Contains(err.Error(), tc.wantKW) {
				t.Errorf("error %q does not contain keyword %q", err, tc.wantKW)
			}
			if !strings.Contains(err.Error(), tc.wantPath) {
				t.Errorf("error %q does not contain path %q", err, tc.wantPath)
			}
			if lossy {
				t.Error("lossy = true, want false")
			}
			if !bytes.Equal(out, tc.schema) {
				t.Errorf("out = %s, want unchanged input %s", out, tc.schema)
			}
		})
	}
}

// TestTranslateForFeatures_Restricted_DeterministicFirstViolation proves the
// walker's determinism across Go's randomized map iteration: two sibling
// property violations must always report the alphabetically-first one
// ("apple" before "zebra"), every time.
func TestTranslateForFeatures_Restricted_DeterministicFirstViolation(t *testing.T) {
	features := FullSchemaSupport()
	features.Const = false
	features.Formats = false
	schema := json.RawMessage(`{"type":"object","properties":{"zebra":{"const":"z"},"apple":{"format":"date-time"}}}`)

	for i := 0; i < 20; i++ {
		_, _, err := TranslateForFeatures(schema, features)
		if !errors.Is(err, ErrUnsupportedSchemaFeature) {
			t.Fatalf("iteration %d: err = %v, want wrapping ErrUnsupportedSchemaFeature", i, err)
		}
		if !strings.Contains(err.Error(), `"format"`) || !strings.Contains(err.Error(), "/properties/apple/format") {
			t.Fatalf("iteration %d: err = %v, want keyword \"format\" at \"/properties/apple/format\"", i, err)
		}
	}
}

// TestFeatureGates_CoverAllSchemaFeatureSetFields guards against the
// gates-table/struct drift the security review flagged: a SchemaFeatureSet
// field with no corresponding featureGates entry would silently fail open —
// declaring that field false would never actually gate anything. For every
// bool field, it flips just that field off (relative to full support) and
// asserts at least one gate's allowed() result changes.
func TestFeatureGates_CoverAllSchemaFeatureSetFields(t *testing.T) {
	full := FullSchemaSupport()
	typ := reflect.TypeOf(full)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Type.Kind() != reflect.Bool {
			t.Fatalf("field %s is %s, want bool — SchemaFeatureSet must stay comparable", field.Name, field.Type.Kind())
		}
		t.Run(field.Name, func(t *testing.T) {
			restricted := full
			reflect.ValueOf(&restricted).Elem().Field(i).SetBool(false)

			for _, g := range featureGates {
				if g.allowed(full) != g.allowed(restricted) {
					return // covered: this gate's outcome depends on the field
				}
			}
			t.Errorf("SchemaFeatureSet field %q has no entry in featureGates — declaring it false would silently fail open", field.Name)
		})
	}
}

func TestTranslateForFeatures_Restricted_InvalidJSON(t *testing.T) {
	in := json.RawMessage(`{not json`)
	out, lossy, err := TranslateForFeatures(in, SchemaFeatureSet{})
	if err == nil {
		t.Fatal("err = nil, want a parse error")
	}
	if errors.Is(err, ErrUnsupportedSchemaFeature) {
		t.Error("err wraps ErrUnsupportedSchemaFeature, want a plain JSON parse error")
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Errorf("err = %v, want it to wrap a *json.SyntaxError", err)
	}
	if lossy {
		t.Error("lossy = true, want false")
	}
	if !bytes.Equal(out, in) {
		t.Errorf("out = %s, want unchanged input %s", out, in)
	}
}

// TestTranslateForFeatures_Restricted_TrailingData proves a well-formed JSON
// value followed by garbage is rejected rather than silently accepted with
// the garbage ignored — json.Decoder.Decode stops after the first value and
// does not check for trailing input on its own.
func TestTranslateForFeatures_Restricted_TrailingData(t *testing.T) {
	in := json.RawMessage(`{"type":"string"}garbage`)
	out, lossy, err := TranslateForFeatures(in, SchemaFeatureSet{})
	if err == nil {
		t.Fatal("err = nil, want an error rejecting trailing data")
	}
	if errors.Is(err, ErrUnsupportedSchemaFeature) {
		t.Error("err wraps ErrUnsupportedSchemaFeature, want a plain parse error")
	}
	if lossy {
		t.Error("lossy = true, want false")
	}
	if !bytes.Equal(out, in) {
		t.Errorf("out = %s, want unchanged input %s", out, in)
	}
}
