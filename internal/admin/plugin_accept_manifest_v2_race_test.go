package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle/desiredstate"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

// A newer bundle can replace the pending row after the handler checked the
// reviewed hash but before the accept transaction runs. The transaction must
// notice, refuse, and leave the newer candidate pending.
func TestAcceptManifest_V2_RefusesWhenCandidateChangedInsideTheTransaction(t *testing.T) {
	ctx := context.Background()
	f := newAcceptV2Fixture(t, "active", true)

	reviewed := sha256Hex(f.candidate)
	newer := strings.Replace(f.candidate, "1.1.0", "1.2.0", 1)
	if err := f.store.Queries().UpsertPluginPendingManifest(ctx, db.UpsertPluginPendingManifestParams{
		PluginID: "plugin-v2", CandidateManifest: newer, OldVersion: "1.0.0", NewVersion: "1.2.0",
		CreatedAt: "2026-08-06T12:00:00Z", UpdatedAt: "2026-08-06T12:00:00Z",
	}); err != nil {
		t.Fatalf("overwrite pending manifest: %v", err)
	}

	plugin, _ := f.store.Queries().GetPluginByID(ctx, "plugin-v2")
	pending, _ := f.store.Queries().GetPluginPendingManifest(ctx, "plugin-v2")
	accepted, err := manifestv2.Parse([]byte(f.candidate))
	if err != nil {
		t.Fatalf("parse candidate: %v", err)
	}

	_, err = f.h.commitAcceptedManifestV2(ctx, plugin, pending, accepted, reviewed, "2026-08-06T12:00:00Z")
	if !errors.Is(err, errAcceptCandidateChanged) {
		t.Fatalf("commit error = %v, want errAcceptCandidateChanged", err)
	}

	after, _ := f.store.Queries().GetPluginByID(ctx, "plugin-v2")
	if after.ManifestSnapshot != plugin.ManifestSnapshot {
		t.Error("the snapshot changed although the reviewed candidate was no longer pending")
	}
	still, err := f.store.Queries().GetPluginPendingManifest(ctx, "plugin-v2")
	if err != nil || still.CandidateManifest != newer {
		t.Errorf("the newer pending candidate was lost: %v", err)
	}
}

type conflictingProvisioner struct{ InstanceProvisioner }

func (conflictingProvisioner) ResyncForPlugin(context.Context, *db.Queries, string) (int, error) {
	return 0, fmt.Errorf("lifecycle: resync instance %q: %w", "inst", desiredstate.ErrCASConflict)
}

// A desired-state row that moved under the resync is a conflict the admin can
// retry, not a server fault, and nothing may be committed.
func TestAcceptManifest_V2_ResyncCASConflictIs409(t *testing.T) {
	ctx := context.Background()
	f := newAcceptV2Fixture(t, "active", true)
	f.h.provisioner = conflictingProvisioner{f.h.provisioner}

	rec := f.accept()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), casConflictMsg) {
		t.Errorf("body %q lacks the CAS conflict message", rec.Body.String())
	}
	plugin, _ := f.store.Queries().GetPluginByID(ctx, "plugin-v2")
	if strings.Contains(plugin.ManifestSnapshot, acceptV2NewDigest) {
		t.Error("the snapshot was committed although the resync failed")
	}
	if _, err := f.store.Queries().GetPluginPendingManifest(ctx, "plugin-v2"); err != nil {
		t.Errorf("pending manifest was consumed: %v", err)
	}
}
