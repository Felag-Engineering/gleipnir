package manifest_test

import (
	"bytes"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// eventKindFixture is a simple filter struct for AddEventKind tests.
type eventKindFixture struct {
	Channel manifest.EqualsField   `json:"channel"`
	Keyword manifest.ContainsField `json:"keyword,omitempty"`
}

// baseManifest returns a minimal valid Manifest to attach event kinds to.
func baseManifest() *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: "v1",
		Name:          "evtkindtest",
		Version:       "0.1.0",
		Services:      manifest.Services{Trigger: "v1"},
		Auth:          manifest.AuthDecl{Mode: "instance_credentials", Strategy: "none"},
	}
}

// TestAddEventKind_PopulatesBindingSchema verifies the happy path: AddEventKind
// with a non-nil filterStruct populates BindingSchema.
func TestAddEventKind_PopulatesBindingSchema(t *testing.T) {
	m := baseManifest()
	err := m.AddEventKind("msg_received", "A message was received", eventKindFixture{}, nil)
	if err != nil {
		t.Fatalf("AddEventKind: %v", err)
	}
	if len(m.EventKinds) != 1 {
		t.Fatalf("len(EventKinds) = %d, want 1", len(m.EventKinds))
	}
	decl := m.EventKinds[0]
	if decl.Kind != "msg_received" {
		t.Errorf("Kind = %q, want %q", decl.Kind, "msg_received")
	}
	if decl.BindingSchema == nil {
		t.Error("BindingSchema is nil, want non-nil (filterStruct was non-nil)")
	}
}

// TestAddEventKind_NilFilterStruct_NoBinding verifies that a nil filterStruct
// results in BindingSchema == nil with no error.
func TestAddEventKind_NilFilterStruct_NoBinding(t *testing.T) {
	m := baseManifest()
	err := m.AddEventKind("simple_event", "no binding", nil, nil)
	if err != nil {
		t.Fatalf("AddEventKind: unexpected error: %v", err)
	}
	if len(m.EventKinds) != 1 {
		t.Fatalf("len(EventKinds) = %d, want 1", len(m.EventKinds))
	}
	if m.EventKinds[0].BindingSchema != nil {
		t.Error("BindingSchema should be nil when filterStruct is nil")
	}
}

// TestMustAddEventKind_NilFilterStruct_DoesNotPanic verifies that
// MustAddEventKind does not panic when filterStruct is nil.
func TestMustAddEventKind_NilFilterStruct_DoesNotPanic(t *testing.T) {
	m := baseManifest()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("MustAddEventKind panicked with nil filterStruct: %v", r)
		}
	}()
	m.MustAddEventKind("no_filter", "no binding schema", nil, nil)
	if len(m.EventKinds) != 1 {
		t.Errorf("len(EventKinds) = %d, want 1", len(m.EventKinds))
	}
}

// TestAddEventKindWithExamples_TypedPayload verifies that a typed struct payload
// round-trips through AddEventKindWithExamples: the decoded node must carry
// the expected "name" and "payload" keys.
func TestAddEventKindWithExamples_TypedPayload(t *testing.T) {
	type msgPayload struct {
		Channel string `yaml:"channel"`
		Text    string `yaml:"text"`
	}

	m := baseManifest()
	err := m.AddEventKindWithExamples("msg_received", "A message", eventKindFixture{}, nil,
		manifest.Example{Name: "incident", Payload: msgPayload{Channel: "#incidents", Text: "alert"}},
	)
	if err != nil {
		t.Fatalf("AddEventKindWithExamples: %v", err)
	}

	if len(m.EventKinds) != 1 {
		t.Fatalf("len(EventKinds) = %d, want 1", len(m.EventKinds))
	}
	decl := m.EventKinds[0]
	if len(decl.Examples) != 1 {
		t.Fatalf("len(Examples) = %d, want 1", len(decl.Examples))
	}

	var decoded map[string]any
	if err := decl.Examples[0].Decode(&decoded); err != nil {
		t.Fatalf("Decode example node: %v", err)
	}
	if decoded["name"] != "incident" {
		t.Errorf("name = %v, want %q", decoded["name"], "incident")
	}
	if _, ok := decoded["payload"]; !ok {
		t.Error("payload key missing from decoded example")
	}
}

// TestAddEventKindWithExamples_EmptyExamples verifies that zero examples produces
// a declaration with nil Examples, matching AddEventKind behaviour.
func TestAddEventKindWithExamples_EmptyExamples(t *testing.T) {
	m := baseManifest()
	if err := m.AddEventKindWithExamples("empty_kind", "no examples", nil, nil); err != nil {
		t.Fatalf("AddEventKindWithExamples: %v", err)
	}
	if len(m.EventKinds) != 1 {
		t.Fatalf("len(EventKinds) = %d, want 1", len(m.EventKinds))
	}
	if m.EventKinds[0].Examples != nil {
		t.Errorf("Examples = %v, want nil for zero examples", m.EventKinds[0].Examples)
	}
}

// TestAddEventKindWithExamples_OrderingPreserved verifies that three examples
// retain their insertion order in the declaration.
func TestAddEventKindWithExamples_OrderingPreserved(t *testing.T) {
	m := baseManifest()
	examples := []manifest.Example{
		{Name: "first", Payload: map[string]string{"k": "a"}},
		{Name: "second", Payload: map[string]string{"k": "b"}},
		{Name: "third", Payload: map[string]string{"k": "c"}},
	}
	if err := m.AddEventKindWithExamples("ordered", "ordering test", nil, nil, examples...); err != nil {
		t.Fatalf("AddEventKindWithExamples: %v", err)
	}
	decl := m.EventKinds[0]
	if len(decl.Examples) != 3 {
		t.Fatalf("len(Examples) = %d, want 3", len(decl.Examples))
	}
	names := []string{"first", "second", "third"}
	for i, node := range decl.Examples {
		var m map[string]any
		if err := node.Decode(&m); err != nil {
			t.Fatalf("Decode examples[%d]: %v", i, err)
		}
		if m["name"] != names[i] {
			t.Errorf("examples[%d].name = %v, want %q", i, m["name"], names[i])
		}
	}
}

// TestAddEventKindWithGuidance_SetsGuidance verifies that
// MustAddEventKindWithGuidance sets the Guidance field on the appended decl.
func TestAddEventKindWithGuidance_SetsGuidance(t *testing.T) {
	m := baseManifest()
	const guidance = "A human posts a message in a watched channel."
	m.MustAddEventKindWithGuidance("channel_message", "Channel message", guidance, eventKindFixture{}, nil)

	if len(m.EventKinds) != 1 {
		t.Fatalf("len(EventKinds) = %d, want 1", len(m.EventKinds))
	}
	decl := m.EventKinds[0]
	if decl.Kind != "channel_message" {
		t.Errorf("Kind = %q, want %q", decl.Kind, "channel_message")
	}
	if decl.Description != "Channel message" {
		t.Errorf("Description = %q, want %q", decl.Description, "Channel message")
	}
	if decl.Guidance != guidance {
		t.Errorf("Guidance = %q, want %q", decl.Guidance, guidance)
	}
}

// TestAddEventKindWithGuidance_RoundTrip verifies that the Guidance field
// survives a Marshal → Unmarshal round-trip.
func TestAddEventKindWithGuidance_RoundTrip(t *testing.T) {
	m := baseManifest()
	const guidance = "Fires when a user sends a 1:1 DM to the bot."
	m.MustAddEventKindWithGuidance("direct_message", "DM to bot", guidance, nil, nil)

	out, err := manifest.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m2 manifest.Manifest
	if err := manifest.Unmarshal(out, &m2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(m2.EventKinds) != 1 {
		t.Fatalf("after round-trip: len(EventKinds) = %d, want 1", len(m2.EventKinds))
	}
	got := m2.EventKinds[0].Guidance
	if got != guidance {
		t.Errorf("round-trip Guidance = %q, want %q", got, guidance)
	}
}

// TestAddEventKind_DeterministicAcrossMarshals builds two manifests with
// identical AddEventKind calls and asserts that manifest.Marshal produces
// byte-equal output for both.
func TestAddEventKind_DeterministicAcrossMarshals(t *testing.T) {
	m1 := baseManifest()
	m2 := baseManifest()

	if err := m1.AddEventKind("msg", "test", eventKindFixture{}, nil); err != nil {
		t.Fatalf("m1.AddEventKind: %v", err)
	}
	if err := m2.AddEventKind("msg", "test", eventKindFixture{}, nil); err != nil {
		t.Fatalf("m2.AddEventKind: %v", err)
	}

	b1, err := manifest.Marshal(m1)
	if err != nil {
		t.Fatalf("Marshal m1: %v", err)
	}
	b2, err := manifest.Marshal(m2)
	if err != nil {
		t.Fatalf("Marshal m2: %v", err)
	}

	if !bytes.Equal(b1, b2) {
		t.Fatalf("marshal not deterministic across identical manifests:\nfirst:\n%s\nsecond:\n%s", b1, b2)
	}
}
