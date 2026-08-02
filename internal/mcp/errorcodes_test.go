package mcp

import (
	"fmt"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

// TestMCPErrorCodeMapping pins the values in errorcodes.go against the spec
// and against ClassifyMCPErrorType, both bare and through a wrapped chain.
// Wire fixtures in other test files (fakeserver_test.go, discover_test.go)
// deliberately keep LITERAL codes rather than these constants, so they stay
// an independent cross-check of this registry instead of a tautology.
func TestMCPErrorCodeMapping(t *testing.T) {
	tests := []struct {
		name         string
		code         int
		wantValue    int
		wantReserved bool
		wantLabel    string
	}{
		{
			name:         "HeaderMismatch",
			code:         errCodeHeaderMismatch,
			wantValue:    -32020,
			wantReserved: true,
			wantLabel:    metrics.ErrorTypeProtocol,
		},
		{
			name:         "MissingRequiredClientCapability",
			code:         errCodeMissingRequiredClientCapability,
			wantValue:    -32021,
			wantReserved: true,
			wantLabel:    metrics.ErrorTypeProtocol,
		},
		{
			name:         "UnsupportedProtocolVersion",
			code:         errCodeUnsupportedProtocolVersion,
			wantValue:    -32022,
			wantReserved: true,
			wantLabel:    metrics.ErrorTypeProtocol,
		},
		{
			name:         "InvalidParams",
			code:         errCodeInvalidParams,
			wantValue:    -32602,
			wantReserved: false,
			wantLabel:    metrics.ErrorTypeProtocol,
		},
		{
			name:         "ResourceNotFoundLegacy",
			code:         errCodeResourceNotFoundLegacy,
			wantValue:    -32002,
			wantReserved: false,
			wantLabel:    metrics.ErrorTypeProtocol,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.code != tc.wantValue {
				t.Fatalf("code = %d, want %d", tc.code, tc.wantValue)
			}

			gotReserved := tc.code >= errCodeMCPReservedMin && tc.code <= errCodeMCPReservedMax
			if gotReserved != tc.wantReserved {
				t.Errorf("in MCP-reserved range = %v, want %v", gotReserved, tc.wantReserved)
			}

			bare := &JSONRPCError{Code: tc.code}
			if got := ClassifyMCPErrorType(bare); got != tc.wantLabel {
				t.Errorf("ClassifyMCPErrorType(bare) = %q, want %q", got, tc.wantLabel)
			}

			// An illustrative wrap, not a literal call-site quote — CallTool's
			// only "post tools/call: %w" wrap covers a sendRPC transport
			// failure, never the bare *JSONRPCError (which it returns
			// unwrapped). This exercises errors.As through a wrapped chain,
			// which is the thing under test here.
			wrapped := fmt.Errorf("mcp probe: %w", &JSONRPCError{Code: tc.code})
			if got := ClassifyMCPErrorType(wrapped); got != tc.wantLabel {
				t.Errorf("ClassifyMCPErrorType(wrapped) = %q, want %q", got, tc.wantLabel)
			}
		})
	}
}

// TestMCPReservedRangeBounds pins the reserved-range boundary constants
// classifyDiscoverResponse depends on for era detection.
func TestMCPReservedRangeBounds(t *testing.T) {
	if errCodeMCPReservedMin >= errCodeMCPReservedMax {
		t.Errorf("errCodeMCPReservedMin (%d) >= errCodeMCPReservedMax (%d), want <", errCodeMCPReservedMin, errCodeMCPReservedMax)
	}
	if errCodeMCPReservedMax != -32020 {
		t.Errorf("errCodeMCPReservedMax = %d, want -32020", errCodeMCPReservedMax)
	}
	if errCodeMCPReservedMin != -32099 {
		t.Errorf("errCodeMCPReservedMin = %d, want -32099", errCodeMCPReservedMin)
	}
}
