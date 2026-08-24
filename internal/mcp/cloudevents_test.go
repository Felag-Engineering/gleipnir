package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// baseCloudEvent returns a well-formed envelope as a map, so each rejection
// test can mutate exactly one field and re-marshal.
func baseCloudEvent() map[string]any {
	return map[string]any{
		"specversion": "1.0",
		"source":      "src",
		"type":        "issue.opened",
		"id":          "abc-123",
		"gleipnirseq": 1,
	}
}

func TestDecodeCloudEvent_Rejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"bad specversion", func(m map[string]any) { m["specversion"] = "0.3" }},
		{"missing id", func(m map[string]any) { delete(m, "id") }},
		{"empty id", func(m map[string]any) { m["id"] = "" }},
		{"missing type", func(m map[string]any) { delete(m, "type") }},
		{"missing source", func(m map[string]any) { delete(m, "source") }},
		{"missing gleipnirseq", func(m map[string]any) { delete(m, "gleipnirseq") }},
		{"garbage gleipnirseq", func(m map[string]any) { m["gleipnirseq"] = "not-a-number" }},
		{"negative gleipnirseq", func(m map[string]any) { m["gleipnirseq"] = -1 }},
		{"oversized data", func(m map[string]any) { m["data"] = strings.Repeat("a", maxCloudEventDataBytes+1) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := baseCloudEvent()
			tc.mutate(m)
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatalf("marshal fixture: %v", err)
			}
			if _, err := DecodeCloudEvent(raw); err == nil {
				t.Errorf("DecodeCloudEvent(%s case) succeeded, want an error", tc.name)
			}
		})
	}
}

func TestDecodeCloudEvent_RoundTrip(t *testing.T) {
	raw := json.RawMessage(`{
		"specversion": "1.0",
		"source": "code-forge",
		"type": "issue.opened",
		"id": "evt-1",
		"time": "2026-08-24T12:00:00Z",
		"data": {"priority": "high"},
		"gleipnirseq": 42
	}`)

	ce, err := DecodeCloudEvent(raw)
	if err != nil {
		t.Fatalf("DecodeCloudEvent: %v", err)
	}
	if ce.SpecVersion != "1.0" {
		t.Errorf("SpecVersion = %q, want 1.0", ce.SpecVersion)
	}
	if ce.Source != "code-forge" {
		t.Errorf("Source = %q, want code-forge", ce.Source)
	}
	if ce.Type != "issue.opened" {
		t.Errorf("Type = %q, want issue.opened", ce.Type)
	}
	if ce.ID != "evt-1" {
		t.Errorf("ID = %q, want evt-1", ce.ID)
	}
	if ce.Sequence != 42 {
		t.Errorf("Sequence = %d, want 42", ce.Sequence)
	}
	if ce.Time.IsZero() {
		t.Error("Time is zero, want the parsed timestamp")
	}
	if string(ce.Data) != `{"priority": "high"}` {
		t.Errorf("Data = %s, want the payload unchanged", ce.Data)
	}
}

// A missing or unparseable time is tolerated: DecodeCloudEvent returns the
// event with a zero Time rather than an error (doc §7.3 — the host
// substitutes observation time downstream).
func TestDecodeCloudEvent_MalformedTimeDecodesToZero(t *testing.T) {
	tests := []string{
		`{"specversion":"1.0","source":"s","type":"t","id":"1","gleipnirseq":1}`,
		`{"specversion":"1.0","source":"s","type":"t","id":"1","time":"","gleipnirseq":1}`,
		`{"specversion":"1.0","source":"s","type":"t","id":"1","time":"not-a-timestamp","gleipnirseq":1}`,
		`{"specversion":"1.0","source":"s","type":"t","id":"1","time":12345,"gleipnirseq":1}`,
	}
	for _, raw := range tests {
		ce, err := DecodeCloudEvent(json.RawMessage(raw))
		if err != nil {
			t.Errorf("DecodeCloudEvent(%s): %v, want no error (malformed time is tolerated)", raw, err)
			continue
		}
		if !ce.Time.IsZero() {
			t.Errorf("DecodeCloudEvent(%s).Time = %v, want the zero value", raw, ce.Time)
		}
	}
}

// A malformed envelope must not affect protocol-era classification or any
// other unrelated field — DecodeCloudEvent is a pure decode, not a validator
// with side effects.
func TestDecodeCloudEvent_BoundsUntrustedFields(t *testing.T) {
	longSource := strings.Repeat("s", maxCloudEventSourceLen+50)
	longType := strings.Repeat("t", maxEventKindNameLen+50)
	longID := strings.Repeat("i", maxCloudEventIDLen+50)

	m := baseCloudEvent()
	m["source"] = longSource
	m["type"] = longType
	m["id"] = longID
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	ce, err := DecodeCloudEvent(raw)
	if err != nil {
		t.Fatalf("DecodeCloudEvent: %v", err)
	}
	const ellipsisOverhead = len("…")
	if len(ce.Source) > maxCloudEventSourceLen+ellipsisOverhead {
		t.Errorf("Source is %d bytes, want at most %d", len(ce.Source), maxCloudEventSourceLen)
	}
	if len(ce.Type) > maxEventKindNameLen+ellipsisOverhead {
		t.Errorf("Type is %d bytes, want at most %d", len(ce.Type), maxEventKindNameLen)
	}
	if len(ce.ID) > maxCloudEventIDLen+ellipsisOverhead {
		t.Errorf("ID is %d bytes, want at most %d", len(ce.ID), maxCloudEventIDLen)
	}
}
