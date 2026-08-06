// Package configvalidate compiles JSON Schema from plugin manifest fields and
// validates config values against them. It is the shared validation layer for:
//
//   - audience entry config (#206 — this package)
//   - trigger binding config (#216)
//   - instance config forms (#241)
//   - instance subscription scope config (#223)
//
// Strictness (additionalProperties: false, required arrays, etc.) is the schema
// author's responsibility; the validator faithfully reports whatever the schema
// declares.
package configvalidate

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/felag-engineering/gleipnir/internal/plugin/schemautil"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
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
	jsonBytes, err := schemautil.ToJSON(m.Channels[0].ConfigSchema)
	if err != nil {
		return nil, fmt.Errorf("configvalidate: marshal channel config_schema: %w", err)
	}
	return cachedCompile("channel", jsonBytes)
}

// ForInstanceConfig returns a Validator for the per-instance config schema
// declared in m.ConfigSchema.
func ForInstanceConfig(m *sdkmanifest.Manifest) (*Validator, error) {
	jsonBytes, err := schemautil.ToJSON(m.ConfigSchema)
	if err != nil {
		return nil, fmt.Errorf("configvalidate: marshal instance config_schema: %w", err)
	}
	return cachedCompile("instance", jsonBytes)
}

// ForSubscriptionScope returns a Validator for the instance-level subscription
// scope schema declared in m.SubscriptionSchema. A nil SubscriptionSchema
// produces a validator that accepts any object (empty schema).
func ForSubscriptionScope(m *sdkmanifest.Manifest) (*Validator, error) {
	jsonBytes, err := schemautil.ToJSON(m.SubscriptionSchema)
	if err != nil {
		return nil, fmt.Errorf("configvalidate: marshal subscription_schema: %w", err)
	}
	return cachedCompile("subscription", jsonBytes)
}

// ForTriggerBinding returns a Validator for the binding schema of the named
// event kind. It scans m.EventKinds linearly and returns ErrEventKindNotFound
// if no entry matches eventKind.
func ForTriggerBinding(m *sdkmanifest.Manifest, eventKind string) (*Validator, error) {
	for _, decl := range m.EventKinds {
		if decl.Kind != eventKind {
			continue
		}
		jsonBytes, err := schemautil.ToJSON(decl.BindingSchema)
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

// denyAllLoader rejects every external schema reference (issue #775).
//
// jsonschema.NewCompiler() defaults its URLLoader to jsonschema.FileLoader{},
// so a compiler built without this override lets a plugin manifest's config
// schema cause a host-side local file READ at compile time. Three vectors all
// compile successfully without it -- and compiling successfully is itself the
// proof the file was read and parsed:
//
//	{"$ref":    "file:///path.json"}
//	{"$schema": "file:///path.json"}
//	{"$id": "file:///dir/x.json", "$ref": "relative.json"}
//
// Plugin manifests are Minisign-signed and TOFU-pinned (ADR-045), so this is
// not "any third party" -- but TOFU trusts the FIRST key unconditionally, and
// GLEIPNIR_ALLOW_UNSIGNED_PLUGINS is a documented escape hatch. A hostile
// first-install bundle, or any instance running that hatch, could otherwise
// read whatever the Gleipnir process can.
//
// This mirrors internal/mcp's denyAllLoader rather than importing it: package
// boundaries here are intentional (internal/plugin must not grow an import
// edge to internal/mcp), and ten lines duplicated is the cheaper of the two
// costs. Keep the two in step.
//
// Standard metaschema URLs (https://json-schema.org/...) still resolve: the
// library serves those from an embedded FS before consulting the configured
// loader, so pinning "$schema" keeps working.
type denyAllLoader struct{}

func (denyAllLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("configvalidate: external schema reference not permitted: %s", url)
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
	c.UseLoader(denyAllLoader{}) // MANDATORY -- see denyAllLoader's doc comment.
	// Pin the draft explicitly: the library's own doc on DefaultDraft warns its
	// default will not stay the same over time, and a schema with no "$schema"
	// would otherwise silently compile against whatever that becomes in a future
	// dependency bump. Here it also means the dialect cannot be steered by an
	// absent or attacker-chosen "$schema".
	c.DefaultDraft(jsonschema.Draft2020)

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
