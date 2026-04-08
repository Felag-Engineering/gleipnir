package anthropic

import (
	"context"
	"testing"
)

// TestCuratedModels_NonEmpty verifies the curated list is populated and that
// every entry has non-empty Name and DisplayName fields with no duplicates.
// claude-sonnet-4-6 must be present as the primary default model.
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

	if !seen["claude-sonnet-4-6"] {
		t.Error("claude-sonnet-4-6 must be present in curatedModels")
	}
}

// TestListModels_ZeroValueClient verifies that a zero-value AnthropicClient
// returns the curated list without panicking or making a network call.
func TestListModels_ZeroValueClient(t *testing.T) {
	c := &AnthropicClient{}
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
	c := &AnthropicClient{}
	for _, m := range curatedModels {
		t.Run(m.Name, func(t *testing.T) {
			if err := c.ValidateModelName(context.Background(), m.Name); err != nil {
				t.Errorf("ValidateModelName(%q) returned unexpected error: %v", m.Name, err)
			}
		})
	}
}

// TestValidateModelName_DatedAlias_Regression is a regression guard for the
// stored-policy backward-compat alias "claude-sonnet-4-20250514", which is
// referenced in schemas/policy.yaml:49. Rejecting it would break run launches
// for any stored policy that uses this dated pin.
func TestValidateModelName_DatedAlias_Regression(t *testing.T) {
	c := &AnthropicClient{}
	if err := c.ValidateModelName(context.Background(), "claude-sonnet-4-20250514"); err != nil {
		t.Errorf("expected nil for backward-compat alias, got: %v", err)
	}
}

// TestValidateModelName_Unknown verifies that an unrecognized model name
// returns a descriptive error.
func TestValidateModelName_Unknown(t *testing.T) {
	c := &AnthropicClient{}
	err := c.ValidateModelName(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown model, got nil")
	}
}

// TestListModels_DoesNotContainAlias verifies that dated aliases in
// validationAliases are NOT included in the display list returned by ListModels.
// They are for validation only and should not appear in the UI model picker.
func TestListModels_DoesNotContainAlias(t *testing.T) {
	c := &AnthropicClient{}
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	for _, m := range models {
		if _, isAlias := validationAliases[m.Name]; isAlias {
			t.Errorf("ListModels returned alias %q, which should not appear in the display list", m.Name)
		}
	}
}
