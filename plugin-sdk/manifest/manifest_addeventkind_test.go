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
