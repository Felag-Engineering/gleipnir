package schemanorm_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/schemanorm"
)

// TestNormalize_Positive is the main byte-comparison table. Every row is run
// through a shared harness asserting (1) got == want byte-for-byte and
// (2) idempotence: Normalize(got) == got byte-for-byte -- normalization
// applied twice must be a no-op, since the only thing this package changes
// is member order and there is only one sorted order.
func TestNormalize_Positive(t *testing.T) {
	for _, tc := range positiveRows() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := schemanorm.Normalize(json.RawMessage(tc.in))
			if err != nil {
				t.Fatalf("Normalize(%s): unexpected error: %v", tc.in, err)
			}
			if !bytes.Equal(got, []byte(tc.want)) {
				t.Fatalf("Normalize(%s) = %s, want %s", tc.in, got, tc.want)
			}

			again, err := schemanorm.Normalize(got)
			if err != nil {
				t.Fatalf("Normalize(Normalize(%s)): unexpected error: %v", tc.in, err)
			}
			if !bytes.Equal(again, got) {
				t.Fatalf("idempotence broken: Normalize(%s) = %s, want %s", got, again, got)
			}
		})
	}
}

type positiveCase struct {
	name string
	in   string
	want string
}

func positiveRows() []positiveCase {
	return []positiveCase{
		{
			name: "flat_object_already_sorted",
			in:   `{"a":1,"b":2}`,
			want: `{"a":1,"b":2}`,
		},
		{
			name: "flat_object_reordered",
			in:   `{"b":2,"a":1}`,
			want: `{"a":1,"b":2}`,
		},
		{
			name: "nested_object_reordered_at_every_level",
			in:   `{"b":2,"a":{"d":1,"c":2}}`,
			want: `{"a":{"c":2,"d":1},"b":2}`,
		},
		{
			name: "array_elements_keep_source_order_but_each_objects_keys_sort",
			in:   `{"z":[{"y":1,"x":2},{"b":1,"a":2}],"a":1}`,
			want: `{"a":1,"z":[{"x":2,"y":1},{"a":2,"b":1}]}`,
		},
		{
			name: "boolean_root_true",
			in:   `true`,
			want: `true`,
		},
		{
			name: "boolean_root_false",
			in:   `false`,
			want: `false`,
		},
		{
			// Normalize has no root-shape gate: a bare scalar, array, or
			// null succeeds even though none of these is a valid JSON
			// Schema on its own (a schema is always an object or a
			// boolean) -- see the package doc's note on this. These four
			// rows lock that scope decision in.
			name: "root_string_scalar",
			in:   `"hello"`,
			want: `"hello"`,
		},
		{
			name: "root_number_scalar",
			in:   `42`,
			want: `42`,
		},
		{
			name: "root_null",
			in:   `null`,
			want: `null`,
		},
		{
			name: "root_array",
			in:   `[3,1,2]`,
			want: `[3,1,2]`,
		},
		{
			name: "null_value_preserved",
			in:   `{"default":null}`,
			want: `{"default":null}`,
		},
		{
			name: "empty_object",
			in:   `{}`,
			want: `{}`,
		},
		{
			name: "empty_array",
			in:   `{"enum":[]}`,
			want: `{"enum":[]}`,
		},
		{
			name: "html_unsafe_characters_not_escaped",
			in:   `{"description":"a < b & c > d"}`,
			want: `{"description":"a < b & c > d"}`,
		},
		{
			name: "non_ascii_content_kept_as_raw_utf8",
			in:   `{"description":"café"}`,
			want: "{\"description\":\"café\"}",
		},
		{
			name: "valid_surrogate_pair_escape_survives",
			in:   `{"description":"😀"}`,
			want: "{\"description\":\"\U0001F600\"}",
		},
		{
			name: "vendor_keywords_untouched",
			in:   `{"x-gleipnir-options":{"source":"channels","multi":true},"x-gleipnir-secret":true}`,
			want: `{"x-gleipnir-options":{"multi":true,"source":"channels"},"x-gleipnir-secret":true}`,
		},
		{
			// This package no longer resolves $ref, strips $defs, or
			// flattens allOf -- see the package doc. This row locks that in:
			// the only change is member order.
			name: "ref_defs_and_allOf_left_completely_untouched",
			in:   `{"allOf":[{"$ref":"#/$defs/T"}],"$defs":{"T":{"type":"string"}}}`,
			want: `{"$defs":{"T":{"type":"string"}},"allOf":[{"$ref":"#/$defs/T"}]}`,
		},
		{
			name: "deeply_nested_well_inside_default_bounds",
			in:   `{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":1}}}}}}}}}}`,
			want: `{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":1}}}}}}}}}}`,
		},
	}
}

// TestNormalize_NumberLiteralsPreserved is the sharp-edge-3 regression
// table: json.Number retains the original literal text through decode and
// re-emit, so number spelling -- not just numeric value -- must round-trip
// byte-for-byte.
func TestNormalize_NumberLiteralsPreserved(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"trailing_zeros", `{"minimum":1.500}`},
		{"large_exponent_overflows_float64", `{"maximum":1e400}`},
		{"negative_zero", `{"default":-0}`},
		{"trailing_point_zero", `{"default":1.0}`},
		{"high_precision_integer", `{"default":123456789012345678901234567890}`},
		{"high_precision_decimal", `{"default":0.123456789012345678901234567890}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := schemanorm.Normalize(json.RawMessage(tc.in))
			if err != nil {
				t.Fatalf("Normalize(%s): unexpected error: %v", tc.in, err)
			}
			if !bytes.Equal(got, []byte(tc.in)) {
				t.Errorf("Normalize(%s) = %s, want the input unchanged (single-key object, already sorted)", tc.in, got)
			}
		})
	}
}

// TestNormalize_DuplicateKeyRejected is the sharp-edge-1 regression table.
func TestNormalize_DuplicateKeyRejected(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantPointer string
	}{
		{
			name:        "duplicate_key_at_root",
			in:          `{"a":1,"a":2}`,
			wantPointer: "",
		},
		{
			name:        "duplicate_key_nested",
			in:          `{"properties":{"a":1,"b":2,"a":3}}`,
			wantPointer: "/properties",
		},
		{
			name:        "duplicate_key_inside_array_element_object",
			in:          `{"anyOf":[{"type":"string"},{"type":"number","type":"integer"}]}`,
			wantPointer: "/anyOf/1",
		},
		{
			// The duplicate itself is nested one level under a key ("a/b")
			// that needs RFC 6901 escaping ("/" -> "~1") to appear in a
			// pointer at all -- this is what actually exercises
			// escapePointer, unlike a root-level duplicate under that key.
			name:        "duplicate_key_under_a_parent_key_needing_pointer_escaping",
			in:          `{"a/b":{"x":1,"x":2}}`,
			wantPointer: "/a~1b",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := schemanorm.Normalize(json.RawMessage(tc.in))
			if err == nil {
				t.Fatalf("Normalize(%s): expected ErrDuplicateKey, got no error", tc.in)
			}
			if !errors.Is(err, schemanorm.ErrDuplicateKey) {
				t.Errorf("errors.Is(err, ErrDuplicateKey) = false; err = %v", err)
			}
			var normErr *schemanorm.Error
			if !errors.As(err, &normErr) {
				t.Fatalf("errors.As(err, &schemanorm.Error{}) failed; err = %v", err)
			}
			if normErr.Pointer != tc.wantPointer {
				t.Errorf("Pointer = %q, want %q", normErr.Pointer, tc.wantPointer)
			}
		})
	}
}

// TestError_MessageQuotesControlCharsInPointer guards against log/terminal
// injection: an object key legally contains control characters via a
// "\u00XX" escape, and *Error.Pointer is built directly from that raw key
// text (see escapePointer in validate.go). Error() must render Pointer with
// %q, not %s, so a key containing an escaped newline or ESC cannot forge a
// second log line or inject an ANSI control sequence into whatever the error
// string is written to.
func TestError_MessageQuotesControlCharsInPointer(t *testing.T) {
	tests := []struct {
		name           string
		in             string
		forbiddenBytes string // raw bytes that must NOT appear unescaped in Error()
	}{
		{
			name:           "escaped_newline_in_key_cannot_forge_a_log_line",
			in:             "{\"a\\nb\":{\"x\":1,\"x\":2}}",
			forbiddenBytes: "\n",
		},
		{
			name:           "escaped_esc_in_key_cannot_inject_ansi",
			in:             "{\"a\\u001bb\":{\"x\":1,\"x\":2}}",
			forbiddenBytes: "\x1b",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := schemanorm.Normalize(json.RawMessage(tc.in))
			if err == nil {
				t.Fatalf("Normalize(%s): expected ErrDuplicateKey, got no error", tc.in)
			}
			msg := err.Error()
			// The message must still be a single line: strip the one
			// trailing newline strings.TrimSuffix would leave from a
			// legitimate rendering, then confirm no forbidden control byte
			// survived unescaped anywhere in the string.
			if strings.Contains(msg, tc.forbiddenBytes) {
				t.Errorf("Error() = %q contains raw control byte %q unescaped; want it quoted via %%q", msg, tc.forbiddenBytes)
			}
			if strings.Count(msg, "\n") > 0 {
				t.Errorf("Error() = %q contains a raw newline; a hostile key could forge additional log lines", msg)
			}
		})
	}
}

// TestNormalize_InvalidUTF8Rejected is the sharp-edge-2 regression table.
func TestNormalize_InvalidUTF8Rejected(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"raw_invalid_utf8_bytes_in_a_string", []byte("{\"description\":\"\xff\xfe\"}")},
		{"lone_high_surrogate_escape", []byte(`{"description":"\ud800"}`)},
		{"lone_low_surrogate_escape", []byte(`{"description":"\udc00"}`)},
		{"high_surrogate_not_followed_by_low", []byte(`{"description":"\ud800x"}`)},
		{"two_consecutive_high_surrogates", []byte(`{"description":"\ud800\ud800"}`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := schemanorm.Normalize(json.RawMessage(tc.in))
			if err == nil {
				t.Fatalf("Normalize(%s): expected ErrInvalidUTF8, got no error", tc.in)
			}
			if !errors.Is(err, schemanorm.ErrInvalidUTF8) {
				t.Errorf("errors.Is(err, ErrInvalidUTF8) = false; err = %v", err)
			}
		})
	}
}

// TestNormalize_Errors covers malformed input and the bound violations.
func TestNormalize_Errors(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		limits      *schemanorm.Limits
		wantErrKind error
	}{
		{
			name:        "empty_input",
			in:          "",
			wantErrKind: schemanorm.ErrInvalidJSON,
		},
		{
			name:        "whitespace_only_input",
			in:          "   \n\t",
			wantErrKind: schemanorm.ErrInvalidJSON,
		},
		{
			name:        "malformed_json",
			in:          `{"a":}`,
			wantErrKind: schemanorm.ErrInvalidJSON,
		},
		{
			name:        "trailing_garbage_after_root_value",
			in:          `{"a":1} garbage`,
			wantErrKind: schemanorm.ErrInvalidJSON,
		},
		{
			name:        "byte_size_exceeded",
			in:          `{"a":1}`,
			limits:      &schemanorm.Limits{MaxBytes: 3},
			wantErrKind: schemanorm.ErrByteSizeExceeded,
		},
		{
			name:        "depth_exceeded",
			in:          `{"a":{"a":{"a":{"a":{"a":{"a":1}}}}}}`,
			limits:      &schemanorm.Limits{MaxDepth: 3},
			wantErrKind: schemanorm.ErrDepthExceeded,
		},
		{
			name:        "node_count_exceeded",
			in:          `{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6}`,
			limits:      &schemanorm.Limits{MaxNodes: 5},
			wantErrKind: schemanorm.ErrNodeCountExceeded,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.limits != nil {
				_, err = schemanorm.NormalizeWithLimits(json.RawMessage(tc.in), *tc.limits)
			} else {
				_, err = schemanorm.Normalize(json.RawMessage(tc.in))
			}
			if err == nil {
				t.Fatalf("Normalize(%s): expected error %v, got none", tc.in, tc.wantErrKind)
			}
			if !errors.Is(err, tc.wantErrKind) {
				t.Errorf("errors.Is(err, %v) = false; err = %v", tc.wantErrKind, err)
			}
		})
	}
}

// TestNormalize_BoundsCannotBeDisabled asserts that a zero or negative
// Limits field falls back to its documented default rather than disabling
// the bound.
func TestNormalize_BoundsCannotBeDisabled(t *testing.T) {
	deep := `{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":1}}}}}}}}}}` // 10 levels
	_, err := schemanorm.NormalizeWithLimits(json.RawMessage(deep), schemanorm.Limits{MaxDepth: 0})
	if err != nil {
		t.Fatalf("MaxDepth: 0 should fall back to DefaultMaxDepth (%d), well above 10 levels; got error: %v", schemanorm.DefaultMaxDepth, err)
	}

	_, err = schemanorm.NormalizeWithLimits(json.RawMessage(`{"a":1}`), schemanorm.Limits{MaxDepth: -1, MaxNodes: -1, MaxBytes: -1})
	if err != nil {
		t.Fatalf("negative Limits fields should all fall back to defaults, not disable the bound; got error: %v", err)
	}
}
