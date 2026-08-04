package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDecodeInputRequiredResult is the table-driven decode test the #792
// acceptance criteria calls for: happy path, each spec §6.2 cap violation,
// and malformed inputRequests. "Absent resultType" is NOT a case here --
// decodeInputRequiredResult is only ever reached once CallTool has already
// normalised resultType to ResultTypeInputRequired (see
// TestCallTool_InputRequired_AbsentResultTypeNeverDecodes below), so an
// absent resultType is proven by never calling this function at all.
func TestDecodeInputRequiredResult(t *testing.T) {
	validRequests := json.RawMessage(`[{"message":"delete the production database?","requestedSchema":{"type":"object"}}]`)
	validState := json.RawMessage(`"opaque-state-token"`)

	tests := []struct {
		name       string
		result     toolsCallResult
		wantErr    bool
		wantReason string // non-empty: substring InputRequiredError.Reason must contain
	}{
		{
			name: "happy path",
			result: toolsCallResult{
				InputRequests: json.RawMessage(`[{"message":"delete the production database?","requestedSchema":{"type":"object","properties":{}},"_meta":{"io.gleipnir/elicitation-kind":"permission"}}]`),
				RequestState:  validState,
			},
		},
		{
			name: "requestState missing",
			result: toolsCallResult{
				InputRequests: validRequests,
			},
			wantErr:    true,
			wantReason: "missing requestState",
		},
		{
			name: "requestState exceeds byte cap",
			result: toolsCallResult{
				InputRequests: validRequests,
				RequestState:  json.RawMessage(`"` + strings.Repeat("a", maxRequestStateBytes) + `"`),
			},
			wantErr:    true,
			wantReason: "requestState is",
		},
		{
			name: "inputRequests exceeds byte cap",
			result: toolsCallResult{
				InputRequests: json.RawMessage(`[{"message":"` + strings.Repeat("a", maxInputRequestsBytes) + `","requestedSchema":{}}]`),
				RequestState:  validState,
			},
			wantErr:    true,
			wantReason: "inputRequests is",
		},
		{
			name: "inputRequests exceeds count cap",
			result: toolsCallResult{
				InputRequests: manyInputRequests(t, maxInputRequests+1),
				RequestState:  validState,
			},
			wantErr:    true,
			wantReason: "exceeds the limit",
		},
		{
			name: "inputRequests is empty array",
			result: toolsCallResult{
				InputRequests: json.RawMessage(`[]`),
				RequestState:  validState,
			},
			wantErr:    true,
			wantReason: "empty",
		},
		{
			name: "inputRequests absent entirely",
			result: toolsCallResult{
				RequestState: validState,
			},
			wantErr:    true,
			wantReason: "does not parse",
		},
		{
			name: "inputRequests is a JSON object, not an array",
			result: toolsCallResult{
				InputRequests: json.RawMessage(`{"message":"oops"}`),
				RequestState:  validState,
			},
			wantErr:    true,
			wantReason: "does not parse",
		},
		{
			name: "inputRequests entry has a non-string message",
			result: toolsCallResult{
				InputRequests: json.RawMessage(`[{"message":123,"requestedSchema":{}}]`),
				RequestState:  validState,
			},
			wantErr:    true,
			wantReason: "does not parse",
		},
		{
			name: "inputRequests entry is not an object",
			result: toolsCallResult{
				InputRequests: json.RawMessage(`["not an object"]`),
				RequestState:  validState,
			},
			wantErr:    true,
			wantReason: "does not parse",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeInputRequiredResult(tc.result)

			if !tc.wantErr {
				if err != nil {
					t.Fatalf("decodeInputRequiredResult: unexpected error: %v", err)
				}
				if len(got.InputRequests) != 1 {
					t.Fatalf("len(InputRequests) = %d, want 1", len(got.InputRequests))
				}
				if got.InputRequests[0].Message != "delete the production database?" {
					t.Errorf("Message = %q, want %q", got.InputRequests[0].Message, "delete the production database?")
				}
				if got.InputRequests[0].ElicitationKind != "permission" {
					t.Errorf("ElicitationKind = %q, want %q", got.InputRequests[0].ElicitationKind, "permission")
				}
				if string(got.RequestState) != string(validState) {
					t.Errorf("RequestState = %s, want %s", got.RequestState, validState)
				}
				return
			}

			if err == nil {
				t.Fatal("decodeInputRequiredResult: expected an error, got nil")
			}
			var irErr *InputRequiredError
			if !errors.As(err, &irErr) {
				t.Fatalf("decodeInputRequiredResult error is not an *InputRequiredError: %v", err)
			}
			if tc.wantReason != "" && !strings.Contains(irErr.Reason, tc.wantReason) {
				t.Errorf("Reason = %q, want it to contain %q", irErr.Reason, tc.wantReason)
			}
			if got.InputRequests != nil || got.RequestState != nil {
				t.Errorf("decodeInputRequiredResult returned non-zero result alongside an error: %+v", got)
			}
		})
	}
}

// manyInputRequests builds a valid inputRequests JSON array of n entries, for
// exercising the maxInputRequests count cap independent of the byte cap.
func manyInputRequests(t *testing.T, n int) json.RawMessage {
	t.Helper()
	entries := make([]map[string]any, n)
	for i := range entries {
		entries[i] = map[string]any{"message": "confirm?", "requestedSchema": map[string]any{}}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal inputRequests fixture: %v", err)
	}
	return raw
}

// TestParseElicitationKind covers the tolerant _meta decode: present + valid,
// absent, malformed JSON, and a non-string value all resolve to something
// usable rather than failing the whole InputRequest decode -- same posture
// as parseServerInfo (meta_test.go).
func TestParseElicitationKind(t *testing.T) {
	tests := []struct {
		name string
		meta json.RawMessage
		want string
	}{
		{name: "present", meta: json.RawMessage(`{"io.gleipnir/elicitation-kind":"permission"}`), want: "permission"},
		{name: "absent key", meta: json.RawMessage(`{}`), want: ""},
		{name: "nil meta", meta: nil, want: ""},
		{name: "empty meta", meta: json.RawMessage(``), want: ""},
		{name: "meta is not an object", meta: json.RawMessage(`"oops"`), want: ""},
		{name: "value is not a string", meta: json.RawMessage(`{"io.gleipnir/elicitation-kind":5}`), want: ""},
		{name: "malformed JSON", meta: json.RawMessage(`{"io.gleipnir/elicitation-kind":`), want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseElicitationKind(tc.meta); got != tc.want {
				t.Errorf("parseElicitationKind(%s) = %q, want %q", tc.meta, got, tc.want)
			}
		})
	}
}

// TestCallTool_InputRequired_AbsentResultTypeNeverDecodes proves the
// legacy/pre-#792 case is unchanged: a server that never sends resultType at
// all (the every-server-before-2026-07-28 case, and the fixture for spec
// §11's "absent ⇒ complete" rule) never reaches decodeInputRequiredResult --
// ToolResult.InputRequired stays nil and the call succeeds, even though this
// fake also configures inputRequests/requestState fixtures that would fail
// every cap check if they were ever decoded.
func TestCallTool_InputRequired_AbsentResultTypeNeverDecodes(t *testing.T) {
	fake := NewFakeMCPServer(
		WithFakeMode(FakeModern),
		// No WithFakeToolResultType call: resultType stays absent from the wire.
		WithFakeInputRequired(nil, nil),
	)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL)
	res, err := c.CallTool(context.Background(), "tool-a", nil, CallOptions{})
	if err != nil {
		t.Fatalf("CallTool: unexpected error: %v", err)
	}
	if res.ResultType != ResultTypeComplete {
		t.Errorf("ResultType = %q, want %q", res.ResultType, ResultTypeComplete)
	}
	if res.InputRequired != nil {
		t.Errorf("InputRequired = %+v, want nil", res.InputRequired)
	}
}

// TestCallTool_InputRequired_OversizeFailsTheCall proves the spec §6.2
// "oversize is rejected as a structural error" rule end to end through
// CallTool, not just at decodeInputRequiredResult: the call fails and
// ToolResult is the zero value, so nothing is left for a caller to
// accidentally persist.
func TestCallTool_InputRequired_OversizeFailsTheCall(t *testing.T) {
	fake := NewFakeMCPServer(
		WithFakeMode(FakeModern),
		WithFakeToolResultType(ResultTypeInputRequired),
		WithFakeInputRequired(manyInputRequests(t, maxInputRequests+1), `"opaque-state-token"`),
	)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL)
	res, err := c.CallTool(context.Background(), "tool-a", nil, CallOptions{})
	if err == nil {
		t.Fatal("CallTool: expected an error for an oversize input_required result, got nil")
	}
	var irErr *InputRequiredError
	if !errors.As(err, &irErr) {
		t.Fatalf("CallTool error is not an *InputRequiredError: %v", err)
	}
	if res.Output != nil || res.IsError || res.ResultType != "" || res.InputRequired != nil {
		t.Errorf("ToolResult = %+v, want the zero value on a failed call", res)
	}
}

// TestCallTool_InputRequiredRetry_AttachesInputResponsesAndRequestState
// proves the MRTR retry-shape support (spec §6.4): when CallOptions carries
// InputResponses + RequestState, the outbound tools/call params include
// both, byte-identical to what the caller supplied.
func TestCallTool_InputRequiredRetry_AttachesInputResponsesAndRequestState(t *testing.T) {
	fake := NewFakeMCPServer(WithFakeMode(FakeModern), WithFakeRejectLegacyHandshake())
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, WithProtocolVersion(ProtocolVersion20260728))
	opts := CallOptions{
		InputResponses: []InputResponse{
			{Action: "accept", Content: json.RawMessage(`{"confirmed":true}`)},
		},
		RequestState: json.RawMessage(`"opaque-state-token"`),
	}
	if _, err := c.CallTool(context.Background(), "tool-a", nil, opts); err != nil {
		t.Fatalf("CallTool: unexpected error: %v", err)
	}

	callReqs := fake.RequestsFor(methodToolsCall)
	if len(callReqs) != 1 {
		t.Fatalf("len(RequestsFor(tools/call)) = %d, want 1", len(callReqs))
	}

	var params toolsCallParams
	if err := json.Unmarshal(callReqs[0].Params, &params); err != nil {
		t.Fatalf("unmarshal recorded params: %v", err)
	}
	if len(params.InputResponses) != 1 {
		t.Fatalf("len(InputResponses) = %d, want 1", len(params.InputResponses))
	}
	if params.InputResponses[0].Action != "accept" {
		t.Errorf("InputResponses[0].Action = %q, want %q", params.InputResponses[0].Action, "accept")
	}
	if string(params.RequestState) != `"opaque-state-token"` {
		t.Errorf("RequestState = %s, want %q", params.RequestState, "opaque-state-token")
	}
}

// TestCallTool_InputRequiredRetry_LegacyTransportOmitsBothFields proves the
// legacy byte-identical guarantee this package holds everywhere else
// (WithProtocolVersion doc, requestMeta doc): even if a caller mistakenly
// sets InputResponses/RequestState against a legacy-pinned client, neither
// field reaches the wire.
func TestCallTool_InputRequiredRetry_LegacyTransportOmitsBothFields(t *testing.T) {
	fake := NewFakeMCPServer(WithFakeMode(FakeModern))
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, WithProtocolVersion(ProtocolVersionLegacy))
	opts := CallOptions{
		InputResponses: []InputResponse{{Action: "accept"}},
		RequestState:   json.RawMessage(`"opaque-state-token"`),
	}
	if _, err := c.CallTool(context.Background(), "tool-a", nil, opts); err != nil {
		t.Fatalf("CallTool: unexpected error: %v", err)
	}

	callReqs := fake.RequestsFor(methodToolsCall)
	if len(callReqs) != 1 {
		t.Fatalf("len(RequestsFor(tools/call)) = %d, want 1", len(callReqs))
	}
	if strings.Contains(string(callReqs[0].Params), "inputResponses") || strings.Contains(string(callReqs[0].Params), "requestState") {
		t.Errorf("legacy tools/call params carry MRTR fields, want neither: %s", callReqs[0].Params)
	}
}
