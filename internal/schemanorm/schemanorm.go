// Package schemanorm performs byte-level normalization of JSON documents
// (in practice, JSON Schema tool-input documents): decode with UseNumber,
// enforce input bounds, and re-emit with object keys recursively sorted and
// HTML escaping disabled. That is the entire transformation.
//
// Normalize accepts any syntactically valid JSON document, not only a JSON
// object. A bare string, number, boolean, array, or null root all succeed
// and round-trip -- there is no root-shape gate requiring the document look
// like a JSON Schema (a JSON Schema in the strict sense is always an object
// or a boolean; this package does not check that, or anything else about
// what the document means, at any position including the root).
//
// # The safety argument
//
// This package does not resolve "$ref", does not strip "$defs", does not
// flatten "allOf", does not touch any identity keyword ("$id"/"$anchor"/
// etc.), and does not gate on JSON Schema dialect. Nothing is inlined,
// merged, wrapped, or relocated: the decoded value -- what the document
// means -- is unchanged. What IS re-serialized is the byte representation of
// that same value: member order is sorted, insignificant whitespace is
// removed, and string escape spelling is canonicalized (e.g. "\/" becomes
// "/", "A" becomes "A", a surrogate-pair escape becomes raw UTF-8, a raw
// U+2028 becomes its "\u2028" escape). All of that is value-preserving --
// two JSON texts that differ only in whitespace, escape spelling, or member
// order decode to the identical value -- but it is not merely "the byte
// order of object members changed", and a caller diffing raw bytes against
// normalized bytes should not conclude otherwise.
//
// That makes the safety argument a single line: JSON object members are
// unordered (RFC 8259 §4), and no JSON Schema keyword's meaning depends on
// member order, so reordering members cannot change what any validator
// accepts. Normalize(x) and x describe exactly the same schema.
//
// A prior version of this package attempted more: bounded "$ref" inlining
// and "allOf" merging, then (after that was found to widen what a schema
// accepts) a "never merge" redesign that substituted a bare "$ref" and
// wrapped a siblinged "$ref" in an "allOf" instead of merging keywords.
// Three independent security reviews each closed one widening mechanism and
// found a new one: keyword relocation across a schema-object boundary
// (e.g. "additionalProperties" moving relative to "properties"), then "$ref"
// mis-resolution ("$id" changing the base URI a pointer resolves against,
// percent-encoded pointer segments), then narrowing-through-wrapping not
// composing under negation ("not"/"if"/"oneOf") plus "$schema" being
// position-dependent dialect metadata that a relocation can silently change.
// The root cause in all three rounds was the same: JSON Schema has keywords
// whose meaning depends on WHERE THEY SIT, not just what they say, and any
// structural transformation moves something. This package now performs no
// structural transformation at all, which is what makes the safety argument
// above checkable in one line instead of needing a fourth security review.
//
// # The three sharp edges
//
// A naive decode-and-re-emit round trip through encoding/json DOES change
// meaning in exactly three ways. This package rejects all three rather than
// accommodating them -- silently repairing bad input would just move the
// same "byte-normalization changed what this document means" risk from
// "someone finds a fourth transformation bug" to "someone finds a case this
// repair gets wrong":
//
//  1. Duplicate keys. encoding/json's Decode is last-wins on a duplicate
//     object key, so {"a":1,"a":2} silently becomes {"a":2} -- a member
//     disappears. A validator with different duplicate-handling (or a human
//     reading the original bytes) could disagree with which member "the
//     schema" actually declares. Rejected with ErrDuplicateKey via a
//     token-level scan that visits every object at every depth, because a
//     plain Decode into a generic tree cannot detect this on its own -- the
//     duplicate is already gone by the time Decode returns.
//  2. Invalid UTF-8 / lone surrogates. encoding/json silently substitutes
//     both an invalid raw UTF-8 byte sequence and an unpaired "\uD800"-
//     "\uDFFF" surrogate escape with U+FFFD at decode time (verified
//     empirically; by the time a value is a Go string the original bytes are
//     already gone), changing the string's value. Rejected with
//     ErrInvalidUTF8, checked directly against the input bytes before any
//     JSON decoding happens -- the only point at which the original bytes
//     are still available to inspect.
//  3. Numeric re-rendering. Decoding a JSON number into float64 loses
//     precision and spelling (1.500 becomes 1.5; a 30-digit integer loses
//     digits; 1e400 overflows). This package decodes with json.Number
//     (UseNumber) instead, which retains the original literal text
//     verbatim through decode and re-emit -- see
//     TestNormalize_NumberLiteralsPreserved in schemanorm_test.go for the
//     regression rows (1e400, -0, 1.0, and a high-precision literal all
//     round-trip byte-for-byte).
//
// These three are the only ways a decode-and-re-emit round trip through
// encoding/json can change a document's meaning; there is no fourth
// sharp edge left for byte-level normalization to have, because there is no
// fourth thing this package does to the document beyond reordering members.
//
// # Bounds
//
// Limits bounds input size, recursion depth, and total value count. A zero
// or negative field is replaced by its Default* constant -- bounds cannot
// be disabled (fail-closed); there is no "unlimited" mode.
//
//   - MaxBytes caps len(raw), checked before any decoding.
//   - MaxDepth caps object/array nesting depth.
//   - MaxNodes caps the total number of JSON values in the document.
//
// Because this package never duplicates any part of the document (nothing
// is inlined or expanded), output size is bounded by roughly 2x input size
// -- not 1x. The unbounded amplification class that motivated a separate
// MaxOutputBytes bound in a prior, ref-resolving version of this package is
// structurally impossible here, so that bound has been removed, but the 2x
// (rather than 1x) constant factor is real, and it is encoding/json's own
// doing: Encoder.Encode always escapes U+2028 and U+2029 to a 6-byte
// "\u2028"/"\u2029" ASCII sequence regardless of SetEscapeHTML, because
// those two code points are valid JSON but invalid inside a <script> block
// in some JS engines. A string that is entirely U+2028 characters therefore
// doubles in size on the way through: 3 raw UTF-8 bytes become 6 ASCII bytes
// per character -- measured exactly 2.000x at 1 MiB (1,048,574 bytes in,
// 2,097,140 bytes out). This factor is a fixed constant, not a function of
// nesting or repetition, so it does not reopen the amplification class
// MaxOutputBytes existed to close; a caller sizing a buffer/column/quota off
// MaxBytes should size it at 2x MaxBytes to be safe.
//
// # Scope
//
// This package is a leaf: it imports only the Go standard library. It must
// not import any internal/* package.
//
// # Call site
//
// internal/mcp discovery (canonicalizeDiscovered in internal/mcp/canonical.go,
// used by both RefreshTools and ProbeTools) normalizes each freshly-discovered
// tool schema with this package and persists the result alongside the raw
// bytes in mcp_tools.canonical_schema. Drift detection then compares that
// canonical form so a schema change that is only cosmetic (member order) does
// not spuriously mark a tool as modified, falling back to raw byte comparison
// when either side has no canonical form stored. The call site is fail-open:
// a schema this package rejects is still discovered and stored, just with a
// NULL canonical_schema and a logged warning -- normalization failure never
// drops a tool or fails discovery. $ref resolution and allOf flattening for
// LLM-provider presentation (Google's function-declaration subset has
// neither) belong in a later per-provider translation step, not here -- see
// docs/developer/mcp-realignment-spec.md §10 step 2, where lossiness is the
// declared, accepted policy because exact enforcement runs separately
// against the untransformed, normalized schema (§10 step 3).
package schemanorm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// DefaultMaxBytes is the default Limits.MaxBytes, checked against len(raw)
// before any JSON decoding happens.
const DefaultMaxBytes = 1 << 20 // 1,048,576

// DefaultMaxDepth is the default Limits.MaxDepth: object/array nesting well
// beyond any real tool schema (which typically bottoms out around 4-15
// levels) while still terminating a pathologically deep document.
const DefaultMaxDepth = 64

// DefaultMaxNodes is the default Limits.MaxNodes: the total count of JSON
// values (objects, arrays, and scalars) a document may contain. 10,000
// values is roughly 100-400 KB serialised -- already unusable as an LLM
// tool schema, and orders of magnitude above any real one (typically
// 20-200 nodes).
const DefaultMaxNodes = 10000

// Limits bounds input size and recursion. A zero or negative field is
// replaced by its Default* constant; there is no way to disable a bound.
type Limits struct {
	// MaxBytes caps len(raw), checked before any JSON decoding.
	MaxBytes int
	// MaxDepth caps object/array nesting depth.
	MaxDepth int
	// MaxNodes caps the total number of JSON values in the document.
	MaxNodes int
}

// DefaultLimits returns the Limits Normalize uses.
func DefaultLimits() Limits {
	return Limits{
		MaxBytes: DefaultMaxBytes,
		MaxDepth: DefaultMaxDepth,
		MaxNodes: DefaultMaxNodes,
	}
}

// Normalize normalizes raw using DefaultLimits. See the package doc for
// what "normalized" means and NormalizeWithLimits for the full contract.
func Normalize(raw json.RawMessage) (json.RawMessage, error) {
	return NormalizeWithLimits(raw, DefaultLimits())
}

// NormalizeWithLimits decodes raw as JSON within lim's bounds and re-emits
// deterministic bytes: object keys recursively sorted, arrays kept in
// source order, numbers preserved verbatim (see the package doc). No
// keyword is interpreted, resolved, or moved.
//
// Normalize is a pure function and safe for concurrent use. The returned
// slice is always a freshly allocated buffer; it never aliases raw.
//
// Returns a *Error (see errors.go) classified by one of the ErrXxx
// sentinels; use errors.Is to check the kind and errors.As to recover the
// Pointer/Detail.
func NormalizeWithLimits(raw json.RawMessage, lim Limits) (json.RawMessage, error) {
	lim = normalizeLimits(lim)

	// MaxBytes is checked first, against the raw byte length, before any
	// decoding happens -- the only bound that protects against a single
	// oversized input.
	if len(raw) > lim.MaxBytes {
		return nil, newErr(ErrByteSizeExceeded, "", fmt.Sprintf("input is %d bytes, exceeds limit of %d", len(raw), lim.MaxBytes))
	}

	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, newErr(ErrInvalidJSON, "", "empty input")
	}

	// The two UTF-8 sharp-edge checks (see the package doc) must run
	// against raw bytes, before any JSON decoding: encoding/json silently
	// substitutes both problem classes with U+FFFD at decode time, so by
	// the time a value has become a Go string the information needed to
	// reject it is already gone.
	if err := checkValidUTF8(raw); err != nil {
		return nil, err
	}

	// Reject duplicate object keys and enforce MaxDepth/MaxNodes via a
	// token-level scan. A plain Decode into a generic tree cannot detect a
	// duplicate key -- encoding/json is silently last-wins (see the
	// package doc's "duplicate keys" sharp edge) -- so this pass runs
	// first and independently of the decode below.
	if err := validateStructure(raw, lim); err != nil {
		return nil, err
	}

	doc, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}

	return marshalDeterministic(doc)
}

// decodeJSON decodes raw into a generic tree (map[string]any / []any /
// string / bool / nil / json.Number). UseNumber is mandatory: without it,
// number literals round-trip through float64 and lose precision/spelling,
// breaking the "numeric re-rendering" sharp edge's guarantee.
func decodeJSON(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, newErr(ErrInvalidJSON, "", fmt.Sprintf("%v", err))
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, newErr(ErrInvalidJSON, "", "trailing content after top-level value")
	}
	return doc, nil
}

// marshalDeterministic marshals a decoded generic tree to compact JSON
// bytes. This package works exclusively on decoded generic trees and
// marshals exactly once here -- encoding/json sorts map[string]any keys
// byte-wise ascending and preserves array order on its own, which is what
// makes the output deterministic without a custom ordered-map type.
func marshalDeterministic(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// SetEscapeHTML(false) keeps description prose readable instead of
	// turning "<", ">", and "&" into backslash-u-escaped sequences; both
	// choices are idempotent, but this one avoids gratuitously mangling
	// stored schemas.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("schemanorm: marshal normalized output: %w", err)
	}
	// Encoder.Encode appends a trailing newline; trim it so callers get
	// exactly the compact JSON bytes.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// normalizeLimits substitutes the Default* constant for any zero/negative
// field. Bounds cannot be disabled.
func normalizeLimits(lim Limits) Limits {
	if lim.MaxBytes <= 0 {
		lim.MaxBytes = DefaultMaxBytes
	}
	if lim.MaxDepth <= 0 {
		lim.MaxDepth = DefaultMaxDepth
	}
	if lim.MaxNodes <= 0 {
		lim.MaxNodes = DefaultMaxNodes
	}
	return lim
}
