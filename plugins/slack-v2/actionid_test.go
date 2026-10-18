package main

import "testing"

func TestActionIDRoundTrip(t *testing.T) {
	tests := []struct{ requestID, optionID string }{
		{"req-1", "approve"},
		{"01HZX:with:colons", "reject"},
		{"r", "o"},
	}
	for _, tt := range tests {
		gotReq, gotOpt, ok := parseActionID(actionIDFor(tt.requestID, tt.optionID))
		if !ok || gotReq != tt.requestID || gotOpt != tt.optionID {
			t.Errorf("round trip (%q,%q) = (%q,%q,%v)", tt.requestID, tt.optionID, gotReq, gotOpt, ok)
		}
	}
}

func TestParseActionIDMalformed(t *testing.T) {
	for _, in := range []string{
		"",
		"approve",
		"feedback_response:",
		"feedback_response:req",
		"feedback_response::approve",
		"feedback_response:req:",
		"other_prefix:req:approve",
		"FEEDBACK_RESPONSE:req:approve",
	} {
		if req, opt, ok := parseActionID(in); ok {
			t.Errorf("parseActionID(%q) = (%q,%q,true), want ok=false", in, req, opt)
		}
	}
}
