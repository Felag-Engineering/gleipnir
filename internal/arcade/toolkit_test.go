package arcade_test

import (
	"testing"

	"github.com/rapp992/gleipnir/internal/arcade"
	"github.com/rapp992/gleipnir/internal/db"
)

func TestSplitToolkit(t *testing.T) {
	tests := []struct {
		name          string
		qualifiedName string
		wantToolkit   string
		wantAction    string
	}{
		{
			name:          "standard qualified name",
			qualifiedName: "Gmail.SendEmail",
			wantToolkit:   "Gmail",
			wantAction:    "SendEmail",
		},
		{
			name:          "no dot — no toolkit",
			qualifiedName: "SendEmail",
			wantToolkit:   "",
			wantAction:    "SendEmail",
		},
		{
			name:          "two dots — only first splits",
			qualifiedName: "Gmail.Send.Email",
			wantToolkit:   "Gmail",
			wantAction:    "Send.Email",
		},
		{
			name:          "empty string",
			qualifiedName: "",
			wantToolkit:   "",
			wantAction:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotToolkit, gotAction := arcade.SplitToolkit(tc.qualifiedName)
			if gotToolkit != tc.wantToolkit || gotAction != tc.wantAction {
				t.Errorf("SplitToolkit(%q) = (%q, %q), want (%q, %q)",
					tc.qualifiedName, gotToolkit, gotAction, tc.wantToolkit, tc.wantAction)
			}
		})
	}
}

func TestGroupToolsByToolkit(t *testing.T) {
	tools := []db.McpTool{
		{ID: "1", Name: "Gmail.SendEmail"},
		{ID: "2", Name: "Gmail.ListEmails"},
		{ID: "3", Name: "GoogleCalendar.CreateEvent"},
		{ID: "4", Name: "nodot"},          // no dot — skipped
		{ID: "5", Name: "Gmail.Archive"},  // preserves order within toolkit
	}

	got := arcade.GroupToolsByToolkit(tools)

	// Three toolkit keys expected (nodot skipped).
	if len(got) != 2 {
		t.Fatalf("expected 2 toolkits, got %d: %v", len(got), got)
	}

	gmailTools := got["Gmail"]
	if len(gmailTools) != 3 {
		t.Fatalf("expected 3 Gmail tools, got %d", len(gmailTools))
	}
	// Preserve input order.
	if gmailTools[0].Name != "Gmail.SendEmail" || gmailTools[1].Name != "Gmail.ListEmails" || gmailTools[2].Name != "Gmail.Archive" {
		t.Errorf("Gmail tools not in expected order: %v", gmailTools)
	}

	calTools := got["GoogleCalendar"]
	if len(calTools) != 1 {
		t.Fatalf("expected 1 GoogleCalendar tool, got %d", len(calTools))
	}
	if calTools[0].Name != "GoogleCalendar.CreateEvent" {
		t.Errorf("unexpected GoogleCalendar tool name: %q", calTools[0].Name)
	}

	// Dotless tool is not present.
	if _, ok := got[""]; ok {
		t.Error("expected no entry for empty toolkit key")
	}
}

func TestGroupToolsByToolkitEmpty(t *testing.T) {
	got := arcade.GroupToolsByToolkit(nil)
	if len(got) != 0 {
		t.Errorf("expected empty map for nil input, got %v", got)
	}
}
