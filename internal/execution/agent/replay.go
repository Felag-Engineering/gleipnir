// Package agent — this file implements spec §6.5 (ADR-055): recovering an
// operator's answer when the server threw away the state needed to accept it.
//
// The situation it exists for: a human takes ten minutes to answer a
// permission prompt; by the time the answer lands, the server's MRTR
// requestState has expired. MRTR is stateless by design, so "start over" is
// always available — and if the server starts over by asking the IDENTICAL
// question, making the human answer it a second time is pure fatigue with no
// information gained. Re-asking a person a question they already answered is
// how approval prompts become reflexive clicks, which is the failure mode
// §6.2 exists to prevent.
//
// So: when a fresh input_required is byte-for-byte the same question, the
// stored answer is replayed against the new requestState automatically. When
// it differs in ANY way, the human sees it again — with the previous question
// and answer attached, so they can tell what changed.
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/schemanorm"
)

// questionFingerprint is a content hash of everything an operator was asked,
// used to decide whether a fresh input_required is the SAME question as the
// one already answered.
//
// What is hashed, and why each part:
//
//   - The message, byte-exactly. It is the sentence the human read and acted
//     on; a server that changes one word is asking something else, and the
//     host is in no position to judge which rewordings are innocent.
//   - The requestedSchema, normalized through internal/schemanorm first. JSON
//     object members are unordered, so a server that re-emits its schema with
//     a different key order is asking the identical question — treating that
//     as a difference would send the human back for no reason. Normalization
//     is the one transformation that is safe to apply here, on schemanorm's
//     own argument: it reorders keys and changes nothing else.
//   - The request COUNT and ORDER of requests as this package holds them.
//     The WIRE correlates an answer to its request by server-assigned ID
//     (ADR-061, go-sdk v1.7.0) — position on the wire means nothing. Only
//     internal/mcp's OWN representation is position-ordered: it sorts
//     InputRequiredResult.InputRequests by ID for deterministic decoding
//     (inputrequired.go), and it is THAT order this function hashes. A
//     server that renames a question's ID without changing its content
//     re-sorts to the same position (same content, same ID-order slot in the
//     common case) and is still the same question; a server that changes
//     WHICH questions it bundles changes this list's order or length either
//     way, which is what this bullet is actually pinning.
//
// Deliberately NOT hashed: the request ID itself (a server MAY legitimately
// mint a new ID for an identical re-ask; content is what makes two questions
// the same, not the label the server put on them this time), requestState
// (it is expected to change — a fresh one is the whole point of the re-ask),
// and elicitationKind (derived from the schema and message this function
// already covers; a server that flips only the kind hint has not changed
// what it is asking a human to decide).
//
// A schema that schemanorm rejects is hashed as its raw bytes instead. That is
// the conservative direction: an unnormalizable schema may then fail equality
// over nothing more than key order, which costs one extra human prompt —
// whereas skipping the schema entirely could replay an answer onto a question
// whose fields changed underneath it.
func questionFingerprint(requests []mcp.InputRequest) string {
	h := sha256.New()
	// Length-prefix every field. Without it, {message:"ab", schema:"c"} and
	// {message:"a", schema:"bc"} hash identically, and a server choosing where
	// to put the boundary is a server choosing collisions.
	writeField := func(b []byte) {
		h.Write([]byte(strconv.Itoa(len(b))))
		h.Write([]byte{':'})
		h.Write(b)
	}

	writeField([]byte(strconv.Itoa(len(requests))))
	for _, r := range requests {
		writeField([]byte(r.Message))
		writeField(canonicalSchemaBytes(r.RequestedSchema))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalSchemaBytes returns schema with object keys recursively sorted, or
// the input unchanged when it cannot be normalized. See questionFingerprint
// for why the fallback errs toward inequality.
func canonicalSchemaBytes(schema json.RawMessage) []byte {
	if len(schema) == 0 {
		return nil
	}
	normalized, err := schemanorm.Normalize(schema)
	if err != nil {
		return schema
	}
	return normalized
}

// answeredQuestion is one operator answer held for possible replay: the
// fingerprint of what was asked, and what came back.
//
// replayed records that the answer has already been spent on one silent
// retry. A server that answers the replay with the same question AGAIN is not
// suffering an expired-state hiccup — it is looping — and the honest response
// is to stop rather than to keep feeding it an answer it evidently will not
// accept. One replay per distinct question is the whole allowance.
//
// kind is the classification (permission/information) THIS answer settled
// under. A replay is only ever spent when the fresh bundle's kind still
// matches this one — a permission ask never replays at all, regardless of
// fingerprint match (security review findings 1-3) — so the kind travels with
// the answer, not just the content hash.
//
// requestID is the persisted tool_input_requests row this answer settled —
// the ORIGINAL human decision a later replay's decision record points back to
// (decision.Record.ReplayOfRequestID).
type answeredQuestion struct {
	fingerprint string
	kind        model.ElicitationKind
	requestID   string
	requests    []mcp.InputRequest
	answers     []mcp.InputResponse
	replayed    bool
}

// matches reports whether requests ask the same question this answer answered.
func (a *answeredQuestion) matches(fingerprint string) bool {
	return a != nil && a.fingerprint == fingerprint
}

// ReplayContext is what an operator is shown when a server re-asks DIFFERENTLY
// after an answer was already given. Without it the second prompt looks like a
// duplicate — the operator has no way to see that the question changed, which
// is exactly the case where a careless "approve" is most dangerous.
//
// It is persisted alongside the new request (tool_input_requests.replay_context)
// rather than only logged, because the operator answering it may be a different
// person on a different day than the one who answered the first.
//
// Both PriorQuestions and the prior answer's content are server- and
// operator-supplied; the same untrusted-content rule as the live request
// applies to rendering them.
type ReplayContext struct {
	// PriorQuestions is what was asked the first time.
	PriorQuestions []PersistedInputRequest `json:"prior_questions"`

	// PriorAnswers is what the operator answered, preserved verbatim. It is
	// NOT replayed — the question changed, so this is reference material for
	// the human, not an input to the retry.
	PriorAnswers []mcp.InputResponse `json:"prior_answers"`

	// Reason is a short host-authored (therefore trusted) explanation of why
	// the operator is seeing a second prompt: reasonQuestionChanged (the
	// content actually differs) or reasonIdenticalReask (byte-identical, but
	// not eligible for a silent replay — a permission ask, or an information
	// ask whose kind flipped under an unchanged fingerprint).
	Reason string `json:"reason"`
}

// reasonQuestionChanged and reasonIdenticalReask are the two Reason values
// this package produces. Constants rather than inline strings so the UI can
// branch on them without matching prose.
const (
	reasonQuestionChanged = "the tool re-asked a different question after your answer"

	// reasonIdenticalReask fires when the fresh bundle matches the prior one
	// byte-for-byte but is not eligible for a silent replay (security review
	// follow-up): a permission ask never replays regardless of match, and an
	// information ask whose classified kind flipped is treated as a new ask.
	// Either way the operator answered this EXACT question moments ago, and
	// needs to be told that plainly rather than seeing what looks like an
	// unexplained duplicate.
	reasonIdenticalReask = "identical_reask"
)

// newReplayContext builds the context attached to a re-prompt whose content
// genuinely changed.
func newReplayContext(prior *answeredQuestion) *ReplayContext {
	return newReplayContextWithReason(prior, reasonQuestionChanged)
}

// newReplayContextWithReason builds the context attached to a re-prompt,
// with an explicit reason -- used directly for reasonIdenticalReask, where
// the content did NOT change but a silent replay was not available.
func newReplayContextWithReason(prior *answeredQuestion, reason string) *ReplayContext {
	if prior == nil {
		return nil
	}
	return &ReplayContext{
		PriorQuestions: toPersistedRequests(prior.requests),
		PriorAnswers:   prior.answers,
		Reason:         reason,
	}
}

// DecodeReplayContext parses a tool_input_requests.replay_context blob. The API
// layer uses it to show what changed since the operator's previous answer.
func DecodeReplayContext(payload string) (*ReplayContext, error) {
	if payload == "" {
		return nil, nil
	}
	var rc ReplayContext
	if err := json.Unmarshal([]byte(payload), &rc); err != nil {
		return nil, err
	}
	return &rc, nil
}
