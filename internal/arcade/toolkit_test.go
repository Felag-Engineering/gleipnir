package arcade_test

import (
	"testing"

	"github.com/felag-engineering/gleipnir/internal/arcade"
	"github.com/felag-engineering/gleipnir/internal/db"
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
			qualifiedName: "Gmail_SendEmail",
			wantToolkit:   "Gmail",
			wantAction:    "SendEmail",
		},
		{
			name:          "no underscore — no toolkit",
			qualifiedName: "SendEmail",
			wantToolkit:   "",
			wantAction:    "SendEmail",
		},
		{
			name:          "two underscores — only first splits",
			qualifiedName: "Gmail_Send_Email",
			wantToolkit:   "Gmail",
			wantAction:    "Send_Email",
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

func TestAuthorizeToolName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"single underscore swapped to dot", "Gmail_SendEmail", "Gmail.SendEmail"},
		{"only first underscore swapped", "Gmail_Send_Email", "Gmail.Send_Email"},
		{"no underscore left untouched", "Plainname", "Plainname"},
		{"empty string", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := arcade.AuthorizeToolName(tc.in); got != tc.want {
				t.Errorf("AuthorizeToolName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestGroupToolsByToolkit(t *testing.T) {
	tools := []db.McpTool{
		{ID: "1", Name: "Gmail_SendEmail"},
		{ID: "2", Name: "Gmail_ListEmails"},
		{ID: "3", Name: "GoogleCalendar_CreateEvent"},
		{ID: "4", Name: "nounderscore"},  // no underscore — skipped
		{ID: "5", Name: "Gmail_Archive"}, // preserves order within toolkit
	}

	got := arcade.GroupToolsByToolkit(tools)

	// Two toolkit keys expected (the dotless tool is skipped).
	if len(got) != 2 {
		t.Fatalf("expected 2 toolkits, got %d: %v", len(got), got)
	}

	gmailTools := got["Gmail"]
	if len(gmailTools) != 3 {
		t.Fatalf("expected 3 Gmail tools, got %d", len(gmailTools))
	}
	// Preserve input order.
	if gmailTools[0].Name != "Gmail_SendEmail" || gmailTools[1].Name != "Gmail_ListEmails" || gmailTools[2].Name != "Gmail_Archive" {
		t.Errorf("Gmail tools not in expected order: %v", gmailTools)
	}

	calTools := got["GoogleCalendar"]
	if len(calTools) != 1 {
		t.Fatalf("expected 1 GoogleCalendar tool, got %d", len(calTools))
	}
	if calTools[0].Name != "GoogleCalendar_CreateEvent" {
		t.Errorf("unexpected GoogleCalendar tool name: %q", calTools[0].Name)
	}

	// Underscoreless tool is not present.
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
