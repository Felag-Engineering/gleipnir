package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
)

// parseErrorEnvelope decodes the full error envelope from a recorder for
// detailed assertions (status + error + detail + issues).
func parseErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) struct {
	Error  string              `json:"error"`
	Detail string              `json:"detail"`
	Issues []httputil.ErrorIssue `json:"issues"`
} {
	t.Helper()
	var env struct {
		Error  string              `json:"error"`
		Detail string              `json:"detail"`
		Issues []httputil.ErrorIssue `json:"issues"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	return env
}

// ─── TestMapLifecycleError ───────────────────────────────────────────────────

func TestMapLifecycleError(t *testing.T) {
	// A minimal PluginHandler is enough to call mapLifecycleError — the method
	// only reads the error and writes to w. No DB or modules are needed.
	h := &PluginHandler{}

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
		wantDetail string
	}{
		// ── sentinel errors (errors.Is) ──────────────────────────────────────
		{
			name:       "503 ErrStoreUnavailable",
			err:        ErrStoreUnavailable,
			wantStatus: http.StatusServiceUnavailable,
			wantMsg:    "plugin management not configured",
			wantDetail: "",
		},
		{
			name:       "404 ErrPluginNotFound",
			err:        ErrPluginNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    "plugin not found",
			wantDetail: "",
		},
		{
			name:       "404 ErrInstanceNotFound",
			err:        ErrInstanceNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    "instance not found",
			wantDetail: "",
		},
		{
			name:       "409 ErrAlreadyInactive",
			err:        ErrAlreadyInactive,
			wantStatus: http.StatusConflict,
			wantMsg:    "instance is already deactivated",
			wantDetail: "",
		},
		{
			name:       "500 ErrRefetchFailed",
			err:        ErrRefetchFailed,
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "state transition succeeded but re-fetch failed",
			wantDetail: "",
		},

		// ── struct error types (errors.As) ───────────────────────────────────
		{
			name:       "409 TerminalStateError",
			err:        TerminalStateError{State: model.PluginHealthStateSignatureInvalid},
			wantStatus: http.StatusConflict,
			wantMsg:    "cannot deactivate instance in terminal state",
			wantDetail: "current state: signature_invalid",
		},
		{
			name:       "409 InflightError Deactivate op",
			err:        InflightError{Count: 3, Op: inflightOpDeactivate},
			wantStatus: http.StatusConflict,
			wantMsg:    "cannot deactivate while tool calls are in progress",
			wantDetail: "3 in-flight calls",
		},
		{
			name:       "409 InflightError Delete op",
			err:        InflightError{Count: 7, Op: inflightOpDelete},
			wantStatus: http.StatusConflict,
			wantMsg:    "instance has in-flight tool calls",
			wantDetail: "7 in-flight calls",
		},
		{
			name:       "409 NotInactiveError",
			err:        NotInactiveError{State: "healthy"},
			wantStatus: http.StatusConflict,
			wantMsg:    "instance is not deactivated",
			wantDetail: "current state: healthy",
		},
		{
			name:       "409 PolicyRefError",
			err:        PolicyRefError{Names: []string{"daily-report", "weekly-sync"}},
			wantStatus: http.StatusConflict,
			wantMsg:    "instance is referenced by policies",
			wantDetail: "daily-report, weekly-sync",
		},
		{
			name:       "409 AudienceRefError",
			err:        AudienceRefError{Names: []string{"ops-channel"}},
			wantStatus: http.StatusConflict,
			wantMsg:    "instance is referenced by audiences",
			wantDetail: "ops-channel",
		},
		{
			name:       "409 InstancesRemainError",
			err:        InstancesRemainError{Names: []string{"prod", "staging"}},
			wantStatus: http.StatusConflict,
			wantMsg:    "all instances must be removed before uninstalling the plugin",
			wantDetail: "prod, staging",
		},

		// ── lifecycleInternalError with distinct public messages ──────────────
		{
			name: "500 lifecycleInternalError: internal error",
			err: &lifecycleInternalError{
				PublicMsg: "internal error",
				Detail:    "",
				Err:       errors.New("db error"),
			},
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "internal error",
			wantDetail: "",
		},
		{
			name: "500 lifecycleInternalError: failed to get plugin",
			err: &lifecycleInternalError{
				PublicMsg: "failed to get plugin",
				Detail:    "",
				Err:       errors.New("db error"),
			},
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to get plugin",
			wantDetail: "",
		},
		{
			name: "500 lifecycleInternalError: failed to get instance",
			err: &lifecycleInternalError{
				PublicMsg: "failed to get instance",
				Detail:    "",
				Err:       errors.New("db error"),
			},
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to get instance",
			wantDetail: "",
		},
		{
			name: "500 lifecycleInternalError: failed to set health state (carries detail)",
			err: &lifecycleInternalError{
				PublicMsg: "failed to set health state",
				Detail:    "CAS conflict on health update",
				Err:       errors.New("CAS conflict on health update"),
			},
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to set health state",
			wantDetail: "CAS conflict on health update",
		},

		// ── fallback: unknown error ───────────────────────────────────────────
		{
			name:       "500 unknown error falls through to default",
			err:        errors.New("unexpected thing"),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "unexpected thing",
			wantDetail: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.mapLifecycleError(rec, tt.err)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			env := parseErrorEnvelope(t, rec)
			if env.Error != tt.wantMsg {
				t.Errorf("error = %q, want %q", env.Error, tt.wantMsg)
			}
			if env.Detail != tt.wantDetail {
				t.Errorf("detail = %q, want %q", env.Detail, tt.wantDetail)
			}
		})
	}
}

// ─── TestMapConfigError ──────────────────────────────────────────────────────

func TestMapConfigError(t *testing.T) {
	h := &PluginHandler{}

	type row struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
		wantDetail string
		// wantIssues is non-nil for WriteValidationError responses; length is asserted.
		wantIssueCount int
	}

	tests := []row{
		// ── sentinel errors (errors.Is) ──────────────────────────────────────
		{
			name:       "404 ErrInstanceNotFound",
			err:        ErrInstanceNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    "instance not found",
		},
		{
			name:       "404 ErrPluginNotFound",
			err:        ErrPluginNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    "plugin not found",
		},
		{
			name:       "404 ErrPropertyNotFound",
			err:        ErrPropertyNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    "property not found in config_schema",
		},
		{
			name:       "400 ErrNoSubscriptionSchema",
			err:        ErrNoSubscriptionSchema,
			wantStatus: http.StatusBadRequest,
			wantMsg:    "plugin declares no subscription_schema",
		},
		{
			name:       "409 ErrCASConflict",
			err:        ErrCASConflict,
			wantStatus: http.StatusConflict,
			wantMsg:    casConflictMsg,
		},

		// ── struct error types (errors.As) ───────────────────────────────────
		{
			name: "500 CorruptManifestError",
			err: CorruptManifestError{
				Detail: "yaml: mapping values are not allowed in this context",
			},
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "corrupt manifest snapshot",
			wantDetail: "yaml: mapping values are not allowed in this context",
		},
		{
			name: "422 configValidationError (field issues → WriteValidationError)",
			err: configValidationError{
				Issues: []configvalidate.FieldError{
					{Field: "api_key", Message: "required"},
					{Field: "timeout", Message: "must be > 0"},
				},
			},
			wantStatus:     http.StatusUnprocessableEntity,
			wantMsg:        "validation failed",
			wantIssueCount: 2,
		},
		{
			name: "400 SentinelRejectedError bulk (WriteValidationError shape)",
			err: SentinelRejectedError{
				Issues: []configvalidate.FieldError{
					{Field: "app_level_token", Message: "value is the redaction sentinel"},
				},
				Single: false,
			},
			wantStatus:     http.StatusBadRequest,
			wantMsg:        "sentinel value rejected",
			wantIssueCount: 1,
		},
		{
			name: "400 SentinelRejectedError single (plain WriteError)",
			err: SentinelRejectedError{
				Single: true,
			},
			wantStatus: http.StatusBadRequest,
			wantMsg:    "value '***' is the redaction sentinel; submit the real secret",
		},

		// ── configInternalError with distinct public messages ─────────────────
		{
			name: "500 configInternalError: validation error (carries detail = err.Error())",
			err: &configInternalError{
				PublicMsg: "validation error",
				Detail:    "schema engine returned unexpected error",
				Err:       errors.New("schema engine returned unexpected error"),
			},
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "validation error",
			wantDetail: "schema engine returned unexpected error",
		},
		{
			name: "500 configInternalError: failed to build scope validator (carries detail)",
			err: &configInternalError{
				PublicMsg: "failed to build scope validator",
				Detail:    "unsupported schema keyword",
				Err:       errors.New("unsupported schema keyword"),
			},
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to build scope validator",
			wantDetail: "unsupported schema keyword",
		},
		{
			name: "500 configInternalError: failed to build config validator (carries detail)",
			err: &configInternalError{
				PublicMsg: "failed to build config validator",
				Detail:    "unsupported schema keyword",
				Err:       errors.New("unsupported schema keyword"),
			},
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to build config validator",
			wantDetail: "unsupported schema keyword",
		},
		{
			name: "500 configInternalError: failed to update subscription scope (empty detail)",
			err: &configInternalError{
				PublicMsg: "failed to update subscription scope",
				Detail:    "",
				Err:       errors.New("db error"),
			},
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to update subscription scope",
			wantDetail: "",
		},
		{
			name: "500 configInternalError: failed to update instance config (empty detail)",
			err: &configInternalError{
				PublicMsg: "failed to update instance config",
				Detail:    "",
				Err:       errors.New("db error"),
			},
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to update instance config",
			wantDetail: "",
		},
		{
			name: "500 configInternalError: failed to get plugin",
			err: &configInternalError{
				PublicMsg: "failed to get plugin",
				Detail:    "",
				Err:       errors.New("db error"),
			},
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "failed to get plugin",
			wantDetail: "",
		},

		// ── fallback ─────────────────────────────────────────────────────────
		{
			name:       "500 unknown error falls through to default",
			err:        errors.New("some unexpected condition"),
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "some unexpected condition",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.mapConfigError(rec, tt.err)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			env := parseErrorEnvelope(t, rec)
			if env.Error != tt.wantMsg {
				t.Errorf("error = %q, want %q", env.Error, tt.wantMsg)
			}
			if env.Detail != tt.wantDetail {
				t.Errorf("detail = %q, want %q", env.Detail, tt.wantDetail)
			}
			if tt.wantIssueCount > 0 && len(env.Issues) != tt.wantIssueCount {
				t.Errorf("issues count = %d, want %d", len(env.Issues), tt.wantIssueCount)
			}
		})
	}
}
