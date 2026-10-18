package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
)

// fakeInputRouter answers AskInput from a script. release, when set, holds the
// answer back until the test closes it, so a test can act while the ask is
// out on the channel.
type fakeInputRouter struct {
	answer  InputChannelAnswer
	err     error
	release chan struct{}
	asked   chan InputChannelAsk
}

func newFakeInputRouter(answer InputChannelAnswer, err error) *fakeInputRouter {
	return &fakeInputRouter{answer: answer, err: err, asked: make(chan InputChannelAsk, 4)}
}

func (f *fakeInputRouter) AskInput(ctx context.Context, ask InputChannelAsk) (InputChannelAnswer, error) {
	f.asked <- ask
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return InputChannelAnswer{}, &InputChannelWaitError{Channel: f.answer.Channel, Err: ctx.Err()}
		}
	}
	return f.answer, f.err
}

// routedChannel is the entry the fake router reports, with one skipped entry
// ahead of it.
var routedChannel = InputChannel{
	EntryID:    "entry-slack",
	InstanceID: "inst-slack",
	Assurance:  "authenticated",
	Considered: []decision.Candidate{{EntryID: "entry-sms", InstanceID: "inst-sms", Reason: "assurance_insufficient"}},
}

// newChannelRoutingFixture is newRoutingFixture with the handler routed
// through router, and the instances the record's foreign keys name.
func newChannelRoutingFixture(t *testing.T, router InputChannelRouter) (*db.Store, *capturePublisher, *RunStateMachine, *InputRequiredHandler) {
	t.Helper()
	s, pub, sm, _ := newRoutingFixture(t, time.Minute)
	for _, id := range []string{"inst-slack", "inst-sms"} {
		insertTestInstance(t, s, id)
	}
	w := NewAuditWriter(s.Queries())
	t.Cleanup(func() { _ = w.Close() })
	return s, pub, sm, NewInputRequiredHandler(w, sm, time.Minute, WithInputChannelRouter(router, "aud-1", "deploy-bot"))
}

func insertTestInstance(t *testing.T, s *db.Store, instanceID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	pluginID := "plugin-" + instanceID
	if _, err := s.CreatePlugin(context.Background(), db.CreatePluginParams{
		ID: pluginID, Name: pluginID, PluginVersion: "1.0.0", ManifestSnapshot: "{}",
		TrustedPubkey: "pk", Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	detail := ""
	if _, err := s.CreatePluginInstance(context.Background(), db.CreatePluginInstanceParams{
		ID: instanceID, PluginID: pluginID, InstanceName: instanceID, ConfigJson: "{}",
		SubscriptionScopeJson: "{}", HandshakeVersions: "{}", HealthState: "healthy",
		HealthDetail: &detail, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
}

func onlyDecision(t *testing.T, s *db.Store) decision.Record {
	t.Helper()
	records, err := decision.NewRecorder(s.Queries()).ForRun(context.Background(), "run1")
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("decision records = %+v, want exactly 1", records)
	}
	return records[0]
}

func toolInputStatus(t *testing.T, s *db.Store, requestID string) string {
	t.Helper()
	row, err := s.GetToolInputRequest(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetToolInputRequest: %v", err)
	}
	return row.Status
}

// A channel answer from a linked approver resumes the run, asserts the linked
// user as responder, and is recorded against the entry that settled it.
func TestInputRequiredHandler_Route_ChannelAnswerFromLinkedApprover(t *testing.T) {
	router := newFakeInputRouter(InputChannelAnswer{
		Channel: routedChannel,
		Action:  inputActionAccept,
		Actor:   InputChannelActor{ExternalID: "U123", UserID: testResponder.UserID, Username: testResponder.Username, MayResolve: true},
	}, nil)
	s, _, sm, h := newChannelRoutingFixture(t, router)

	answers, requestID, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	ask := <-router.asked
	if ask.AudienceID != "aud-1" || ask.PolicyName != "deploy-bot" || ask.Kind != elicitationKindPermission || ask.Request.Message != "deploy to prod?" {
		t.Errorf("ask = %+v, want the single permission entry for aud-1", ask)
	}
	if ask.Wait <= 0 || ask.Wait > time.Minute {
		t.Errorf("ask.Wait = %s, want the remaining effective deadline", ask.Wait)
	}

	if len(answers) != 1 || answers[0].Action != inputActionAccept {
		t.Fatalf("answers = %+v, want one accept", answers)
	}
	var meta map[string]responderMetaValue
	if err := json.Unmarshal(answers[0].Meta, &meta); err != nil {
		t.Fatalf("responder meta: %v (%s)", err, answers[0].Meta)
	}
	if got := meta[responderMetaKey]; got.UserID != testResponder.UserID || got.Username != testResponder.Username || got.Gate != "permission" {
		t.Errorf("responder = %+v, want %+v at the permission gate", got, testResponder)
	}
	if sm.Current() != model.RunStatusRunning {
		t.Errorf("run status = %s, want running", sm.Current())
	}
	if status := toolInputStatus(t, s, requestID); status != "resolved" {
		t.Errorf("request status = %q, want resolved", status)
	}

	rec := onlyDecision(t, s)
	if rec.Outcome != decision.OutcomeAnswered || rec.LinkMethod != decision.LinkDirectory || rec.ActorUserID != testResponder.UserID || rec.ActorExternalID != "U123" {
		t.Errorf("record = outcome %q link %q user %q actor %q, want answered by U123 via directory_mapping", rec.Outcome, rec.LinkMethod, rec.ActorUserID, rec.ActorExternalID)
	}
	if rec.ChannelEntryID != "entry-slack" || rec.ChannelInstance != "inst-slack" || rec.ChannelAssurance != "authenticated" {
		t.Errorf("record channel = %q/%q/%q, want the entry that settled", rec.ChannelEntryID, rec.ChannelInstance, rec.ChannelAssurance)
	}
	if len(rec.Considered) != 1 || rec.Considered[0].EntryID != "entry-sms" {
		t.Errorf("Considered = %+v, want the skipped entry", rec.Considered)
	}
}

// An unverified actor's answer carries no responder identity; a decline is
// still handed back to the server, and the record flags it.
func TestInputRequiredHandler_Route_ChannelDeclineFromUnverifiedActor(t *testing.T) {
	router := newFakeInputRouter(InputChannelAnswer{
		Channel: routedChannel,
		Action:  inputActionDecline,
		Actor:   InputChannelActor{ExternalID: "U-stranger"},
	}, nil)
	s, _, sm, h := newChannelRoutingFixture(t, router)

	answers, _, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(answers) != 1 || answers[0].Action != inputActionDecline || answers[0].Meta != nil {
		t.Fatalf("answers = %+v, want one decline asserting no responder", answers)
	}
	if sm.Current() != model.RunStatusRunning {
		t.Errorf("run status = %s, want running — a refusal is an answer", sm.Current())
	}
	rec := onlyDecision(t, s)
	if rec.Outcome != decision.OutcomeRejected || rec.LinkMethod != decision.LinkUnverified || rec.ActorExternalID != "U-stranger" || rec.ActorUserID != "" {
		t.Errorf("record = %+v, want an unverified rejection by U-stranger", rec)
	}
	if rec.Severity() != "warning" {
		t.Errorf("severity = %q, want warning for an unverified permission decision", rec.Severity())
	}
}

// A permission granted by an actor the host cannot tie to an approver is
// refused: nothing goes back to the server, the request is cancelled, and the
// run fails, as a plugin-routed policy approval does (#1028).
func TestInputRequiredHandler_Route_UnverifiedChannelGrantIsRefused(t *testing.T) {
	router := newFakeInputRouter(InputChannelAnswer{
		Channel: routedChannel,
		Action:  inputActionAccept,
		Actor:   InputChannelActor{ExternalID: "U-stranger"},
	}, nil)
	s, _, _, h := newChannelRoutingFixture(t, router)

	answers, requestID, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
	var routeErr *InputRoutingError
	if !errors.As(err, &routeErr) {
		t.Fatalf("Route err = %v, want *InputRoutingError", err)
	}
	if answers != nil {
		t.Errorf("answers = %+v, want none sent to the server", answers)
	}
	if routeErr.RequestID == "" || requestID != "" {
		t.Errorf("request IDs = %q (error) / %q (return), want the error to name it", routeErr.RequestID, requestID)
	}
	if status := toolInputStatus(t, s, routeErr.RequestID); status != "cancelled" {
		t.Errorf("request status = %q, want cancelled", status)
	}
	rec := onlyDecision(t, s)
	if rec.Outcome != decision.OutcomeRejected || rec.LinkMethod != decision.LinkUnverified || rec.ChannelEntryID != "entry-slack" {
		t.Errorf("record = %+v, want an unverified rejection on entry-slack", rec)
	}
}

// An information answer from an unverified actor is accepted as the channel's
// claim: a weak channel may supply information.
func TestInputRequiredHandler_Route_ChannelInformationFromUnverifiedActor(t *testing.T) {
	router := newFakeInputRouter(InputChannelAnswer{
		Channel: InputChannel{EntryID: "entry-sms", InstanceID: "inst-sms", Assurance: "weak"},
		Action:  inputActionAccept,
		Content: json.RawMessage(`{"ticket":"OPS-7"}`),
		Actor:   InputChannelActor{ExternalID: "+15550100"},
	}, nil)
	s, _, _, h := newChannelRoutingFixture(t, router)

	answers, _, err := h.Route(context.Background(), routeRequest(informationAsk(), time.Minute))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(answers) != 1 || string(answers[0].Content) != `{"ticket":"OPS-7"}` || answers[0].Meta != nil {
		t.Fatalf("answers = %+v, want the form content with no responder", answers)
	}
	rec := onlyDecision(t, s)
	if rec.Kind != elicitationKindInformation || rec.Outcome != decision.OutcomeAnswered || rec.ChannelAssurance != "weak" || rec.Severity() != "info" {
		t.Errorf("record = %+v (severity %s), want an info-severity answered information record on a weak channel", rec, rec.Severity())
	}
}

func informationAsk() *mcp.InputRequiredResult {
	return &mcp.InputRequiredResult{
		InputRequests: []mcp.InputRequest{{
			ID:              "q1",
			Message:         "which ticket?",
			RequestedSchema: json.RawMessage(`{"type":"object","properties":{"ticket":{"type":"string"}}}`),
		}},
		RequestState: json.RawMessage(`{"cursor":"abc"}`),
	}
}

// With no plugin entry able to take the ask it is asked in-app, and the record
// says why each entry was passed over.
func TestInputRequiredHandler_Route_ChannelFallsBackToInApp(t *testing.T) {
	router := newFakeInputRouter(InputChannelAnswer{}, &InputRouteToInAppError{
		Considered: []decision.Candidate{{EntryID: "entry-sms", InstanceID: "inst-sms", Reason: "assurance_insufficient"}},
	})
	s, pub, _, h := newChannelRoutingFixture(t, router)

	done := runRoute(h, routeRequest(permissionAsk("deploy to prod?"), time.Minute))
	requestID := awaitPendingToolInputID(t, pub, s)
	<-router.asked
	resolveWhenRegistered(t, h, requestID, `[{"action":"accept","content":{}}]`)
	if res := <-done; res.err != nil {
		t.Fatalf("Route: %v", res.err)
	}

	rec := onlyDecision(t, s)
	if rec.ChannelEntryID != "gleipnir.in-app" || rec.LinkMethod != decision.LinkSession {
		t.Errorf("record channel %q link %q, want the in-app session answer", rec.ChannelEntryID, rec.LinkMethod)
	}
	if len(rec.Considered) != 1 || rec.Considered[0].EntryID != "entry-sms" {
		t.Errorf("Considered = %+v, want the passed-over entry", rec.Considered)
	}
}

// While a plugin channel holds the ask, an in-app answer is refused rather
// than accepted into a slot nobody reads.
func TestInputRequiredHandler_Route_InAppAnswerRefusedWhileChannelHoldsTheAsk(t *testing.T) {
	router := newFakeInputRouter(InputChannelAnswer{
		Channel: routedChannel,
		Action:  inputActionDecline,
		Actor:   InputChannelActor{ExternalID: "U123", UserID: testResponder.UserID, Username: testResponder.Username, MayResolve: true},
	}, nil)
	router.release = make(chan struct{})
	s, pub, _, h := newChannelRoutingFixture(t, router)

	done := runRoute(h, routeRequest(permissionAsk("deploy to prod?"), time.Minute))
	requestID := awaitPendingToolInputID(t, pub, s)
	<-router.asked

	if err := h.Resolve(requestID, `[{"action":"accept","content":{}}]`, testResponder); !errors.Is(err, ErrUnknownInputRequestID) {
		t.Errorf("in-app Resolve while routed = %v, want ErrUnknownInputRequestID", err)
	}
	close(router.release)
	res := <-done
	if res.err != nil {
		t.Fatalf("Route: %v", res.err)
	}
	if len(res.answers) != 1 || res.answers[0].Action != inputActionDecline {
		t.Errorf("answers = %+v, want the channel's decline", res.answers)
	}
}

// A routed ask nobody answers is a timeout, recorded against the channel.
func TestInputRequiredHandler_Route_ChannelTimeout(t *testing.T) {
	router := newFakeInputRouter(InputChannelAnswer{}, &InputChannelWaitError{Channel: routedChannel, TimedOut: true, Err: errors.New("deadline")})
	s, _, _, h := newChannelRoutingFixture(t, router)

	_, _, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
	var routeErr *InputRoutingError
	if !errors.As(err, &routeErr) {
		t.Fatalf("Route err = %v, want *InputRoutingError", err)
	}
	if status := toolInputStatus(t, s, routeErr.RequestID); status != "timed_out" {
		t.Errorf("request status = %q, want timed_out", status)
	}
	rec := onlyDecision(t, s)
	if rec.Outcome != decision.OutcomeTimeout || rec.ChannelEntryID != "entry-slack" || rec.ActorExternalID != "" {
		t.Errorf("record = %+v, want an actorless timeout on entry-slack", rec)
	}
}

// A routed ask that ends without a usable answer (the channel cancelled it,
// or the host refused what came back) cancels the request.
func TestInputRequiredHandler_Route_ChannelEndsWithoutAnswer(t *testing.T) {
	router := newFakeInputRouter(InputChannelAnswer{}, &InputChannelWaitError{Channel: routedChannel, Err: errors.New("the channel cancelled the ask")})
	s, _, _, h := newChannelRoutingFixture(t, router)

	_, _, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
	var routeErr *InputRoutingError
	if !errors.As(err, &routeErr) || !strings.Contains(err.Error(), "cancelled the ask") {
		t.Fatalf("Route err = %v, want *InputRoutingError naming the cause", err)
	}
	if status := toolInputStatus(t, s, routeErr.RequestID); status != "cancelled" {
		t.Errorf("request status = %q, want cancelled", status)
	}
	rec := onlyDecision(t, s)
	if rec.Outcome != decision.OutcomeCancelled || rec.ChannelEntryID != "entry-slack" || rec.LinkMethod != decision.LinkNone {
		t.Errorf("record = %+v, want a cancelled record on entry-slack", rec)
	}
}

// A bundle of several asks is never routed: it stays in-app, and the record
// says why no plugin entry was asked.
func TestInputRequiredHandler_Route_BundleStaysInApp(t *testing.T) {
	router := newFakeInputRouter(InputChannelAnswer{}, errors.New("must not be called"))
	s, pub, _, h := newChannelRoutingFixture(t, router)

	bundle := informationAsk()
	bundle.InputRequests = append(bundle.InputRequests, mcp.InputRequest{
		ID: "q2", Message: "which env?", RequestedSchema: json.RawMessage(`{"type":"object","properties":{"env":{"type":"string"}}}`),
	})
	done := runRoute(h, routeRequest(bundle, time.Minute))
	requestID := awaitPendingToolInputID(t, pub, s)
	if err := h.Resolve(requestID, `[{"action":"accept","content":{"ticket":"OPS-7"}},{"action":"accept","content":{"env":"prod"}}]`, testResponder); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res := <-done; res.err != nil {
		t.Fatalf("Route: %v", res.err)
	}
	select {
	case ask := <-router.asked:
		t.Errorf("a bundle was routed to a channel: %+v", ask)
	default:
	}
	rec := onlyDecision(t, s)
	if len(rec.Considered) != 1 || rec.Considered[0].Reason != considerMultiEntryBundle {
		t.Errorf("Considered = %+v, want the %s reason", rec.Considered, considerMultiEntryBundle)
	}
}

type routeOutcome struct {
	answers []mcp.InputResponse
	err     error
}

func runRoute(h *InputRequiredHandler, req InputRoutingRequest) <-chan routeOutcome {
	done := make(chan routeOutcome, 1)
	go func() {
		answers, _, err := h.Route(context.Background(), req)
		done <- routeOutcome{answers: answers, err: err}
	}()
	return done
}

// resolveWhenRegistered answers requestID in-app once its waiter exists. On
// the fallback path the waiter is registered only after routing returns, which
// happens after the pause is already observable.
func resolveWhenRegistered(t *testing.T, h *InputRequiredHandler, requestID, body string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := h.Resolve(requestID, body, testResponder)
		if err == nil {
			return
		}
		if !errors.Is(err, ErrUnknownInputRequestID) || time.Now().After(deadline) {
			t.Fatalf("Resolve: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// No eligible entry and no in-app fallback: the request is cancelled and the
// run fails, with the passed-over entries on the record.
func TestInputRequiredHandler_Route_NoChannelAndNoFallbackFails(t *testing.T) {
	router := newFakeInputRouter(InputChannelAnswer{}, &InputNoChannelError{
		Considered: []decision.Candidate{{EntryID: "entry-sms", InstanceID: "inst-sms", Reason: "assurance_insufficient"}},
	})
	s, _, _, h := newChannelRoutingFixture(t, router)

	_, _, err := h.Route(context.Background(), routeRequest(permissionAsk("deploy to prod?"), time.Minute))
	var routeErr *InputRoutingError
	if !errors.As(err, &routeErr) {
		t.Fatalf("Route err = %v, want *InputRoutingError", err)
	}
	if status := toolInputStatus(t, s, routeErr.RequestID); status != "cancelled" {
		t.Errorf("request status = %q, want cancelled", status)
	}
	rec := onlyDecision(t, s)
	if rec.Outcome != decision.OutcomeCancelled || len(rec.Considered) != 1 || rec.Considered[0].EntryID != "entry-sms" {
		t.Errorf("record = %+v, want a cancelled record naming the skipped entry", rec)
	}
}
