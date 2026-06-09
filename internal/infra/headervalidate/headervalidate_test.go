package headervalidate

import (
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Valid names
		{name: "simple lowercase", input: "x-api-key", wantErr: false},
		{name: "Authorization", input: "Authorization", wantErr: false},
		{name: "X-Custom-Header", input: "X-Custom-Header", wantErr: false},
		{name: "alphanumeric with hyphen", input: "x-custom-123", wantErr: false},

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

		// Reserved headers — case-insensitive checks
		{name: "Mcp-Session-Id exact", input: "Mcp-Session-Id", wantErr: true},
		{name: "mcp-session-id lowercase", input: "mcp-session-id", wantErr: true},
		{name: "MCP-SESSION-ID uppercase", input: "MCP-SESSION-ID", wantErr: true},
		{name: "Content-Type exact", input: "Content-Type", wantErr: true},
		{name: "content-type lowercase", input: "content-type", wantErr: true},
		{name: "Accept exact", input: "Accept", wantErr: true},
		{name: "ACCEPT uppercase", input: "ACCEPT", wantErr: true},
		{name: "Content-Length exact", input: "Content-Length", wantErr: true},
		{name: "Host exact", input: "Host", wantErr: true},
		{name: "HOST uppercase", input: "HOST", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateName(tt.input)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateName(%q) = nil, want error", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateName(%q) = %v, want nil", tt.input, err)
			}
		})
	}
}

// TestReservedHeaderNames_AllCoveredByValidateName verifies that every name in
// ReservedHeaderNames is rejected by ValidateName regardless of case.
func TestReservedHeaderNames_AllCoveredByValidateName(t *testing.T) {
	for _, reserved := range ReservedHeaderNames {
		// Exact case
		if err := ValidateName(reserved); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error for reserved header", reserved)
		}
		// Lowercase
		if err := ValidateName(strings.ToLower(reserved)); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error for lowercase reserved header", strings.ToLower(reserved))
		}
		// Uppercase
		if err := ValidateName(strings.ToUpper(reserved)); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error for uppercase reserved header", strings.ToUpper(reserved))
		}
	}
}
