package llm

// SchemaFeatureSet declares which JSON Schema constructs a ProviderWire can
// represent on the request wire when presenting a tool's InputSchema.
// TranslateForFeatures uses this declaration to decide whether a canonical
// schema can be forwarded as-is or must be simplified. Of the fields below,
// simplifySchema can actually eliminate OneOf, AnyOf, and Const when a wire
// declares them unsupported (discriminated oneOf/anyOf → enum, otherwise a
// permissive union with prose; const folds into a single-value enum); it has
// no rewrite for AllOf, Not, Defs, or Formats, so declaring any of those
// false still fails closed with ErrUnsupportedSchemaFeature.
//
// Positive polarity is deliberate: the zero value SchemaFeatureSet{} means
// "supports nothing", so a forgotten/unset declaration fails closed rather
// than silently forwarding a construct the wire cannot represent.
//
// Value-constraint keywords (minimum, maxLength, pattern, uniqueItems,
// additionalProperties, patternProperties, ...) are deliberately NOT modelled
// here: they are not what spec §10 asks the shared pass to rewrite. Add a
// field here only when a wire needs the shared pass to rewrite that keyword.
//
// READ THIS BEFORE TRUSTING THE lossy FLAG. Because this vocabulary is much
// narrower than what a wire may actually discard, lossy=false means ONLY
// "no MODELLED feature was rewritten". It does NOT mean "the wire will present
// the canonical schema faithfully". A wire is free to silently drop any
// unmodelled value constraint on its way out: the Google wire, for instance,
// builds its request from just type/description/enum/required/properties/items
// and discards everything else, so a schema whose only interesting keyword is
// "pattern" reports lossy=false while the model is shown an unconstrained
// string. That widening is tolerable only where enforcement is exact and
// independent of what the model saw — which is why the dispatch-time validator
// must never consult anything this pass produces.
//
// The type must remain comparable (no slices or maps) so IsFull can stay a
// struct equality check.
type SchemaFeatureSet struct {
	OneOf   bool // "oneOf" branching
	AnyOf   bool // "anyOf" branching
	AllOf   bool // "allOf" intersection (canonicalization preserves these; flattening happens here, not at discovery)
	Not     bool // "not" negation
	Defs    bool // "$defs" / "definitions" / "$ref" (inseparable: a $ref without $defs is meaningless)
	Const   bool // "const" (the shared pass folds this into a single-value enum)
	Formats bool // the "format" annotation keyword
}

// FullSchemaSupport returns a SchemaFeatureSet with every field true — the
// declaration for a wire that forwards the canonical schema unmodified.
func FullSchemaSupport() SchemaFeatureSet {
	return SchemaFeatureSet{
		OneOf:   true,
		AnyOf:   true,
		AllOf:   true,
		Not:     true,
		Defs:    true,
		Const:   true,
		Formats: true,
	}
}

// IsFull reports whether f declares support for every modelled feature.
func (f SchemaFeatureSet) IsFull() bool {
	return f == FullSchemaSupport()
}
