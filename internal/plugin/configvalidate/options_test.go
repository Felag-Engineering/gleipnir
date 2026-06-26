package configvalidate

import (
	"reflect"
	"testing"
)

// ── OptionsAnnotations tests ──────────────────────────────────────────────────

func TestOptionsAnnotations_NilSchema(t *testing.T) {
	specs, err := OptionsAnnotations(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specs != nil {
		t.Errorf("nil schema: want nil map, got %v", specs)
	}
}

func TestOptionsAnnotations_NoPropertiesKey(t *testing.T) {
	node := parseSchemaNode(t, "type: object\n")
	specs, err := OptionsAnnotations(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specs != nil {
		t.Errorf("schema without properties: want nil map, got %v", specs)
	}
}

func TestOptionsAnnotations_NoAnnotatedProperties(t *testing.T) {
	node := parseSchemaNode(t, `
type: object
properties:
  channel:
    type: string
  name:
    type: string
`)
	specs, err := OptionsAnnotations(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specs != nil {
		t.Errorf("schema without options annotations: want nil map, got %v", specs)
	}
}

func TestOptionsAnnotations_SingleAnnotationScalarString(t *testing.T) {
	node := parseSchemaNode(t, `
type: object
properties:
  user:
    type: string
    x-gleipnir-options:
      source: users
  name:
    type: string
`)
	specs, err := OptionsAnnotations(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]OptionsSpec{
		"user": {Source: "users", Multi: false},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Errorf("OptionsAnnotations = %v, want %v", specs, want)
	}
}

func TestOptionsAnnotations_MultiAnnotationArrayField(t *testing.T) {
	node := parseSchemaNode(t, `
type: object
properties:
  channels:
    type: array
    x-gleipnir-options:
      source: channels
      multi: true
    items:
      type: string
`)
	specs, err := OptionsAnnotations(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]OptionsSpec{
		"channels": {Source: "channels", Multi: true},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Errorf("OptionsAnnotations = %v, want %v", specs, want)
	}
}

func TestOptionsAnnotations_MixedAnnotatedAndPlain(t *testing.T) {
	node := parseSchemaNode(t, `
type: object
properties:
  channels:
    type: array
    x-gleipnir-options:
      source: channels
      multi: true
    items:
      type: string
  user:
    type: string
    x-gleipnir-options:
      source: users
  name:
    type: string
`)
	specs, err := OptionsAnnotations(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]OptionsSpec{
		"channels": {Source: "channels", Multi: true},
		"user":     {Source: "users", Multi: false},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Errorf("OptionsAnnotations = %v, want %v", specs, want)
	}
}

func TestOptionsAnnotations_AnnotationWithoutSource_Skipped(t *testing.T) {
	node := parseSchemaNode(t, `
type: object
properties:
  channel:
    type: string
    x-gleipnir-options:
      multi: true
`)
	specs, err := OptionsAnnotations(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specs != nil {
		t.Errorf("annotation without source must be skipped, got %v", specs)
	}
}

func TestOptionsAnnotations_NonMapAnnotation_Skipped(t *testing.T) {
	// A scalar annotation value (e.g. true) must be skipped — the annotation
	// must be a mapping with at least a "source" key.
	node := parseSchemaNode(t, `
type: object
properties:
  channel:
    type: string
    x-gleipnir-options: true
`)
	specs, err := OptionsAnnotations(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if specs != nil {
		t.Errorf("non-map annotation must be skipped, got %v", specs)
	}
}

func TestOptionsAnnotations_MultiDefaultsFalse(t *testing.T) {
	node := parseSchemaNode(t, `
type: object
properties:
  user:
    type: string
    x-gleipnir-options:
      source: users
`)
	specs, err := OptionsAnnotations(node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec, ok := specs["user"]; !ok {
		t.Fatalf("user property not found in specs")
	} else if spec.Multi {
		t.Errorf("multi without explicit true should default to false, got %v", spec.Multi)
	}
}
