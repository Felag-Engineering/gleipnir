package google

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
}

// TestListModels_ZeroValueClient verifies that a zero-value GeminiClient
// returns the curated list without panicking or making a network call.
func TestListModels_ZeroValueClient(t *testing.T) {
	c := &GeminiClient{}
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
	c := &GeminiClient{}
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
	c := &GeminiClient{}
	err := c.ValidateModelName(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown model, got nil")
	}
}
