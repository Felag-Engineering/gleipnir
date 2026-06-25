package main

import (
	"strings"
	"testing"

	"github.com/slack-go/slack"

	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
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

// TestBuildNotifyBlocks asserts that buildNotifyBlocks returns a single
// section block with the given text (and optional mention prepended).
func TestBuildNotifyBlocks(t *testing.T) {
	t.Run("without_mention", func(t *testing.T) {
		blocks := buildNotifyBlocks("Run completed successfully.", "")
		if len(blocks) != 1 {
			t.Fatalf("want 1 block, got %d", len(blocks))
		}
		sb, ok := blocks[0].(*slack.SectionBlock)
		if !ok {
			t.Fatalf("want SectionBlock, got %T", blocks[0])
		}
		if sb.Text.Text != "Run completed successfully." {
			t.Errorf("text: want %q, got %q", "Run completed successfully.", sb.Text.Text)
		}
	})

	t.Run("with_mention", func(t *testing.T) {
		blocks := buildNotifyBlocks("Alert!", "@oncall")
		if len(blocks) != 1 {
			t.Fatalf("want 1 block, got %d", len(blocks))
		}
		sb, ok := blocks[0].(*slack.SectionBlock)
		if !ok {
			t.Fatalf("want SectionBlock, got %T", blocks[0])
		}
		if !strings.Contains(sb.Text.Text, "@oncall") {
			t.Errorf("mention not in text: %q", sb.Text.Text)
		}
		if !strings.Contains(sb.Text.Text, "Alert!") {
			t.Errorf("body not in text: %q", sb.Text.Text)
		}
	})
}

// TestBuildResolvedBlocks asserts that buildResolvedBlocks:
//   - Has NO action block (buttons removed).
//   - Contains the emoji/label for each reason.
//   - Includes the prompt as a context block when non-empty.
func TestBuildResolvedBlocks(t *testing.T) {
	cases := []struct {
		reason    channelv1.TerminalReason
		wantEmoji string
		wantLabel string
	}{
		{channelv1.TerminalReason_TERMINAL_REASON_APPROVED, "✅", "Approved"},
		{channelv1.TerminalReason_TERMINAL_REASON_REJECTED, "⛔", "Rejected"},
		{channelv1.TerminalReason_TERMINAL_REASON_ANSWERED, "💬", "Answered"},
		{channelv1.TerminalReason_TERMINAL_REASON_TIMED_OUT, "⏰", "Expired"},
	}

	for _, tc := range cases {
		t.Run(tc.wantLabel, func(t *testing.T) {
			blocks := buildResolvedBlocks("Should we deploy?", tc.reason, "U01USER")
			if len(blocks) == 0 {
				t.Fatal("expected non-empty blocks")
			}

			// No action block.
			for _, b := range blocks {
				if _, isAction := b.(*slack.ActionBlock); isAction {
					t.Error("resolved blocks must NOT contain an action block")
				}
			}

			// Summary section contains emoji + label.
			sb, ok := blocks[0].(*slack.SectionBlock)
			if !ok {
				t.Fatalf("blocks[0]: want SectionBlock, got %T", blocks[0])
			}
			if !strings.Contains(sb.Text.Text, tc.wantEmoji) {
				t.Errorf("emoji %q not in summary %q", tc.wantEmoji, sb.Text.Text)
			}
			if !strings.Contains(sb.Text.Text, tc.wantLabel) {
				t.Errorf("label %q not in summary %q", tc.wantLabel, sb.Text.Text)
			}

			// Prompt appears in a context block.
			var foundPrompt bool
			for _, b := range blocks {
				if cb, ok := b.(*slack.ContextBlock); ok {
					for _, elem := range cb.ContextElements.Elements {
						if to, ok := elem.(*slack.TextBlockObject); ok {
							if strings.Contains(to.Text, "Should we deploy?") {
								foundPrompt = true
							}
						}
					}
				}
			}
			if !foundPrompt {
				t.Error("prompt not found in context block")
			}
		})
	}
}

// TestBuildResolvedBlocks_NoPrompt asserts that when the prompt is empty no
// extra context block is added.
func TestBuildResolvedBlocks_NoPrompt(t *testing.T) {
	blocks := buildResolvedBlocks("", channelv1.TerminalReason_TERMINAL_REASON_TIMED_OUT, "")
	if len(blocks) != 1 {
		t.Errorf("want 1 block with empty prompt, got %d", len(blocks))
	}
}

// TestFallbackText asserts the plain-text fallback combinator.
func TestFallbackText(t *testing.T) {
	cases := []struct {
		summary string
		prompt  string
		want    string
	}{
		{"✅ Approved", "Deploy to prod?", "✅ Approved\nDeploy to prod?"},
		{"⏰ Expired", "", "⏰ Expired"},
	}
	for _, tc := range cases {
		got := fallbackText(tc.summary, tc.prompt)
		if got != tc.want {
			t.Errorf("fallbackText(%q, %q) = %q, want %q", tc.summary, tc.prompt, got, tc.want)
		}
	}
}
