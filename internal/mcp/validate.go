package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// SchemaViolation is a single field-level failure from validating a tool
// call's arguments against a tool's canonical JSON Schema (spec §10 step 3).
// Field is a dot-separated instance path; "" means the violation applies to
// the root value itself (e.g. no branch of a root-level oneOf matched).
type SchemaViolation struct {
	Field   string
	Message string
}

// SchemaViolationError wraps the field-level violations produced by
// ArgValidator.Validate. Callers use errors.As to recover the structured
// detail; Error() renders one line combining all violations, since a tool
// call is a single unit of correction for the agent.
type SchemaViolationError struct {
	Violations []SchemaViolation
}

func (e *SchemaViolationError) Error() string {
	parts := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		field := v.Field
		if field == "" {
			field = "(root)"
		}
		parts[i] = fmt.Sprintf("%s: %s", field, v.Message)
	}
	return "arguments do not match the tool schema: " + strings.Join(parts, "; ")
}

// ArgValidator holds a *jsonschema.Schema compiled once from a tool's
// canonical schema (spec §10 step 3's "exact enforcement"). Construct with
// NewArgValidator at run start; call Validate per tool call.
type ArgValidator struct {
	compiled *jsonschema.Schema
}

// denyAllLoader rejects every external schema reference.
//
// jsonschema.NewCompiler() defaults its URLLoader to jsonschema.FileLoader{}
// (verified: roots.go newRoots()), so a compiler built without this override
// would let a stored schema containing "$ref":"file:///etc/passwd" cause a
// local file read at compile time -- confirmed empirically: without this
// loader, compiling that $ref produces "invalid character 'r' looking for
// beginning of value", i.e. it had already read /etc/passwd and tried to
// parse it as JSON. This is the requirement recorded at
// internal/mcp/registry.go's ResolvedTool.CanonicalSchema doc and
// docs/developer/mcp-realignment-spec.md §10.
//
// Standard metaschema URLs (https://json-schema.org/...) are still resolved:
// the library serves those from an embedded FS before consulting the
// configured loader, so pinning "$schema" continues to work.
//
// internal/plugin/configvalidate has its own copy of this type, for the sibling
// compiler that validates plugin manifest config schemas (issue #775). The two
// are duplicated rather than shared because internal/plugin must not grow an
// import edge to internal/mcp -- keep them in step.
type denyAllLoader struct{}

func (denyAllLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("mcp: external schema reference not permitted: %s", url)
}

// NewArgValidator compiles schema -- narrowed to params per ADR-017 first,
// see NarrowSchema -- into an ArgValidator for exact pre-dispatch argument
// enforcement. Narrowing before compiling avoids a deadlock: an ADR-017
// params block that omits a schema-required property would otherwise leave
// that property permanently both demanded (by the unnarrowed "required") and
// rejected (by the key-presence gate, which runs on the narrowed schema) --
// NarrowSchema filters "required" down to params-listed keys, removing it.
// With no params, NarrowSchema returns schema unchanged and the full
// contract is enforced. With params, the stored schema's contract is only
// PARTIALLY enforced for the params-scoped tool: NarrowSchema deliberately
// drops "required" entries for properties params excludes (see the
// anti-deadlock note above), so the compiled validator can no longer demand
// those properties even though the tool's own canonical schema does. This is
// the intended trade-off, not a gap to fix here.
//
// Returns an error if schema is empty, fails to narrow, fails to parse as
// JSON, exceeds the static $ref-expansion budget (see
// maxSchemaExpansionPaths), or fails to compile as a JSON Schema. Callers
// (see execution/agent's compileArgValidator) decide how to degrade on
// error -- this constructor never falls back on its own.
func NewArgValidator(schema json.RawMessage, params map[string]any) (*ArgValidator, error) {
	if len(bytes.TrimSpace(schema)) == 0 {
		return nil, fmt.Errorf("mcp: empty schema")
	}

	narrowed, err := NarrowSchema(schema, params)
	if err != nil {
		return nil, fmt.Errorf("narrowing schema: %w", err)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(narrowed))
	if err != nil {
		return nil, fmt.Errorf("parsing schema JSON: %w", err)
	}

	// Refuse before compiling, not after -- see maxSchemaExpansionPaths's
	// doc comment. Compile itself is fast (this bug is purely a
	// VALIDATION-time cost), so this check does not need to run after
	// Compile succeeds; running it first also means a rejected schema never
	// reaches the compiler at all.
	if err := checkSchemaExpansionBudget(doc); err != nil {
		return nil, fmt.Errorf("rejecting schema before compile: %w", err)
	}

	c := jsonschema.NewCompiler()
	c.UseLoader(denyAllLoader{}) // MANDATORY -- see denyAllLoader's doc comment.
	// Pin the draft explicitly: the library's own doc on DefaultDraft warns
	// its default will not stay the same over time, and a schema with no
	// "$schema" field would otherwise silently compile against whatever
	// that default becomes in a future dependency bump.
	c.DefaultDraft(jsonschema.Draft2020)

	if err := c.AddResource("mem://schema.json", doc); err != nil {
		return nil, fmt.Errorf("adding schema resource: %w", err)
	}
	compiled, err := c.Compile("mem://schema.json")
	if err != nil {
		return nil, fmt.Errorf("compiling schema: %w", err)
	}

	return &ArgValidator{compiled: compiled}, nil
}

// Validate checks input -- a tool call's arguments, exactly as they will be
// dispatched -- against the compiled schema. Numbers in input are expected to
// be float64, matching what encoding/json.Unmarshal produces for the agent's
// tool-call arguments (verified: float64(2) satisfies "type":"integer";
// float64(2.5) does not). Returns nil when input satisfies the schema.
//
// A schema-validation failure is always returned as *SchemaViolationError
// (use errors.As to recover it). Any other error indicates a programming bug
// -- the underlying library returned an error of an unexpected type -- and is
// wrapped rather than silently swallowed.
//
// Guards against a zero-value ArgValidator{} constructed outside this
// package (e.g. in a test): entry.argValidator != nil at the call site
// (agent.go) only checks the pointer is non-nil, not that it went through
// NewArgValidator, so a nil compiled field would otherwise panic inside the
// run goroutine. Mirrors the same guard in
// internal/plugin/configvalidate.Validator.Validate.
func (v *ArgValidator) Validate(input map[string]any) error {
	if v.compiled == nil {
		return fmt.Errorf("mcp: Validate called on an ArgValidator with no compiled schema")
	}
	err := v.compiled.Validate(input)
	if err == nil {
		return nil
	}

	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return fmt.Errorf("mcp: unexpected validation error type %T: %w", err, err)
	}

	var violations []SchemaViolation
	flattenViolations(ve, &violations)

	// jsonschema/v6 iterates the *instance* map when collecting
	// AdditionalProperties violations (validator.go: `for pname, pvalue :=
	// range obj`), and Go map iteration order is randomized -- so without
	// sorting, the rendered message (and any caller comparing it, including
	// the audit trail and the LLM) would see a different violation order on
	// every call for the same input.
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Field != violations[j].Field {
			return violations[i].Field < violations[j].Field
		}
		return violations[i].Message < violations[j].Message
	})

	return &SchemaViolationError{Violations: violations}
}

// flattenViolations recursively walks ve to reach leaves (ValidationErrors
// with no Causes) and translates each into one or more SchemaViolations.
// Mirrors internal/plugin/configvalidate.flattenErrors in shape -- not
// imported, because internal/mcp must not depend on internal/plugin.
//
//   - *kind.Required and *kind.AdditionalProperties each aggregate multiple
//     property names into one leaf; range their slices directly, emitting one
//     SchemaViolation per name.
//   - Every other leaf kind: use ve.Error() -- deliberately NOT
//     ErrorKind.LocalizedString(nil), which panics on a nil message.Printer --
//     and trim its "at '<pointer>': " prefix, since Field already carries
//     that location.
func flattenViolations(ve *jsonschema.ValidationError, out *[]SchemaViolation) {
	if len(ve.Causes) > 0 {
		for _, cause := range ve.Causes {
			flattenViolations(cause, out)
		}
		return
	}

	field := joinPath(ve.InstanceLocation)

	switch ek := ve.ErrorKind.(type) {
	case *kind.Required:
		for _, name := range ek.Missing {
			*out = append(*out, SchemaViolation{
				Field:   joinParentField(field, name),
				Message: "missing required field: " + name,
			})
		}
	case *kind.AdditionalProperties:
		for _, name := range ek.Properties {
			*out = append(*out, SchemaViolation{
				Field:   joinParentField(field, name),
				Message: "unexpected field: " + name,
			})
		}
	default:
		prefix := fmt.Sprintf("at '%s': ", instancePointer(ve.InstanceLocation))
		*out = append(*out, SchemaViolation{
			Field:   field,
			Message: strings.TrimPrefix(ve.Error(), prefix),
		})
	}
}

// joinPath converts a pre-split, RFC 6901-unescaped InstanceLocation token
// slice into a dot-separated field path. An empty slice (root) returns "".
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

// rfc6901Unescape performs RFC 6901 token unescaping: ~1 -> / and ~0 -> ~.
// The library already pre-unescapes tokens in InstanceLocation, so this is a
// defensive belt-and-suspenders pass.
func rfc6901Unescape(tok string) string {
	tok = strings.ReplaceAll(tok, "~1", "/")
	tok = strings.ReplaceAll(tok, "~0", "~")
	return tok
}

// instancePointer renders InstanceLocation tokens as an RFC 6901 JSON
// Pointer, matching the format jsonschema/v6 embeds in a leaf
// ValidationError's Error() ("at '<pointer>': <message>"), so
// flattenViolations can strip that prefix and keep only the message.
func instancePointer(tokens []string) string {
	var sb strings.Builder
	for _, tok := range tokens {
		sb.WriteByte('/')
		sb.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(tok))
	}
	return sb.String()
}
