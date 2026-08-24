package events

import (
	"encoding/json"
	"testing"
)

func TestDiscoverResult_RendersKindsInOrder(t *testing.T) {
	kinds := []Kind{
		{Kind: "a", Guidance: "first"},
		{Kind: "b"},
	}
	result := discoverResult(kinds)

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal discoverResult: %v", err)
	}
	var decoded struct {
		Kinds []eventKindWire `json:"kinds"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Kinds) != 2 {
		t.Fatalf("got %d kinds, want 2", len(decoded.Kinds))
	}
	if decoded.Kinds[0].Kind != "a" || decoded.Kinds[0].Guidance != "first" {
		t.Errorf("kinds[0] = %+v", decoded.Kinds[0])
	}
	if decoded.Kinds[1].Kind != "b" || decoded.Kinds[1].Guidance != "" {
		t.Errorf("kinds[1] = %+v", decoded.Kinds[1])
	}
}

func TestDiscoverResult_OmitsEmptyOptionalFields(t *testing.T) {
	kinds := []Kind{{Kind: "a"}}
	raw, err := json.Marshal(discoverResult(kinds))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	entries, ok := decoded["kinds"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("kinds = %v", decoded["kinds"])
	}
	entry, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("entry = %v", entries[0])
	}
	for _, field := range []string{"guidance", "binding_schema", "operators"} {
		if _, present := entry[field]; present {
			t.Errorf("entry carries empty field %q, want it omitted", field)
		}
	}
}
