package audience_test

import (
	"errors"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/audience"
)

// alwaysCanRequest is a callback that always reports the instance can handle Request.
func alwaysCanRequest(_ string) (bool, error) { return true, nil }

// neverCanRequest is a callback that always reports the instance cannot handle Request.
func neverCanRequest(_ string) (bool, error) { return false, nil }

// errCallback returns an error on any call.
var errCallbackErr = errors.New("callback failure")

func errCallback(_ string) (bool, error) { return false, errCallbackErr }

func TestValidateAudienceCoverage_DisableFalse_ZeroEntries_NoError(t *testing.T) {
	errs, err := audience.ValidateAudienceCoverage(nil, false, alwaysCanRequest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("expected no FieldErrors, got %v", errs)
	}
}

func TestValidateAudienceCoverage_DisableFalse_NotifyOnly_NoError(t *testing.T) {
	entries := []audience.ProposedEntry{
		{PluginInstanceID: "inst1", Notify: true, Request: false},
	}
	errs, err := audience.ValidateAudienceCoverage(entries, false, neverCanRequest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("expected no FieldErrors, got %v", errs)
	}
}

func TestValidateAudienceCoverage_DisableTrue_ZeroEntries_OneError(t *testing.T) {
	errs, err := audience.ValidateAudienceCoverage(nil, true, alwaysCanRequest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 FieldError, got %d", len(errs))
	}
	if errs[0].Field != "disable_in_app_fallback" {
		t.Errorf("FieldError.Field = %q, want %q", errs[0].Field, "disable_in_app_fallback")
	}
}

func TestValidateAudienceCoverage_DisableTrue_NotifyOnly_OneError(t *testing.T) {
	entries := []audience.ProposedEntry{
		{PluginInstanceID: "inst1", Notify: true, Request: false},
	}
	errs, err := audience.ValidateAudienceCoverage(entries, true, alwaysCanRequest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 FieldError, got %d: %v", len(errs), errs)
	}
	if errs[0].Field != "disable_in_app_fallback" {
		t.Errorf("FieldError.Field = %q, want %q", errs[0].Field, "disable_in_app_fallback")
	}
}

func TestValidateAudienceCoverage_DisableTrue_OneRequestCapable_NoError(t *testing.T) {
	entries := []audience.ProposedEntry{
		{PluginInstanceID: "inst1", Notify: false, Request: true},
	}
	errs, err := audience.ValidateAudienceCoverage(entries, true, alwaysCanRequest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) != 0 {
		t.Errorf("expected no FieldErrors, got %v", errs)
	}
}

// Entry has Request=true but the manifest does NOT implement Request.
func TestValidateAudienceCoverage_DisableTrue_RequestEntryManifestNoImpl_OneError(t *testing.T) {
	entries := []audience.ProposedEntry{
		{PluginInstanceID: "inst1", Notify: false, Request: true},
	}
	errs, err := audience.ValidateAudienceCoverage(entries, true, neverCanRequest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 FieldError, got %d", len(errs))
	}
}

// Callback returns a Go error → propagated, no FieldError emitted.
func TestValidateAudienceCoverage_DisableTrue_CallbackError_Propagated(t *testing.T) {
	entries := []audience.ProposedEntry{
		{PluginInstanceID: "inst1", Request: true},
	}
	_, err := audience.ValidateAudienceCoverage(entries, true, errCallback)
	if !errors.Is(err, errCallbackErr) {
		t.Errorf("expected errCallbackErr, got %v", err)
	}
}
