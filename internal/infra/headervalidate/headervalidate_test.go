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
		{name: "Mcp-Method exact", input: "Mcp-Method", wantErr: true},
		{name: "mcp-method lowercase", input: "mcp-method", wantErr: true},
		{name: "MCP-METHOD uppercase", input: "MCP-METHOD", wantErr: true},
		{name: "mCp-MeThOd mixed case", input: "mCp-MeThOd", wantErr: true},
		{name: "Mcp-Name exact", input: "Mcp-Name", wantErr: true},
		{name: "mcp-name lowercase", input: "mcp-name", wantErr: true},
		{name: "MCP-NAME uppercase", input: "MCP-NAME", wantErr: true},
		{name: "mCp-nAmE mixed case", input: "mCp-nAmE", wantErr: true},
		{name: "Mcp-Protocol-Version exact", input: "Mcp-Protocol-Version", wantErr: true},
		{name: "mcp-protocol-version lowercase", input: "mcp-protocol-version", wantErr: true},
		{name: "MCP-PROTOCOL-VERSION uppercase", input: "MCP-PROTOCOL-VERSION", wantErr: true},
		{name: "mCp-PrOtOcOl-VeRsIoN mixed case", input: "mCp-PrOtOcOl-VeRsIoN", wantErr: true},
		{name: "Content-Type exact", input: "Content-Type", wantErr: true},
		{name: "content-type lowercase", input: "content-type", wantErr: true},
		{name: "Accept exact", input: "Accept", wantErr: true},
		{name: "ACCEPT uppercase", input: "ACCEPT", wantErr: true},
		{name: "Content-Length exact", input: "Content-Length", wantErr: true},
		{name: "Host exact", input: "Host", wantErr: true},
		{name: "HOST uppercase", input: "HOST", wantErr: true},

		// Negative controls — near-miss names must NOT be rejected; the check
		// is exact-match, not prefix/substring.
		{name: "X-Mcp-Method is allowed", input: "X-Mcp-Method", wantErr: false},
		{name: "Mcp-Method-Override is allowed", input: "Mcp-Method-Override", wantErr: false},
		{name: "Mcp-Names is allowed", input: "Mcp-Names", wantErr: false},
		{name: "Mcp-Protocol-Versions is allowed", input: "Mcp-Protocol-Versions", wantErr: false},
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

// TestReservedHeaderNames_ExactSet pins the exact set of reserved names,
// independent of order. ValidateName's guarantee is membership, not
// ordering, so an order-sensitive comparison here would fail CI on a purely
// cosmetic reorder. The point of this test is to make two kinds of drift
// loud: a silent removal of "Mcp-Session-Id" before its 12-month deprecation
// window closes, and a silent, undocumented addition to the list.
func TestReservedHeaderNames_ExactSet(t *testing.T) {
	want := map[string]struct{}{
		"Mcp-Session-Id":       {},
		"Mcp-Method":           {},
		"Mcp-Name":             {},
		"Mcp-Protocol-Version": {},
		"Content-Type":         {},
		"Accept":               {},
		"Content-Length":       {},
		"Host":                 {},
	}

	got := make(map[string]struct{}, len(ReservedHeaderNames))
	for _, name := range ReservedHeaderNames {
		got[name] = struct{}{}
	}

	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("ReservedHeaderNames is missing expected entry %q", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("ReservedHeaderNames has unexpected entry %q", name)
		}
	}
}
