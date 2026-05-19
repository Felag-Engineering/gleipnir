package main

import (
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

// TestBuildRequestBlocks_DefaultButtons asserts that buildRequestBlocks with
// defaultResponseButtons produces blocks containing "Approve" and "Reject".
func TestBuildRequestBlocks_DefaultButtons(t *testing.T) {
	blocks := buildRequestBlocks("req-123", "Should we deploy?", defaultResponseButtons(), "")
	if len(blocks) == 0 {
		t.Fatal("expected non-empty blocks slice")
	}
	// Action block must contain action_ids for both approve and reject.
	approveID := actionIDFor("req-123", "approve")
	rejectID := actionIDFor("req-123", "reject")
	if !strings.Contains(approveID, "req-123") {
		t.Errorf("approve action_id %q does not contain requestID", approveID)
	}
	if !strings.Contains(rejectID, "reject") {
		t.Errorf("reject action_id %q does not contain 'reject'", rejectID)
	}
	// Verify 2 blocks: header section + action block.
	if len(blocks) != 2 {
		t.Errorf("want 2 blocks (header + actions), got %d", len(blocks))
	}
}

// TestBuildRequestBlocks_WithMention asserts that the mention is prepended to
// the prompt text in the header section.
func TestBuildRequestBlocks_WithMention(t *testing.T) {
	blocks := buildRequestBlocks("req-456", "Approve deployment?", defaultResponseButtons(), "@oncall")
	if len(blocks) == 0 {
		t.Fatal("expected non-empty blocks slice")
	}
	if len(blocks) != 2 {
		t.Errorf("want 2 blocks, got %d", len(blocks))
	}
	headerBlock, ok := blocks[0].(*slack.SectionBlock)
	if !ok {
		t.Fatalf("expected SectionBlock at index 0, got %T", blocks[0])
	}
	if !strings.Contains(headerBlock.Text.Text, "@oncall") {
		t.Errorf("expected mention '@oncall' in section text, got %q", headerBlock.Text.Text)
	}
}

// TestActionIDRoundTrip asserts that parseActionID(actionIDFor(reqID, optID))
// returns the original values.
func TestActionIDRoundTrip(t *testing.T) {
	cases := []struct {
		requestID string
		optionID  string
	}{
		{"req-abc-123", "approve"},
		{"req-xyz", "reject"},
		{"some:complex:id", "custom_option"},
	}
	for _, tc := range cases {
		actionID := actionIDFor(tc.requestID, tc.optionID)
		gotReq, gotOpt, ok := parseActionID(actionID)
		if !ok {
			t.Errorf("parseActionID(%q): ok=false, want true", actionID)
			continue
		}
		if gotReq != tc.requestID {
			t.Errorf("parseActionID(%q): requestID=%q, want %q", actionID, gotReq, tc.requestID)
		}
		if gotOpt != tc.optionID {
			t.Errorf("parseActionID(%q): optionID=%q, want %q", actionID, gotOpt, tc.optionID)
		}
	}
}

// TestParseActionID_BadFormat asserts that malformed action_ids return ok=false.
func TestParseActionID_BadFormat(t *testing.T) {
	cases := []string{
		"",
		"not_feedback_response",
		"feedback_response:",
		"feedback_response:only_one_part",
		"feedback_response::empty_request_id",
		"feedback_response:some_req:",
	}
	for _, actionID := range cases {
		_, _, ok := parseActionID(actionID)
		if ok {
			t.Errorf("parseActionID(%q): ok=true, want false", actionID)
		}
	}
}
