//go:build substratev2

package assembly

// An upgrade of an installed plugin through the v2 assembly: a signed bundle
// pinning a new image is blocked for review, and accepting it moves what the
// reconciler converges to, so the running generation is rotated to the new
// image. Synchronization is on the observe-pass event, as in the boot test.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/loader"
	"github.com/felag-engineering/gleipnir/internal/plugin/loader/loadertest"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
)

// generationWithDigest returns the generation of instanceID in status that runs
// digest, if any.
func (f *e2eFixture) generationWithDigest(instanceID, status, digest string) (db.PluginContainerGeneration, bool) {
	f.t.Helper()
	gens, err := f.q.ListContainerGenerationsByInstance(context.Background(), instanceID)
	if err != nil {
		f.t.Fatalf("ListContainerGenerationsByInstance: %v", err)
	}
	for _, g := range gens {
		if g.Status == status && g.ImageDigest == digest {
			return g, true
		}
	}
	return db.PluginContainerGeneration{}, false
}

func TestBootV2_AcceptedDigestChangeRotatesGeneration(t *testing.T) {
	ctx := context.Background()
	f := newE2EFixture(t)
	instanceID := f.createInstance()

	base, err := f.q.GetPluginByID(ctx, f.pluginID)
	if err != nil {
		t.Fatalf("GetPluginByID: %v", err)
	}
	baseRow, err := f.q.GetPluginContainerByInstance(ctx, instanceID)
	if err != nil {
		t.Fatalf("GetPluginContainerByInstance: %v", err)
	}

	f.boot()
	f.passUntil("generation 1 active on the installed image", func() bool {
		_, ok := f.generationWithDigest(instanceID, "active", baseRow.ImageDigest)
		return ok && f.converged(instanceID)()
	})

	// A new release signed by the same key and pinning a different image.
	_, portStr, err := net.SplitHostPort(f.pluginAddr)
	if err != nil {
		t.Fatalf("plugin address %q: %v", f.pluginAddr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("plugin port %q: %v", portStr, err)
	}
	next := loadertest.Build(t, loadertest.Options{
		Name: e2ePluginName, Version: "1.1.0", Seed: "e2e-image-next", Key: f.key,
		Manifest: func(digest string) []byte { return []byte(e2eManifest(port, digest)) },
	})
	f.rt.PendingImages = []container.ImageInfo{{ID: next.Digest}}
	_, err = f.a.AdminDeps().Installer.Install(ctx, next.Path)
	var rejected *loader.InstallRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != "material_change" {
		t.Fatalf("Install of a new image = %v, want a material_change rejection", err)
	}
	if f.rt.Loads != 2 {
		t.Fatalf("image loads = %d, want 2: the candidate image is loaded so accepting needs no second upload", f.rt.Loads)
	}

	// Blocked means the consent and the desired state are exactly as they
	// were, and a further pass changes nothing.
	f.pub.drain()
	f.a.reconciler.(*reconciler.Reconciler).Kick()
	f.pub.await(t, reconciler.EventRotationPassCompleted)
	blocked, _ := f.q.GetPluginByID(ctx, f.pluginID)
	if blocked.ManifestSnapshot != base.ManifestSnapshot {
		t.Fatal("a blocked bundle changed the consented snapshot")
	}
	row, _ := f.q.GetPluginContainerByInstance(ctx, instanceID)
	if row.ImageDigest != baseRow.ImageDigest {
		t.Fatalf("desired digest = %q, want unchanged %q while blocked", row.ImageDigest, baseRow.ImageDigest)
	}
	gens, _ := f.q.ListContainerGenerationsByInstance(ctx, instanceID)
	if len(gens) != 1 {
		t.Fatalf("%d generations while blocked, want 1", len(gens))
	}

	// The admin accepts: the desired image moves and the reconciler rotates.
	if rec := f.do(http.MethodPost, "/plugins/"+f.pluginID+"/accept-manifest", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("accept-manifest = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	row, _ = f.q.GetPluginContainerByInstance(ctx, instanceID)
	if row.ImageDigest != next.Digest {
		t.Fatalf("desired digest after accept = %q, want %q", row.ImageDigest, next.Digest)
	}

	f.passUntil("generation 2 active on the accepted image", func() bool {
		_, ok := f.generationWithDigest(instanceID, "active", next.Digest)
		return ok
	})
	gen2, _ := f.generationWithDigest(instanceID, "active", next.Digest)
	if gen2.Generation != 2 {
		t.Errorf("rotated to generation %d, want 2", gen2.Generation)
	}
	if _, stillActive := f.generationWithDigest(instanceID, "active", baseRow.ImageDigest); stillActive {
		t.Error("the superseded generation is still active")
	}
}
