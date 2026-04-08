package openai

import (
	"context"
	"testing"
)

// TestCuratedModels_NonEmpty verifies the curated list is populated and that
// every entry has non-empty Name and DisplayName fields with no duplicates.
func TestCuratedModels_NonEmpty(t *testing.T) {
	if len(curatedModels) == 0 {
		t.Fatal("curatedModels must not be empty")
	}

	seen := make(map[string]bool, len(curatedModels))
	for _, m := range curatedModels {
		if m.Name == "" {
			t.Errorf("curatedModels entry has empty Name: %+v", m)
		}
		if m.DisplayName == "" {
			t.Errorf("curatedModels entry %q has empty DisplayName", m.Name)
		}
		if seen[m.Name] {
			t.Errorf("duplicate Name %q in curatedModels", m.Name)
		}
		seen[m.Name] = true
	}

	// Confirm each expected model ID is present.
	expected := []string{"gpt-5", "gpt-5-mini", "gpt-5-nano", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano"}
	for _, id := range expected {
		if !seen[id] {
			t.Errorf("%q is missing from curatedModels", id)
		}
	}
}

// TestListModels_ZeroValueClient verifies that a zero-value Client returns
// the curated list without panicking or making a network call.
func TestListModels_ZeroValueClient(t *testing.T) {
	c := &Client{}
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels on zero-value client returned error: %v", err)
	}
	if len(models) != len(curatedModels) {
		t.Errorf("got %d models, want %d", len(models), len(curatedModels))
	}
}

// TestValidateModelName_CuratedModels verifies every curated model name passes
// validation without error.
func TestValidateModelName_CuratedModels(t *testing.T) {
	c := &Client{}
	for _, m := range curatedModels {
		t.Run(m.Name, func(t *testing.T) {
			if err := c.ValidateModelName(context.Background(), m.Name); err != nil {
				t.Errorf("ValidateModelName(%q) returned unexpected error: %v", m.Name, err)
			}
		})
	}
}

// TestValidateModelName_Unknown verifies that an unrecognized model name
// returns a descriptive error.
func TestValidateModelName_Unknown(t *testing.T) {
	c := &Client{}
	err := c.ValidateModelName(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown model, got nil")
	}
}

// TestValidateModelName_Empty verifies that an empty model name returns an error.
func TestValidateModelName_Empty(t *testing.T) {
	c := &Client{}
	if err := c.ValidateModelName(context.Background(), ""); err == nil {
		t.Error("expected error for empty model name, got nil")
	}
}

// TestCuratedModels_NoReasoningModels is a regression guard asserting that
// o3 and o4-mini are not in the curated list. They are excluded because we
// cannot guarantee the #618 tool-use invariant for reasoning models.
func TestCuratedModels_NoReasoningModels(t *testing.T) {
	excluded := []string{"o3", "o4-mini"}
	seen := make(map[string]bool, len(curatedModels))
	for _, m := range curatedModels {
		seen[m.Name] = true
	}
	for _, id := range excluded {
		if seen[id] {
			t.Errorf("%q must not be in curatedModels (reasoning model, #618 invariant not verified)", id)
		}
	}
}
