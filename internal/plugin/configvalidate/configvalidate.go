// Package configvalidate compiles JSON Schema from plugin manifest fields and
// validates config values against them. It is the shared validation layer for:
//
//   - audience entry config (#206 — this package)
//   - trigger binding config (#216)
//   - instance config forms (#241)
//
// Strictness (additionalProperties: false, required arrays, etc.) is the schema
// author's responsibility; the validator faithfully reports whatever the schema
// declares.
package configvalidate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"gopkg.in/yaml.v3"
)

// ErrNotChannelPlugin is returned by ForChannelAudience when the manifest does
// not declare a ChannelService or has no channel declarations.
var ErrNotChannelPlugin = errors.New("manifest does not declare a ChannelService")

// ErrEventKindNotFound is returned by ForTriggerBinding when the requested
// event kind is not declared in the manifest.
var ErrEventKindNotFound = errors.New("event kind not found in manifest")

// FieldError is a single field-level validation failure. Field is a
// dot-separated JSON path (e.g. "outer.inner"); empty string means the root
// value. Message is a human-readable description of the violation.
type FieldError struct {
	Field   string
	Message string
}

// Validator wraps a compiled *jsonschema.Schema. Compile once, Validate many.
type Validator struct {
	compiled *jsonschema.Schema
}

// ForChannelAudience returns a Validator for the per-audience-entry config
// schema declared in m.Channels[0].ConfigSchema.
//
// Returns ErrNotChannelPlugin if m.Services.Channel == "" or len(m.Channels)
// == 0 — plugins lacking a ChannelService cannot be used as audience entries.
func ForChannelAudience(m *sdkmanifest.Manifest) (*Validator, error) {
	if m.Services.Channel == "" || len(m.Channels) == 0 {
		return nil, ErrNotChannelPlugin
	}
	jsonBytes, err := nodeToJSON(m.Channels[0].ConfigSchema)
	if err != nil {
		return nil, fmt.Errorf("configvalidate: marshal channel config_schema: %w", err)
	}
	return cachedCompile("channel", jsonBytes)
}

// ForInstanceConfig returns a Validator for the per-instance config schema
// declared in m.ConfigSchema.
func ForInstanceConfig(m *sdkmanifest.Manifest) (*Validator, error) {
	jsonBytes, err := nodeToJSON(m.ConfigSchema)
	if err != nil {
		return nil, fmt.Errorf("configvalidate: marshal instance config_schema: %w", err)
	}
	return cachedCompile("instance", jsonBytes)
}

// ForTriggerBinding returns a Validator for the binding schema of the named
// event kind. It scans m.EventKinds linearly and returns ErrEventKindNotFound
// if no entry matches eventKind.
func ForTriggerBinding(m *sdkmanifest.Manifest, eventKind string) (*Validator, error) {
	for _, decl := range m.EventKinds {
		if decl.Kind != eventKind {
			continue
		}
		jsonBytes, err := nodeToJSON(decl.BindingSchema)
		if err != nil {
			return nil, fmt.Errorf("configvalidate: marshal binding_schema for %q: %w", eventKind, err)
		}
		return cachedCompile("trigger:"+eventKind, jsonBytes)
	}
	return nil, ErrEventKindNotFound
}

// ValidateChannelCapabilities checks that the notify/request toggles on an
// audience entry are consistent with what the plugin's manifest declares.
//
// Returns a []FieldError (never a wrapped error) so callers can merge these
// errors into the same envelope as config_schema errors from Validate.
// Returns nil when all checks pass.
func ValidateChannelCapabilities(m *sdkmanifest.Manifest, notify, request bool) []FieldError {
	if m.Services.Channel == "" || len(m.Channels) == 0 {
		return []FieldError{{
			Field:   "plugin_instance_id",
			Message: "plugin does not provide a ChannelService",
		}}
	}

	decl := m.Channels[0]
	var errs []FieldError
	if notify && !decl.ImplementsNotify {
		errs = append(errs, FieldError{
			Field:   "notify",
			Message: "plugin does not implement Notify",
		})
	}
	if request && !decl.ImplementsRequest {
		errs = append(errs, FieldError{
			Field:   "request",
			Message: "plugin does not implement Request",
		})
	}
	return errs
}

// Compile builds a Validator directly from raw JSON Schema bytes. Use the
// For* helpers when working with a manifest; use Compile only when you already
// have raw bytes.
func Compile(schemaBytes []byte) (*Validator, error) {
	return compile(schemaBytes)
}

// compile is the internal (non-cached) compiler.
func compile(schemaBytes []byte) (*Validator, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("configvalidate: parse schema JSON: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("mem://schema.json", doc); err != nil {
		return nil, fmt.Errorf("configvalidate: add schema resource: %w", err)
	}
	schema, err := c.Compile("mem://schema.json")
	if err != nil {
		return nil, fmt.Errorf("configvalidate: compile schema: %w", err)
	}
	return &Validator{compiled: schema}, nil
}

// Validate validates value against the compiled schema. Schema-validation
// failures are returned as ([]FieldError, nil). A non-nil error indicates a
// programming bug (nil schema, unexpected internal error).
func (v *Validator) Validate(value any) ([]FieldError, error) {
	if v.compiled == nil {
		return nil, errors.New("configvalidate: Validate called on nil schema")
	}
	err := v.compiled.Validate(value)
	if err == nil {
		return nil, nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return nil, fmt.Errorf("configvalidate: unexpected error type %T: %w", err, err)
	}
	var errs []FieldError
	flattenErrors(ve, &errs)
	return errs, nil
}

// flattenErrors recursively walks ve to reach leaves (ValidationErrors with no
// Causes). Leaves are translated into FieldErrors via two paths:
//
//   - Path A (kind-aware): *kind.Required and *kind.AdditionalProperties each
//     aggregate multiple property names into one leaf; we range their slices
//     directly, emitting one FieldError per name.
//   - Path B (InstanceLocation): all other leaf kinds; we join
//     InstanceLocation tokens with '.' to form the field path.
func flattenErrors(ve *jsonschema.ValidationError, out *[]FieldError) {
	if len(ve.Causes) > 0 {
		for _, cause := range ve.Causes {
			flattenErrors(cause, out)
		}
		return
	}
	// Leaf node: translate to FieldError(s).
	parentPath := joinPath(ve.InstanceLocation)

	switch ek := ve.ErrorKind.(type) {
	case *kind.Required:
		// Path A: range Missing; one FieldError per missing property name.
		for _, name := range ek.Missing {
			field := joinParentField(parentPath, name)
			*out = append(*out, FieldError{
				Field:   field,
				Message: "missing required field: " + name,
			})
		}
	case *kind.AdditionalProperties:
		// Path A: range Properties; one FieldError per unexpected property name.
		for _, name := range ek.Properties {
			field := joinParentField(parentPath, name)
			*out = append(*out, FieldError{
				Field:   field,
				Message: "unexpected field: " + name,
			})
		}
	default:
		// Path B: translate InstanceLocation to dot-separated path.
		*out = append(*out, FieldError{
			Field:   parentPath,
			Message: ve.Error(),
		})
	}
}

// joinPath converts a pre-split, RFC 6901-unescaped InstanceLocation token
// slice into a dot-separated field path. An empty slice returns "".
func joinPath(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	unescaped := make([]string, len(tokens))
	for i, tok := range tokens {
		unescaped[i] = rfc6901Unescape(tok)
	}
	return strings.Join(unescaped, ".")
}

// joinParentField appends name to parent with a '.' separator. When parent is
// empty (root), returns name alone.
func joinParentField(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

// rfc6901Unescape performs RFC 6901 token unescaping: ~1 → / and ~0 → ~.
// The library already pre-unescapes tokens in InstanceLocation, so this is
// a defensive belt-and-suspenders pass.
func rfc6901Unescape(tok string) string {
	tok = strings.ReplaceAll(tok, "~1", "/")
	tok = strings.ReplaceAll(tok, "~0", "~")
	return tok
}

// nodeToJSON converts a *yaml.Node to canonical JSON bytes suitable for
// feeding to the jsonschema compiler. Returns an error when the node cannot be
// marshalled.
//
// TODO: this mirrors canonicalSchemaBytes in internal/plugin/manifest/diff.go.
// Consolidate into a shared helper when a third caller appears.
func nodeToJSON(node *yaml.Node) ([]byte, error) {
	if node == nil {
		// nil schema → empty object schema (validates anything).
		return []byte(`{}`), nil
	}
	raw, err := yaml.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("yaml marshal: %w", err)
	}
	var tree any
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	out, err := json.Marshal(tree)
	if err != nil {
		return nil, fmt.Errorf("json marshal: %w", err)
	}
	return out, nil
}
