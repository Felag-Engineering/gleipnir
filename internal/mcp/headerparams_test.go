package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/infra/headervalidate"
)

func TestExtractHeaderParams(t *testing.T) {
	tests := []struct {
		name               string
		schema             json.RawMessage
		input              map[string]any
		authHeaders        []AuthHeader // configured ADR-039 auth headers for the server; nil unless a case tests the collision
		want               []headerParam
		wantErrProperty    string // non-empty: assert errors.As(*HeaderParamError) with this Property
		forbidValueInError string // non-empty: assert Error() does not contain this substring
	}{
		{
			name:   "one annotated property produces one header; unannotated property produces none",
			schema: json.RawMessage(`{"properties":{"api_key":{"type":"string","x-mcp-header":"X-Api-Key"},"other":{"type":"string"}}}`),
			input:  map[string]any{"api_key": "secret123", "other": "value"},
			want:   []headerParam{{Name: "X-Api-Key", Value: "secret123"}},
		},
		{
			name:   "two annotated properties sorted by header name",
			schema: json.RawMessage(`{"properties":{"a":{"x-mcp-header":"X-Zebra"},"b":{"x-mcp-header":"X-Alpha"}}}`),
			input:  map[string]any{"a": "1", "b": "2"},
			want:   []headerParam{{Name: "X-Alpha", Value: "2"}, {Name: "X-Zebra", Value: "1"}},
		},
		{
			name:   "annotated property absent from input produces no header and no error",
			schema: json.RawMessage(`{"properties":{"a":{"x-mcp-header":"X-A"}}}`),
			input:  map[string]any{},
			want:   nil,
		},
		{
			name:   "value coercion: integral float64",
			schema: json.RawMessage(`{"properties":{"v":{"x-mcp-header":"X-V"}}}`),
			input:  map[string]any{"v": float64(5)},
			want:   []headerParam{{Name: "X-V", Value: "5"}},
		},
		{
			name:   "value coercion: fractional float64",
			schema: json.RawMessage(`{"properties":{"v":{"x-mcp-header":"X-V"}}}`),
			input:  map[string]any{"v": float64(2.5)},
			want:   []headerParam{{Name: "X-V", Value: "2.5"}},
		},
		{
			name:   "value coercion: bool",
			schema: json.RawMessage(`{"properties":{"v":{"x-mcp-header":"X-V"}}}`),
			input:  map[string]any{"v": true},
			want:   []headerParam{{Name: "X-V", Value: "true"}},
		},
		{
			name:   "value coercion: json.Number preserved verbatim",
			schema: json.RawMessage(`{"properties":{"v":{"x-mcp-header":"X-V"}}}`),
			input:  map[string]any{"v": json.Number("1e400")},
			want:   []headerParam{{Name: "X-V", Value: "1e400"}},
		},
		{
			name:   "value coercion: int (poll-trigger YAML input)",
			schema: json.RawMessage(`{"properties":{"v":{"x-mcp-header":"X-V"}}}`),
			input:  map[string]any{"v": int(7)},
			want:   []headerParam{{Name: "X-V", Value: "7"}},
		},
		{
			name:   "value coercion: int64 (poll-trigger YAML input)",
			schema: json.RawMessage(`{"properties":{"v":{"x-mcp-header":"X-V"}}}`),
			input:  map[string]any{"v": int64(9)},
			want:   []headerParam{{Name: "X-V", Value: "9"}},
		},
		{
			name:            "value is nil → error",
			schema:          json.RawMessage(`{"properties":{"v":{"x-mcp-header":"X-V"}}}`),
			input:           map[string]any{"v": nil},
			wantErrProperty: "v",
		},
		{
			name:            "value is an object → error",
			schema:          json.RawMessage(`{"properties":{"v":{"x-mcp-header":"X-V"}}}`),
			input:           map[string]any{"v": map[string]any{"x": 1}},
			wantErrProperty: "v",
		},
		{
			name:            "value is an array → error",
			schema:          json.RawMessage(`{"properties":{"v":{"x-mcp-header":"X-V"}}}`),
			input:           map[string]any{"v": []any{1, 2}},
			wantErrProperty: "v",
		},
		{
			name:               "value contains CRLF header injection → error, value never in message",
			schema:             json.RawMessage(`{"properties":{"v":{"x-mcp-header":"X-V"}}}`),
			input:              map[string]any{"v": "ok\r\nX-Injected: 1"},
			wantErrProperty:    "v",
			forbidValueInError: "X-Injected",
		},
		{
			name:            "annotation is not a JSON string: number",
			schema:          json.RawMessage(`{"properties":{"a":{"x-mcp-header":5}}}`),
			input:           map[string]any{"a": "x"},
			wantErrProperty: "a",
		},
		{
			name:            "annotation is not a JSON string: array",
			schema:          json.RawMessage(`{"properties":{"a":{"x-mcp-header":["A"]}}}`),
			input:           map[string]any{"a": "x"},
			wantErrProperty: "a",
		},
		{
			name:   "empty schema → nil, nil",
			schema: json.RawMessage(``),
			input:  map[string]any{"a": "x"},
			want:   nil,
		},
		{
			name:   "null schema → nil, nil",
			schema: json.RawMessage(`null`),
			input:  map[string]any{"a": "x"},
			want:   nil,
		},
		{
			name:   "boolean schema (legal JSON Schema, no properties) → nil, nil",
			schema: json.RawMessage(`true`),
			input:  map[string]any{"a": "x"},
			want:   nil,
		},
		{
			name:   "schema with no properties → nil, nil",
			schema: json.RawMessage(`{"type":"object"}`),
			input:  map[string]any{"a": "x"},
			want:   nil,
		},
		{
			name:   "property schema that is not an object → nil, nil",
			schema: json.RawMessage(`{"properties":{"a":true}}`),
			input:  map[string]any{"a": "x"},
			want:   nil,
		},
		{
			name:            "two properties declare the same header name, differing only in case → error",
			schema:          json.RawMessage(`{"properties":{"a":{"x-mcp-header":"X-Dup"},"b":{"x-mcp-header":"x-dup"}}}`),
			input:           nil, // duplicate rejection must not depend on input values being present
			wantErrProperty: "b",
		},
		{
			name:            "value exceeds maxHeaderParamValueLen → error (S4)",
			schema:          json.RawMessage(`{"properties":{"v":{"x-mcp-header":"X-V"}}}`),
			input:           map[string]any{"v": strings.Repeat("a", maxHeaderParamValueLen+1)},
			wantErrProperty: "v",
		},
		{
			name:            "declared name collides with a configured ADR-039 auth header → fails closed, not left to ordering (S2(b))",
			schema:          json.RawMessage(`{"properties":{"api_key":{"x-mcp-header":"x-api-key"}}}`), // differing case on purpose
			input:           map[string]any{"api_key": "agent-supplied"},
			authHeaders:     []AuthHeader{{Name: "X-Api-Key", Value: "admin-secret"}},
			wantErrProperty: "api_key",
		},
		{
			name:            "underscore twin of a configured ADR-039 auth header is rejected outright, not left to canonicalization (S6)",
			schema:          json.RawMessage(`{"properties":{"api_key":{"x-mcp-header":"X-Api_key"}}}`),
			input:           map[string]any{"api_key": "agent-supplied"},
			authHeaders:     []AuthHeader{{Name: "X-Api-Key", Value: "admin-secret"}},
			wantErrProperty: "api_key",
		},
		{
			name:            "underscore twin of a denied X-Forwarded-* name is rejected outright, not just the '-' prefix form (S6)",
			schema:          json.RawMessage(`{"properties":{"ip":{"x-mcp-header":"X-Forwarded_for"}}}`),
			input:           map[string]any{"ip": "127.0.0.1"},
			wantErrProperty: "ip",
		},
		{
			name:            "dot twin of a configured ADR-039 auth header is rejected outright (S11)",
			schema:          json.RawMessage(`{"properties":{"api_key":{"x-mcp-header":"X.Api.Key"}}}`),
			input:           map[string]any{"api_key": "agent-supplied"},
			authHeaders:     []AuthHeader{{Name: "X-Api-Key", Value: "admin-secret"}},
			wantErrProperty: "api_key",
		},
		{
			name:            "dot twin of a denied X-Forwarded-* name is rejected outright (S11)",
			schema:          json.RawMessage(`{"properties":{"ip":{"x-mcp-header":"X.Forwarded.For"}}}`),
			input:           map[string]any{"ip": "127.0.0.1"},
			wantErrProperty: "ip",
		},
		{
			name:            "dot twin of the reserved Mcp-Name header is rejected outright, not smuggleable (S11)",
			schema:          json.RawMessage(`{"properties":{"name":{"x-mcp-header":"Mcp.Name"}}}`),
			input:           map[string]any{"name": "agent-supplied"},
			wantErrProperty: "name",
		},
		{
			name:            "tilde, caret, backtick, and pipe twins are all rejected outright (S11)",
			schema:          json.RawMessage("{\"properties\":{\"a\":{\"x-mcp-header\":\"X~A\"},\"b\":{\"x-mcp-header\":\"X^B\"},\"c\":{\"x-mcp-header\":\"X`C\"},\"d\":{\"x-mcp-header\":\"X|D\"}}}"),
			input:           map[string]any{"a": "1", "b": "2", "c": "3", "d": "4"},
			wantErrProperty: "a",
		},
		{
			name:   "ordinary hyphenated names are accepted (no regression)",
			schema: json.RawMessage(`{"properties":{"v":{"type":"string","x-mcp-header":"X-Api-Version"},"t":{"type":"string","x-mcp-header":"X-Trace-Id"},"l":{"type":"string","x-mcp-header":"Accept-Language"}}}`),
			input:  map[string]any{"v": "1", "t": "abc", "l": "en-US"},
			want:   []headerParam{{Name: "Accept-Language", Value: "en-US"}, {Name: "X-Api-Version", Value: "1"}, {Name: "X-Trace-Id", Value: "abc"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractHeaderParams(tc.schema, tc.input, tc.authHeaders)

			if tc.wantErrProperty == "" {
				if err != nil {
					t.Fatalf("extractHeaderParams: unexpected error: %v", err)
				}
				if len(got) != len(tc.want) {
					t.Fatalf("extractHeaderParams = %+v, want %+v", got, tc.want)
				}
				for i := range got {
					if got[i] != tc.want[i] {
						t.Errorf("extractHeaderParams[%d] = %+v, want %+v", i, got[i], tc.want[i])
					}
				}
				return
			}

			if err == nil {
				t.Fatal("extractHeaderParams: expected an error, got nil")
			}
			var hpErr *HeaderParamError
			if !errors.As(err, &hpErr) {
				t.Fatalf("extractHeaderParams error is not a *HeaderParamError: %v", err)
			}
			if hpErr.Property != tc.wantErrProperty {
				t.Errorf("HeaderParamError.Property = %q, want %q", hpErr.Property, tc.wantErrProperty)
			}
			if got != nil {
				t.Errorf("extractHeaderParams returned non-nil headers alongside an error: %+v", got)
			}
			if tc.forbidValueInError != "" && strings.Contains(err.Error(), tc.forbidValueInError) {
				t.Errorf("error message %q must not contain the agent-supplied value %q", err.Error(), tc.forbidValueInError)
			}
		})
	}
}

// TestExtractHeaderParams_MoreThanMaxHeaderParams locks the maxHeaderParams
// bound: a tool whose schema resolves more annotated properties to header
// values than the bound allows fails closed rather than truncating silently.
func TestExtractHeaderParams_MoreThanMaxHeaderParams(t *testing.T) {
	properties := make(map[string]any, maxHeaderParams+1)
	input := make(map[string]any, maxHeaderParams+1)
	for i := 0; i < maxHeaderParams+1; i++ {
		propName := fmt.Sprintf("prop%02d", i)
		headerName := fmt.Sprintf("X-Header-%02d", i)
		properties[propName] = map[string]any{"x-mcp-header": headerName}
		input[propName] = "v"
	}
	schemaObj := map[string]any{"properties": properties}
	schema, err := json.Marshal(schemaObj)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}

	got, err := extractHeaderParams(schema, input, nil)
	if err == nil {
		t.Fatal("extractHeaderParams: expected an error for exceeding maxHeaderParams, got nil")
	}
	var hpErr *HeaderParamError
	if !errors.As(err, &hpErr) {
		t.Fatalf("extractHeaderParams error is not a *HeaderParamError: %v", err)
	}
	if got != nil {
		t.Errorf("extractHeaderParams returned non-nil headers alongside an error: %+v", got)
	}
}

// TestExtractHeaderParams_SchemaTooLarge locks the maxHeaderParamSchemaBytes
// bound (R4): an oversized schema is treated as "nothing safely extractable"
// rather than failing the call, since the schema is the server's own
// declaration and not agent-controlled input.
func TestExtractHeaderParams_SchemaTooLarge(t *testing.T) {
	oversized := make([]byte, maxHeaderParamSchemaBytes+1)
	for i := range oversized {
		oversized[i] = ' '
	}
	oversized[0] = '{'
	oversized[len(oversized)-1] = '}'

	got, err := extractHeaderParams(json.RawMessage(oversized), map[string]any{"a": "x"}, nil)
	if err != nil {
		t.Fatalf("extractHeaderParams: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("extractHeaderParams = %+v, want nil for an oversized schema", got)
	}
}

// TestExtractHeaderParams_ReservedNamesRejected ranges over
// headervalidate.ReservedHeaderNames (both as-written and lower-cased) so
// any future addition to that list is covered automatically: an MCP server
// must never be able to smuggle a reserved header name through a tool
// parameter's x-mcp-header annotation.
func TestExtractHeaderParams_ReservedNamesRejected(t *testing.T) {
	for _, reserved := range headervalidate.ReservedHeaderNames {
		for _, variant := range []string{reserved, strings.ToLower(reserved)} {
			t.Run(variant, func(t *testing.T) {
				schemaObj := map[string]any{
					"properties": map[string]any{
						"a": map[string]any{"x-mcp-header": variant},
					},
				}
				schema, err := json.Marshal(schemaObj)
				if err != nil {
					t.Fatalf("marshal schema: %v", err)
				}

				got, err := extractHeaderParams(schema, map[string]any{"a": "x"}, nil)
				if err == nil {
					t.Fatalf("extractHeaderParams: expected an error for reserved name %q, got nil", variant)
				}
				var hpErr *HeaderParamError
				if !errors.As(err, &hpErr) {
					t.Fatalf("extractHeaderParams error is not a *HeaderParamError: %v", err)
				}
				if hpErr.Property != "a" {
					t.Errorf("HeaderParamError.Property = %q, want %q", hpErr.Property, "a")
				}
				if got != nil {
					t.Errorf("extractHeaderParams returned non-nil headers alongside an error: %+v", got)
				}
			})
		}
	}
}

// TestExtractHeaderParams_DeniedNamesRejected ranges over
// deniedHeaderParamNames (both as-written and lower-cased), plus a sample of
// the X-Forwarded-* family it denies by prefix, so any future addition to
// that list is covered automatically (S1/S2/S7/S8/S9 of the security
// review) — this is how X-HTTP-Method, X-Method-Override, X-Real-IP, and
// User-Agent, all now literal entries in deniedHeaderParamNames, get their
// own subtests without further changes here. Two names that a plain range
// over the list would NOT reach are added explicitly below: "X-Http-Method"
// (the exact wire casing from the S7 repro, distinct from the list's own
// "X-HTTP-Method" entry once both are canonicalized) and bare "X-Forwarded"
// (matched by isDeniedHeaderParamName's own equality check, not the
// prefix match or the list, since S8). Every one of these names passes
// headervalidate.ValidateName — none of them is in
// headervalidate.ReservedHeaderNames — so this is what stops a remote MCP
// server from choosing them via a tool's own schema.
func TestExtractHeaderParams_DeniedNamesRejected(t *testing.T) {
	denied := append([]string{}, deniedHeaderParamNames...)
	denied = append(denied,
		"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", // X-Forwarded-* family, denied by prefix
		"X-Forwarded",   // bare name, not just the "-" prefixed family (S8)
		"X-Http-Method", // wire casing from the S7 repro; not the list's own "X-HTTP-Method" casing
	)

	for _, name := range denied {
		for _, variant := range []string{name, strings.ToLower(name)} {
			t.Run(variant, func(t *testing.T) {
				schemaObj := map[string]any{
					"properties": map[string]any{
						"a": map[string]any{"x-mcp-header": variant},
					},
				}
				schema, err := json.Marshal(schemaObj)
				if err != nil {
					t.Fatalf("marshal schema: %v", err)
				}

				got, err := extractHeaderParams(schema, map[string]any{"a": "x"}, nil)
				if err == nil {
					t.Fatalf("extractHeaderParams: expected an error for denied name %q, got nil", variant)
				}
				var hpErr *HeaderParamError
				if !errors.As(err, &hpErr) {
					t.Fatalf("extractHeaderParams error is not a *HeaderParamError: %v", err)
				}
				if hpErr.Property != "a" {
					t.Errorf("HeaderParamError.Property = %q, want %q", hpErr.Property, "a")
				}
				if got != nil {
					t.Errorf("extractHeaderParams returned non-nil headers alongside an error: %+v", got)
				}
			})
		}
	}
}

// TestHeaderParamError_ErrorStaysBoundedForHugeName locks S3: even when
// headervalidate.ValidateName's own error text re-embeds an untruncated,
// server-controlled header name, HeaderParamError.Error() stays bounded.
// Before the fix, a multi-megabyte x-mcp-header annotation with one illegal
// character produced a multi-megabyte Error() — an unbounded injection
// channel into the audit trail and the LLM's message history via
// schemaViolation.
func TestHeaderParamError_ErrorStaysBoundedForHugeName(t *testing.T) {
	// A trailing space is not a valid RFC 7230 token character, so
	// headervalidate.ValidateName rejects this name and its error embeds it
	// verbatim via %q.
	hugeName := strings.Repeat("A", 3_000_000) + " "
	schemaObj := map[string]any{
		"properties": map[string]any{
			"a": map[string]any{"x-mcp-header": hugeName},
		},
	}
	schema, err := json.Marshal(schemaObj)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}

	_, err = extractHeaderParams(schema, map[string]any{"a": "x"}, nil)
	if err == nil {
		t.Fatal("extractHeaderParams: expected an error for an invalid header name, got nil")
	}

	// Generous: Property and HeaderName are each capped at
	// maxHeaderParamFieldLen, Reason at maxHeaderParamReasonLen, plus a
	// small constant amount of format-string overhead.
	const maxBoundedErrorLen = 3 * (maxHeaderParamFieldLen + maxHeaderParamReasonLen)
	if got := len(err.Error()); got > maxBoundedErrorLen {
		t.Errorf("HeaderParamError.Error() length = %d, want <= %d (S3: an untruncated server-controlled name must not reach Error())", got, maxBoundedErrorLen)
	}
}
