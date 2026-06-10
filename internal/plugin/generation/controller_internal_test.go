package generation

import (
	"context"
	"testing"
)

// TestRemoveCancelEntry_DoesNotAliasInput proves that removeCancelEntry builds
// a fresh backing array when it removes an entry, leaving the caller's original
// slice header and backing array byte-for-byte untouched.
func TestRemoveCancelEntry_DoesNotAliasInput(t *testing.T) {
	noop := func() {}
	orig := []*cancelEntry{
		{id: 1, cancel: context.CancelFunc(noop)},
		{id: 2, cancel: context.CancelFunc(noop)},
		{id: 3, cancel: context.CancelFunc(noop)},
	}
	// Snapshot the original pointer values before the call.
	before := append([]*cancelEntry(nil), orig...)

	out := removeCancelEntry(orig, 2)

	// Result must contain exactly ids 1 and 3 in order.
	if len(out) != 2 {
		t.Fatalf("expected len 2 after removing id 2, got %d", len(out))
	}
	if out[0].id != 1 || out[1].id != 3 {
		t.Fatalf("expected ids [1 3] after removal, got [%d %d]", out[0].id, out[1].id)
	}

	// The original slice must be unchanged — same length and same pointers as
	// the before snapshot, proving the input backing array was not shifted.
	if len(orig) != 3 {
		t.Fatalf("original slice length changed: expected 3, got %d", len(orig))
	}
	for i, ptr := range before {
		if orig[i] != ptr {
			t.Errorf("orig[%d] pointer changed: expected %p, got %p", i, ptr, orig[i])
		}
	}

	// Prove distinct backing arrays: mutating out[0] must not affect orig[0].
	sentinel := &cancelEntry{id: 99}
	out[0] = sentinel
	if orig[0].id != 1 {
		t.Fatalf("mutating out[0] changed orig[0].id to %d (shared backing array)", orig[0].id)
	}
}

// TestRemoveCancelEntry_IDNotFound_ReturnsInputUnchanged verifies the miss
// contract: when no entry matches the id, removeCancelEntry returns the input
// slice unchanged (same length and same element pointers).
func TestRemoveCancelEntry_IDNotFound_ReturnsInputUnchanged(t *testing.T) {
	noop := func() {}
	orig := []*cancelEntry{
		{id: 1, cancel: context.CancelFunc(noop)},
		{id: 2, cancel: context.CancelFunc(noop)},
	}
	before := append([]*cancelEntry(nil), orig...)

	out := removeCancelEntry(orig, 999)

	if len(out) != len(orig) {
		t.Fatalf("expected len %d on miss, got %d", len(orig), len(out))
	}
	for i, ptr := range before {
		if out[i] != ptr {
			t.Errorf("out[%d] pointer differs on miss: expected %p, got %p", i, ptr, out[i])
		}
	}
}
