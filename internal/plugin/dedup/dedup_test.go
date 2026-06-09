package dedup_test

import (
	"context"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/dedup"
)

// TestNoop_AlwaysMiss verifies that Noop.Seen always returns (false, nil)
// regardless of how many times the same key is presented.
func TestNoop_AlwaysMiss(t *testing.T) {
	t.Parallel()

	var store dedup.Noop
	ctx := context.Background()

	k := dedup.Key{
		InstanceID: "inst-1",
		EventKind:  "channel_message",
		EventID:    "01J3G4H5K6M7N8P9Q0R1S2T3U4",
	}

	for i := range 5 {
		seen, err := store.Seen(ctx, k)
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		if seen {
			t.Fatalf("iteration %d: Noop returned seen=true; expected false", i)
		}
	}
}

// TestNoop_SatisfiesInterface verifies that Noop implements Store at compile time.
func TestNoop_SatisfiesInterface(t *testing.T) {
	t.Parallel()

	var _ dedup.Store = dedup.Noop{}
}

// TestNoop_DifferentKeys verifies that Noop returns false for all key variants.
func TestNoop_DifferentKeys(t *testing.T) {
	t.Parallel()

	var store dedup.Noop
	ctx := context.Background()

	keys := []dedup.Key{
		{InstanceID: "inst-a", EventKind: "message", EventID: "event-1"},
		{InstanceID: "inst-b", EventKind: "message", EventID: "event-1"},
		{InstanceID: "inst-a", EventKind: "reaction", EventID: "event-1"},
		{InstanceID: "inst-a", EventKind: "message", EventID: "event-2"},
		{InstanceID: "", EventKind: "", EventID: ""},
	}

	for _, k := range keys {
		seen, err := store.Seen(ctx, k)
		if err != nil {
			t.Errorf("key %+v: unexpected error: %v", k, err)
		}
		if seen {
			t.Errorf("key %+v: Noop returned seen=true; expected false", k)
		}
	}
}
