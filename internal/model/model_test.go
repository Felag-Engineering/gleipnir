package model

import (
	"sort"
	"testing"

	"github.com/oklog/ulid/v2"
)

func TestEnumValid(t *testing.T) {
	t.Run("RunStatus", func(t *testing.T) {
		valid := []RunStatus{
			RunStatusPending, RunStatusRunning, RunStatusWaitingForApproval,
			RunStatusComplete, RunStatusFailed, RunStatusInterrupted,
		}
		for _, v := range valid {
			if !v.Valid() {
				t.Errorf("expected %q to be valid", v)
			}
		}
		for _, bad := range []RunStatus{"", "invalid"} {
			if bad.Valid() {
				t.Errorf("expected %q to be invalid", bad)
			}
		}
	})

	t.Run("TriggerType", func(t *testing.T) {
		valid := []TriggerType{TriggerTypeWebhook, TriggerTypeCron, TriggerTypePoll}
		for _, v := range valid {
			if !v.Valid() {
				t.Errorf("expected %q to be valid", v)
			}
		}
		for _, bad := range []TriggerType{"", "invalid"} {
			if bad.Valid() {
				t.Errorf("expected %q to be invalid", bad)
			}
		}
	})

	t.Run("CapabilityRole", func(t *testing.T) {
		valid := []CapabilityRole{CapabilityRoleSensor, CapabilityRoleActuator, CapabilityRoleFeedback}
		for _, v := range valid {
			if !v.Valid() {
				t.Errorf("expected %q to be valid", v)
			}
		}
		for _, bad := range []CapabilityRole{"", "invalid"} {
			if bad.Valid() {
				t.Errorf("expected %q to be invalid", bad)
			}
		}
	})

	t.Run("StepType", func(t *testing.T) {
		valid := []StepType{
			StepTypeThought, StepTypeToolCall, StepTypeToolResult,
			StepTypeApprovalRequest, StepTypeFeedbackRequest, StepTypeFeedbackResponse,
			StepTypeError, StepTypeComplete,
		}
		for _, v := range valid {
			if !v.Valid() {
				t.Errorf("expected %q to be valid", v)
			}
		}
		for _, bad := range []StepType{"", "invalid"} {
			if bad.Valid() {
				t.Errorf("expected %q to be invalid", bad)
			}
		}
	})

	t.Run("ApprovalMode", func(t *testing.T) {
		valid := []ApprovalMode{ApprovalModeNone, ApprovalModeRequired}
		for _, v := range valid {
			if !v.Valid() {
				t.Errorf("expected %q to be valid", v)
			}
		}
		for _, bad := range []ApprovalMode{"", "invalid"} {
			if bad.Valid() {
				t.Errorf("expected %q to be invalid", bad)
			}
		}
	})

	t.Run("OnTimeout", func(t *testing.T) {
		valid := []OnTimeout{OnTimeoutReject, OnTimeoutApprove}
		for _, v := range valid {
			if !v.Valid() {
				t.Errorf("expected %q to be valid", v)
			}
		}
		for _, bad := range []OnTimeout{"", "invalid"} {
			if bad.Valid() {
				t.Errorf("expected %q to be invalid", bad)
			}
		}
	})

	t.Run("ConcurrencyPolicy", func(t *testing.T) {
		valid := []ConcurrencyPolicy{ConcurrencySkip, ConcurrencyQueue, ConcurrencyParallel, ConcurrencyReplace}
		for _, v := range valid {
			if !v.Valid() {
				t.Errorf("expected %q to be valid", v)
			}
		}
		for _, bad := range []ConcurrencyPolicy{"", "invalid"} {
			if bad.Valid() {
				t.Errorf("expected %q to be invalid", bad)
			}
		}
	})

	t.Run("ApprovalStatus", func(t *testing.T) {
		valid := []ApprovalStatus{
			ApprovalStatusPending, ApprovalStatusApproved, ApprovalStatusRejected, ApprovalStatusTimeout,
		}
		for _, v := range valid {
			if !v.Valid() {
				t.Errorf("expected %q to be valid", v)
			}
		}
		for _, bad := range []ApprovalStatus{"", "invalid"} {
			if bad.Valid() {
				t.Errorf("expected %q to be invalid", bad)
			}
		}
	})
}

func TestNewULID(t *testing.T) {
	const n = 100
	ids := make([]string, n)
	for i := range ids {
		ids[i] = NewULID()
	}

	// Each ID must parse as a valid ULID.
	for _, id := range ids {
		if _, err := ulid.ParseStrict(id); err != nil {
			t.Errorf("NewULID() returned invalid ULID %q: %v", id, err)
		}
	}

	// ULIDs from a monotonic source must be lexicographically sorted
	// (lexicographic order == chronological order for ULIDs).
	if !sort.StringsAreSorted(ids) {
		t.Error("NewULID() output is not monotonically increasing")
	}
}
