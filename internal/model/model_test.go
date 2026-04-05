package model

import (
	"encoding/json"
	"sort"
	"strings"
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
		valid := []TriggerType{TriggerTypeWebhook, TriggerTypeManual, TriggerTypeScheduled, TriggerTypePoll}
		for _, v := range valid {
			if !v.Valid() {
				t.Errorf("expected %q to be valid", v)
			}
		}
		for _, bad := range []TriggerType{"", "invalid", "cron"} {
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
		valid := []OnTimeout{OnTimeoutReject}
		for _, v := range valid {
			if !v.Valid() {
				t.Errorf("expected %q to be valid", v)
			}
		}
		for _, bad := range []OnTimeout{"", "invalid", "approve"} {
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

	t.Run("ErrorCode", func(t *testing.T) {
		valid := []ErrorCode{
			ErrorCodeToolError,
			ErrorCodeAPIError,
			ErrorCodeCancelled,
			ErrorCodeMissingCapability,
			ErrorCodeApprovalRejected,
			ErrorCodeTokenBudgetExceeded,
			ErrorCodeToolCallLimitExceeded,
			ErrorCodeSchemaViolation,
		}
		for _, v := range valid {
			if !v.Valid() {
				t.Errorf("expected %q to be valid", v)
			}
		}
		for _, bad := range []ErrorCode{"", "invalid"} {
			if bad.Valid() {
				t.Errorf("expected %q to be invalid", bad)
			}
		}
	})
}

func TestErrorStepContent_JSON(t *testing.T) {
	c := ErrorStepContent{
		Message: "tool not found: foo.bar",
		Code:    ErrorCodeToolError,
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	want := `{"message":"tool not found: foo.bar","code":"tool_error"}`
	if string(data) != want {
		t.Errorf("JSON = %s, want %s", data, want)
	}

	var got ErrorStepContent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.Message != c.Message {
		t.Errorf("Message = %q, want %q", got.Message, c.Message)
	}
	if got.Code != c.Code {
		t.Errorf("Code = %q, want %q", got.Code, c.Code)
	}
}

func TestModelConfig_JSONRoundTrip(t *testing.T) {
	t.Run("with options", func(t *testing.T) {
		mc := ModelConfig{
			Provider: "anthropic",
			Name:     "claude-sonnet-4-20250514",
			Options:  map[string]any{"temperature": 0.7},
		}
		data, err := json.Marshal(mc)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		var got ModelConfig
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}
		if got.Provider != mc.Provider {
			t.Errorf("Provider = %q, want %q", got.Provider, mc.Provider)
		}
		if got.Name != mc.Name {
			t.Errorf("Name = %q, want %q", got.Name, mc.Name)
		}
		if got.Options["temperature"] != mc.Options["temperature"] {
			t.Errorf("Options[temperature] = %v, want %v", got.Options["temperature"], mc.Options["temperature"])
		}
	})

	t.Run("nil options omitted from json", func(t *testing.T) {
		mc := ModelConfig{Provider: "anthropic", Name: "claude-sonnet-4-20250514"}
		data, err := json.Marshal(mc)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		if strings.Contains(string(data), `"options"`) {
			t.Errorf("expected options key to be absent with omitempty, got: %s", data)
		}
	})
}

func TestModelConfig_Defaults(t *testing.T) {
	if DefaultProvider != "anthropic" {
		t.Errorf("DefaultProvider = %q, want %q", DefaultProvider, "anthropic")
	}
	if DefaultModelName != "claude-sonnet-4-6" {
		t.Errorf("DefaultModelName = %q, want %q", DefaultModelName, "claude-sonnet-4-6")
	}
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
