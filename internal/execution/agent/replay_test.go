package agent

import (
	"encoding/json"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/mcp"
)

// req is a terse constructor for one elicitation.
func req(message, schema string) mcp.InputRequest {
	return mcp.InputRequest{Message: message, RequestedSchema: json.RawMessage(schema)}
}

// The equality rule §6.5 rests on: same question ⇒ replay silently, any
// difference ⇒ ask the human again. Getting either direction wrong is a real
// failure — a false "equal" replays an answer onto a question nobody agreed
// to, a false "different" fatigues an operator for nothing — so both are
// pinned here.
func TestQuestionFingerprint(t *testing.T) {
	base := []mcp.InputRequest{
		req("Deploy to production?", `{"type":"object","properties":{}}`),
	}

	tests := []struct {
		name      string
		requests  []mcp.InputRequest
		wantEqual bool
	}{
		{
			name:      "byte-identical",
			requests:  []mcp.InputRequest{req("Deploy to production?", `{"type":"object","properties":{}}`)},
			wantEqual: true,
		},
		{
			// The whole reason normalization runs first. JSON members are
			// unordered, so this is the same schema; treating it as a change
			// would re-prompt a human over nothing.
			name: "reordered schema keys",
			requests: []mcp.InputRequest{
				req("Deploy to production?", `{"properties":{},"type":"object"}`),
			},
			wantEqual: true,
		},
		{
			name: "reordered nested schema keys",
			requests: []mcp.InputRequest{
				req("Pick a target", `{"properties":{"env":{"enum":["prod","dev"],"type":"string"}},"type":"object"}`),
			},
			wantEqual: false, // differs from base by more than key order; see the paired case below
		},
		{
			name: "whitespace-only schema difference",
			requests: []mcp.InputRequest{
				req("Deploy to production?", "{\n  \"type\": \"object\",\n  \"properties\": {}\n}"),
			},
			wantEqual: true,
		},
		{
			// A new field means the operator is consenting to something they
			// were not shown the first time.
			name: "added schema field",
			requests: []mcp.InputRequest{
				req("Deploy to production?", `{"type":"object","properties":{"force":{"type":"boolean"}}}`),
			},
			wantEqual: false,
		},
		{
			name: "changed message",
			requests: []mcp.InputRequest{
				req("Deploy to production NOW?", `{"type":"object","properties":{}}`),
			},
			wantEqual: false,
		},
		{
			// Not even a case difference is forgiven: the host is in no
			// position to rule on which rewordings are innocent.
			name: "message differing only in case",
			requests: []mcp.InputRequest{
				req("deploy to production?", `{"type":"object","properties":{}}`),
			},
			wantEqual: false,
		},
		{
			name: "extra request appended",
			requests: []mcp.InputRequest{
				req("Deploy to production?", `{"type":"object","properties":{}}`),
				req("Also restart the workers?", `{"type":"object","properties":{}}`),
			},
			wantEqual: false,
		},
		{
			name:      "no schema at all",
			requests:  []mcp.InputRequest{req("Deploy to production?", "")},
			wantEqual: false,
		},
	}

	want := questionFingerprint(base)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := questionFingerprint(tc.requests)
			if equal := got == want; equal != tc.wantEqual {
				t.Errorf("fingerprint equality = %v, want %v", equal, tc.wantEqual)
			}
		})
	}
}

// MRTR correlates answers to requests by array position and nothing else, so
// swapping two requests means each stored answer would land on the other's
// question. Order is part of the question's identity.
func TestQuestionFingerprint_MultiRequestOrderMatters(t *testing.T) {
	forward := []mcp.InputRequest{
		req("Delete the staging database?", `{"type":"object","properties":{}}`),
		req("Delete the production database?", `{"type":"object","properties":{}}`),
	}
	reversed := []mcp.InputRequest{forward[1], forward[0]}

	if questionFingerprint(forward) == questionFingerprint(reversed) {
		t.Error("reordered requests fingerprint equal; an answer would replay onto the wrong question")
	}
}

// Normalization must reach nested objects, not just the top level.
func TestQuestionFingerprint_NestedKeyOrderIsEqual(t *testing.T) {
	a := []mcp.InputRequest{req("Pick a target", `{"type":"object","properties":{"env":{"type":"string","enum":["prod","dev"]}}}`)}
	b := []mcp.InputRequest{req("Pick a target", `{"properties":{"env":{"enum":["prod","dev"],"type":"string"}},"type":"object"}`)}

	if questionFingerprint(a) != questionFingerprint(b) {
		t.Error("nested key reordering changed the fingerprint; normalization is not reaching nested objects")
	}
}

// Length-prefixing is what stops a server from moving the boundary between two
// fields to manufacture a collision with a question it never asked.
func TestQuestionFingerprint_FieldBoundariesAreUnambiguous(t *testing.T) {
	a := []mcp.InputRequest{req("ab", `{"x":1}`)}
	b := []mcp.InputRequest{req("a", `b{"x":1}`)}

	if questionFingerprint(a) == questionFingerprint(b) {
		t.Error("fields concatenate ambiguously; the hash is not length-prefixed")
	}
}

// A schema schemanorm refuses (here: duplicate keys) must not silently drop
// out of the hash — that would make two different questions look identical.
// It falls back to raw bytes, which can only ever produce MORE re-prompts.
func TestQuestionFingerprint_UnnormalizableSchemaStillContributes(t *testing.T) {
	dup := `{"a":1,"a":2}`
	other := `{"a":1,"a":3}`

	if questionFingerprint([]mcp.InputRequest{req("m", dup)}) ==
		questionFingerprint([]mcp.InputRequest{req("m", other)}) {
		t.Error("unnormalizable schemas hash equal; a rejected schema is being dropped from the hash")
	}
	if questionFingerprint([]mcp.InputRequest{req("m", dup)}) ==
		questionFingerprint([]mcp.InputRequest{req("m", "")}) {
		t.Error("an unnormalizable schema hashes the same as no schema at all")
	}
}

// answeredQuestion.matches must tolerate a nil receiver: on the first ask
// there is no prior answer, and the loop calls it unconditionally.
func TestAnsweredQuestionMatches(t *testing.T) {
	var nilPrior *answeredQuestion
	if nilPrior.matches("anything") {
		t.Error("a nil prior answer matched; the first ask would replay a non-existent answer")
	}

	prior := &answeredQuestion{fingerprint: "abc"}
	if !prior.matches("abc") {
		t.Error("matching fingerprint reported as different")
	}
	if prior.matches("abd") {
		t.Error("differing fingerprint reported as matching")
	}
}

func TestReplayContextRoundTrip(t *testing.T) {
	prior := &answeredQuestion{
		requests: []mcp.InputRequest{req("Deploy to production?", `{"type":"object"}`)},
		answers:  []mcp.InputResponse{{Action: inputActionAccept, Content: json.RawMessage(`{"ok":true}`)}},
	}

	rc := newReplayContext(prior)
	if rc == nil {
		t.Fatal("newReplayContext returned nil for a non-nil prior answer")
	}
	if rc.Reason != reasonQuestionChanged {
		t.Errorf("Reason = %q, want %q", rc.Reason, reasonQuestionChanged)
	}

	encoded, err := json.Marshal(rc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := DecodeReplayContext(string(encoded))
	if err != nil {
		t.Fatalf("DecodeReplayContext: %v", err)
	}
	if len(decoded.PriorQuestions) != 1 || decoded.PriorQuestions[0].Message != "Deploy to production?" {
		t.Errorf("prior questions did not survive the round trip: %+v", decoded.PriorQuestions)
	}
	if len(decoded.PriorAnswers) != 1 || decoded.PriorAnswers[0].Action != inputActionAccept {
		t.Errorf("prior answers did not survive the round trip: %+v", decoded.PriorAnswers)
	}
}

func TestNewReplayContext_NilPrior(t *testing.T) {
	if rc := newReplayContext(nil); rc != nil {
		t.Errorf("newReplayContext(nil) = %+v, want nil", rc)
	}
}

func TestDecodeReplayContext(t *testing.T) {
	got, err := DecodeReplayContext("")
	if err != nil || got != nil {
		t.Errorf("DecodeReplayContext(\"\") = (%+v, %v), want (nil, nil)", got, err)
	}
	if _, err := DecodeReplayContext("{not json"); err == nil {
		t.Error("DecodeReplayContext accepted malformed JSON")
	}
}
