package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// subscribedPolicyYAML names a plugin instance that does not exist. ADR-048
// binding validation is what rejects it; every other check passes.
const subscribedPolicyYAML = `name: subscribed-policy
trigger:
  type: subscribed
  source: no-such-instance
  event_kind: channel_message
capabilities:
  tools: []
  feedback:
    enabled: true
agent:
  task: react to messages
  limits:
    max_tokens_per_run: 20000
    max_tool_calls_per_run: 50
  concurrency: skip
model:
  provider: anthropic
  name: claude-sonnet-4-6
`

// unknownInstanceResolver fails every lookup, which is the first thing
// SubscribedBindingValidator.Validate checks. It returns before touching the
// manifest snapshotter, so the validator under test needs no snapshotter.
type unknownInstanceResolver struct{}

func (unknownInstanceResolver) ResolveInstanceByName(context.Context, string) (string, error) {
	return "", errors.New("no such instance")
}

// TestPolicySaveRunsSubscribedBindingValidation is a wiring test, not a
// validator test.
//
// internal/policy/subscribed_validator_test.go already covers the validator's
// own behaviour thoroughly, and passed throughout the period in which binding
// validation was running on no save path at all (#870): the router built its
// own policy.Service and never received the validator, while the only service
// that had it served webhook rotate/reveal. A unit test that constructs its own
// service cannot observe that, because it supplies the collaborator itself.
//
// So this exercises the real BuildRouter path end to end. It fails if anyone
// removes the wiring again, which is the only thing that would have caught the
// original defect.
func TestPolicySaveRunsSubscribedBindingValidation(t *testing.T) {
	post := func(t *testing.T, router http.Handler, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", strings.NewReader(subscribedPolicyYAML))
		req.Header.Set("Content-Type", "application/yaml")
		req.AddCookie(&http.Cookie{Name: "gleipnir_session", Value: token})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	t.Run("a subscribed policy naming an unknown instance is rejected on save", func(t *testing.T) {
		store := testutil.NewTestStore(t)
		token := insertUserWithSession(t, store, "wiring-admin", "admin")
		validator := policy.NewSubscribedBindingValidator(unknownInstanceResolver{}, nil)

		w := post(t, buildTestRouterWithSubscribedValidator(t, store, validator), token)

		if w.Code == http.StatusCreated {
			t.Fatalf("policy saved with an unvalidated binding — the validator is not wired to the save path (#870); body: %s", w.Body.String())
		}
		// 400, not 422: policy_handler.go:364 maps every validation issue to
		// WriteValidationError(StatusBadRequest), uniformly for structural and
		// I/O-touching checks alike.
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
		}
		if body := w.Body.String(); !strings.Contains(body, "no-such-instance") {
			t.Errorf("response does not name the offending instance, so the operator cannot act on it: %s", body)
		}
	})

	t.Run("a router cannot be built with a policy service that is missing the validator", func(t *testing.T) {
		// Before #871 this state was not just reachable but shipped: a service
		// with a nil validator served the policy routes and saved the policy
		// above with a 201. BuildRouter now refuses it, so "binding validation
		// is off" cannot be reached by forgetting something.
		store := testutil.NewTestStore(t)
		incomplete := policy.NewService(store, nil, nil, nil, nil)

		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("BuildRouter accepted a policy service with missing collaborators; a save path that skips checks is reachable again")
			}
			msg, _ := r.(string)
			if !strings.Contains(msg, "subscribedValidator") {
				t.Errorf("panic does not name the missing collaborator, so the operator cannot tell what to wire: %v", r)
			}
		}()

		api.BuildRouter(api.RouterConfig{
			Services: api.BackgroundServices{Store: store, PolicyService: incomplete},
		})
	})
}
