// Package binding provides the host-side, runtime evaluation engine for
// plugin trigger bindings. A binding is a flat map of field→value pairs from
// the policy YAML (e.g. `binding: {channel: "#incidents", mention_only: true}`)
// that the host evaluates against every event payload received from the
// plugin's EmitEvent RPC. No per-match round-trip RPC to the plugin is needed.
//
// # Operators
//
// The operator applied to each field is determined by the field's schema shape,
// not by any wrapper key. The operator table for v1:
//
//	Field name == "mention_only" AND schema type == boolean → OpMentionOnly
//	Schema type:string, format:regex                        → OpRegex
//	Schema type:string, format:contains                     → OpContains
//	Schema type:string (no format)                          → OpEquals
//	Schema type:boolean or number or integer                → OpEquals
//	Schema format:glob                                      → ErrUnsupportedOperator
//	Any other / unknown combination                         → ErrUnsupportedOperator
//
// # Reserved field name: mention_only
//
// The field name "mention_only" is reserved by the v1 binding convention.
// Plugin authors MUST name their mention-only boolean field exactly
// "mention_only" for the OpMentionOnly semantic to apply. When the operator
// supplies `mention_only: true` in the binding, Evaluate checks the event
// payload field "mentioned" (boolean) rather than "mention_only". This
// indirection is specified in docs/developer/plugin-system-spec.md §7.2.
//
// # Evaluation semantics
//
// Evaluate applies an implicit AND across all compiled fields: every field
// must match for Evaluate to return true. If any field does not match — or if
// the corresponding payload field is absent or the wrong type — Evaluate
// returns (false, nil) without an error. The silent-no-fire policy is
// intentional: plugin payloads are external; crashing the evaluation loop on
// a type mismatch would be worse than silently skipping the event.
//
// # Relationship with configvalidate
//
// configvalidate.ForTriggerBinding is the save-time validator: it checks that
// the operator-supplied binding YAML is structurally valid according to the
// manifest's binding_schema. This package is the runtime evaluator: it is
// called on every EmitEvent to decide whether to fire the policy. They are
// complementary and not interchangeable.
//
// # Scope
//
// This package is a leaf: it imports only stdlib and gopkg.in/yaml.v3. It
// must not import any internal/* package.
package binding

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Operator identifies which matching rule applies to a compiled field.
type Operator int

const (
	// OpEquals requires deep equality between the binding value and the payload
	// field value after Go type coercion. Applies to string, bool, and numeric
	// schema types.
	OpEquals Operator = iota

	// OpContains requires that the payload field (string) contains the binding
	// value (string) as a substring.
	OpContains

	// OpRegex requires that the payload field (string) matches the binding
	// value interpreted as a Go RE2 regular expression.
	OpRegex

	// OpMentionOnly applies to the reserved field name "mention_only". When the
	// binding value is true, the payload must carry a truthy "mentioned" boolean
	// field. When the binding value is false, the field is a no-op (matches
	// everything).
	OpMentionOnly
)

// CompiledField holds the parsed and operator-resolved representation of a
// single binding field. Regex patterns are pre-compiled so that Evaluate is
// allocation-free.
type CompiledField struct {
	// Name is the field name as it appears in the binding / schema.
	Name string

	// Op is the operator to apply during evaluation.
	Op Operator

	// StrValue is set when the binding value was a string (OpEquals, OpContains,
	// OpRegex).
	StrValue string

	// BoolValue is set when the binding value was a boolean.
	BoolValue bool

	// HasBool is true when the binding value was a boolean (needed to
	// distinguish BoolValue==false from "not set").
	HasBool bool

	// FloatValue is set when the binding value was a number.
	FloatValue float64

	// HasFloat is true when the binding value was a number.
	HasFloat bool

	// Regex is the pre-compiled regular expression for OpRegex fields.
	Regex *regexp.Regexp
}

// CompiledBinding is the result of Compile. It is safe to use concurrently.
type CompiledBinding struct {
	fields []CompiledField
}

// Sentinel errors returned by Compile.
var (
	// ErrInvalidRegex is returned when a regex binding value is not a valid Go
	// RE2 expression. Note that ECMA regexes (e.g. lookbehinds like (?<=foo)bar)
	// are not supported — Go uses RE2, not PCRE or V8.
	ErrInvalidRegex = errors.New("binding: invalid regular expression (Go RE2 required)")

	// ErrUnknownField is returned when the binding map contains a key that does
	// not appear in the manifest's binding_schema properties.
	ErrUnknownField = errors.New("binding: unknown field")

	// ErrUnsupportedOperator is returned when the schema declares a format that
	// does not map to any v1 operator (e.g. format:glob).
	ErrUnsupportedOperator = errors.New("binding: unsupported operator")
)

// Compile resolves the operator for each key in binding using the field type
// and format declared in schema (a YAML mapping node shaped like the
// binding_schema from a manifest EventKindDecl). The returned CompiledBinding
// is immutable; call Evaluate once per event.
//
// Nil-schema guard:
//   - Compile(nil/empty-map, nil) → empty CompiledBinding (matches everything).
//   - Compile(non-empty-map, nil) → ErrUnknownField with the first key, because
//     there is no schema to validate the key against.
//
// Type-mismatch at compile time (e.g. binding supplies a bool for a string
// field) is returned as an error. Payload type mismatches at evaluation time
// are silent (see package doc).
func Compile(binding map[string]any, schema *yaml.Node) (*CompiledBinding, error) {
	if len(binding) == 0 {
		return &CompiledBinding{}, nil
	}

	// With a non-empty binding and no schema, every key is unknown.
	if schema == nil {
		for k := range binding {
			return nil, fmt.Errorf("%w: %q", ErrUnknownField, k)
		}
	}

	props := findMappingValue(schema, "properties")
	// props may be nil if the schema has no properties.

	cb := &CompiledBinding{}
	for key, rawVal := range binding {
		fieldSchema := findMappingValue(props, key)
		if fieldSchema == nil {
			return nil, fmt.Errorf("%w: %q", ErrUnknownField, key)
		}

		cf, err := compileField(key, rawVal, fieldSchema)
		if err != nil {
			return nil, err
		}
		cb.fields = append(cb.fields, cf)
	}
	return cb, nil
}

// compileField resolves the operator and validates the binding value type for
// a single field.
func compileField(name string, rawVal any, fieldSchema *yaml.Node) (CompiledField, error) {
	schemaType := mappingScalar(fieldSchema, "type")
	schemaFormat := mappingScalar(fieldSchema, "format")

	cf := CompiledField{Name: name}

	// Reserved field name: mention_only with boolean schema.
	if name == "mention_only" && schemaType == "boolean" {
		b, ok := rawVal.(bool)
		if !ok {
			return cf, fmt.Errorf("binding: field %q: expected bool, got %T", name, rawVal)
		}
		cf.Op = OpMentionOnly
		cf.BoolValue = b
		cf.HasBool = true
		return cf, nil
	}

	switch schemaType {
	case "string":
		switch schemaFormat {
		case "regex":
			s, ok := rawVal.(string)
			if !ok {
				return cf, fmt.Errorf("binding: field %q: expected string, got %T", name, rawVal)
			}
			re, err := regexp.Compile(s)
			if err != nil {
				return cf, fmt.Errorf("%w: field %q: %v", ErrInvalidRegex, name, err)
			}
			cf.Op = OpRegex
			cf.StrValue = s
			cf.Regex = re
		case "contains":
			s, ok := rawVal.(string)
			if !ok {
				return cf, fmt.Errorf("binding: field %q: expected string, got %T", name, rawVal)
			}
			cf.Op = OpContains
			cf.StrValue = s
		case "":
			s, ok := rawVal.(string)
			if !ok {
				return cf, fmt.Errorf("binding: field %q: expected string, got %T", name, rawVal)
			}
			cf.Op = OpEquals
			cf.StrValue = s
		default:
			return cf, fmt.Errorf("%w: field %q has format %q", ErrUnsupportedOperator, name, schemaFormat)
		}

	case "boolean":
		b, ok := rawVal.(bool)
		if !ok {
			return cf, fmt.Errorf("binding: field %q: expected bool, got %T", name, rawVal)
		}
		cf.Op = OpEquals
		cf.BoolValue = b
		cf.HasBool = true

	case "number", "integer":
		// YAML numbers unmarshal as int or float64 depending on value.
		switch v := rawVal.(type) {
		case float64:
			cf.FloatValue = v
			cf.HasFloat = true
		case int:
			cf.FloatValue = float64(v)
			cf.HasFloat = true
		case int64:
			cf.FloatValue = float64(v)
			cf.HasFloat = true
		default:
			return cf, fmt.Errorf("binding: field %q: expected number, got %T", name, rawVal)
		}
		cf.Op = OpEquals

	default:
		return cf, fmt.Errorf("%w: field %q has unrecognised type %q", ErrUnsupportedOperator, name, schemaType)
	}

	return cf, nil
}

// Evaluate applies the compiled binding against payload. Returns (true, nil)
// when all fields match, (false, nil) otherwise. Never returns a non-nil error:
// type mismatches and missing payload fields are treated as non-matches.
func (cb *CompiledBinding) Evaluate(payload map[string]any) (bool, error) {
	for _, f := range cb.fields {
		if !evalField(f, payload) {
			return false, nil
		}
	}
	return true, nil
}

// evalField evaluates a single compiled field against the payload. Returns
// false on any mismatch, missing field, or type error (silent policy).
func evalField(f CompiledField, payload map[string]any) bool {
	switch f.Op {
	case OpMentionOnly:
		// When the binding value is false, mention_only is a no-op.
		if !f.BoolValue {
			return true
		}
		// Require payload["mentioned"] to be truthy boolean.
		v, ok := payload["mentioned"]
		if !ok {
			return false
		}
		b, ok := v.(bool)
		return ok && b

	case OpRegex:
		s, ok := payloadString(payload, f.Name)
		if !ok {
			return false
		}
		return f.Regex.MatchString(s)

	case OpContains:
		s, ok := payloadString(payload, f.Name)
		if !ok {
			return false
		}
		return strings.Contains(s, f.StrValue)

	case OpEquals:
		pv, exists := payload[f.Name]
		if !exists {
			return false
		}
		return valuesEqual(f, pv)
	}
	return false
}

// valuesEqual compares a compiled field's expected value against a payload
// value. Silent false on type mismatch.
func valuesEqual(f CompiledField, pv any) bool {
	if f.HasBool {
		b, ok := pv.(bool)
		return ok && b == f.BoolValue
	}
	if f.HasFloat {
		switch v := pv.(type) {
		case float64:
			return v == f.FloatValue
		case int:
			return float64(v) == f.FloatValue
		case int64:
			return float64(v) == f.FloatValue
		}
		return false
	}
	// String equals.
	s, ok := pv.(string)
	return ok && s == f.StrValue
}

// payloadString extracts a string field from the payload. Returns ("", false)
// if the field is absent or not a string.
func payloadString(payload map[string]any, key string) (string, bool) {
	v, ok := payload[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// findMappingValue searches a *yaml.MappingNode for the given key and returns
// its value node, or nil if not found. Returns nil for nil or non-mapping input.
func findMappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// mappingScalar returns the scalar string value of key in the mapping node n,
// or "" if not found or the value is not a scalar.
func mappingScalar(n *yaml.Node, key string) string {
	v := findMappingValue(n, key)
	if v == nil || v.Kind != yaml.ScalarNode {
		return ""
	}
	return v.Value
}
