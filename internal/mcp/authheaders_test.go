package mcp

import (
	"testing"
)

func TestValidateHeaderName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Valid names
		{name: "x-api-key", input: "x-api-key", wantErr: false},
		{name: "Authorization", input: "Authorization", wantErr: false},
		{name: "X-Custom-Header", input: "X-Custom-Header", wantErr: false},
		{name: "x-custom-123", input: "x-custom-123", wantErr: false},

		// Empty name
		{name: "empty", input: "", wantErr: true},

		// CR/LF injection (rejected by httpguts)
		{name: "CR in name", input: "X-Bad\rInjected", wantErr: true},
		{name: "LF in name", input: "X-Bad\nInjected", wantErr: true},
		{name: "CRLF in name", input: "X-Bad\r\nInjected", wantErr: true},

		// NUL byte (rejected by httpguts)
		{name: "NUL in name", input: "X-Bad\x00Header", wantErr: true},

		// Colon (rejected by httpguts — not a valid token char)
		{name: "colon in name", input: "X:Bad", wantErr: true},

		// Space (rejected by httpguts)
		{name: "space in name", input: "X Bad", wantErr: true},

		// Reserved headers (case-insensitive)
		{name: "Mcp-Session-Id", input: "Mcp-Session-Id", wantErr: true},
		{name: "mcp-session-id lowercase", input: "mcp-session-id", wantErr: true},
		{name: "MCP-SESSION-ID uppercase", input: "MCP-SESSION-ID", wantErr: true},
		{name: "Content-Type", input: "Content-Type", wantErr: true},
		{name: "content-type lowercase", input: "content-type", wantErr: true},
		{name: "Accept", input: "Accept", wantErr: true},
		{name: "ACCEPT uppercase", input: "ACCEPT", wantErr: true},
		{name: "Content-Length", input: "Content-Length", wantErr: true},
		{name: "Host", input: "Host", wantErr: true},
		{name: "HOST uppercase", input: "HOST", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHeaderName(tt.input)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateHeaderName(%q) = nil, want error", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateHeaderName(%q) = %v, want nil", tt.input, err)
			}
		})
	}
}

func TestMarshalUnmarshalAuthHeaders_RoundTrip(t *testing.T) {
	headers := []AuthHeader{
		{Name: "x-api-key", Value: "sk-live-123"},
		{Name: "Authorization", Value: "Bearer token-abc"},
	}

	data, err := MarshalAuthHeaders(headers)
	if err != nil {
		t.Fatalf("MarshalAuthHeaders: %v", err)
	}
	if data == nil {
		t.Fatal("MarshalAuthHeaders returned nil for non-empty input")
	}

	got, err := UnmarshalAuthHeaders(data)
	if err != nil {
		t.Fatalf("UnmarshalAuthHeaders: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Name != "x-api-key" || got[0].Value != "sk-live-123" {
		t.Errorf("got[0] = %+v, want {x-api-key, sk-live-123}", got[0])
	}
	if got[1].Name != "Authorization" || got[1].Value != "Bearer token-abc" {
		t.Errorf("got[1] = %+v, want {Authorization, Bearer token-abc}", got[1])
	}
}

func TestMarshalAuthHeaders_EmptyReturnsNil(t *testing.T) {
	data, err := MarshalAuthHeaders(nil)
	if err != nil {
		t.Fatalf("MarshalAuthHeaders(nil): %v", err)
	}
	if data != nil {
		t.Errorf("MarshalAuthHeaders(nil) = %q, want nil", data)
	}

	data2, err := MarshalAuthHeaders([]AuthHeader{})
	if err != nil {
		t.Fatalf("MarshalAuthHeaders([]): %v", err)
	}
	if data2 != nil {
		t.Errorf("MarshalAuthHeaders([]) = %q, want nil", data2)
	}
}

func TestUnmarshalAuthHeaders_EmptyInput(t *testing.T) {
	got, err := UnmarshalAuthHeaders(nil)
	if err != nil {
		t.Fatalf("UnmarshalAuthHeaders(nil): %v", err)
	}
	if got != nil {
		t.Errorf("UnmarshalAuthHeaders(nil) = %v, want nil", got)
	}

	got2, err := UnmarshalAuthHeaders([]byte{})
	if err != nil {
		t.Fatalf("UnmarshalAuthHeaders([]): %v", err)
	}
	if got2 != nil {
		t.Errorf("UnmarshalAuthHeaders([]) = %v, want nil", got2)
	}
}

