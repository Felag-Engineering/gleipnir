package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// --- fixtures ---------------------------------------------------------------

var gcFrozen = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

func gcStamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// gcFixture stands up a real store with one plugin and n instances, plus a
// reconciler wired for GC over a Fake runtime.
//
// The store is real rather than a double because half of what GC promises is a
// property of the queries — "no live generation references this digest" is a
// SQL predicate over a status set, and a hand-written double would be asserting
// the test author's reading of it rather than the query's.
func gcFixture(t *testing.T, instances int) (*db.Store, *container.Fake, *Reconciler, []string) {
	t.Helper()
	s := testutil.NewTestStore(t)

	if _, err := s.DB().Exec(
		`INSERT INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, version, created_at, updated_at)
		 VALUES ('pl1', 'slack', '1.0.0', '{}', 'pubkey', 'active', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert plugin: %v", err)
	}

	ids := make([]string, instances)
	for i := range ids {
		ids[i] = fmt.Sprintf("inst%d", i)
		if _, err := s.DB().Exec(
			`INSERT INTO plugin_instances(id, plugin_id, instance_name, config_json, subscription_scope_json, handshake_versions, health_state, version, created_at, updated_at)
			 VALUES (?, 'pl1', ?, '{}', '{}', '{}', 'healthy', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
			ids[i], "instance-"+ids[i],
		); err != nil {
			t.Fatalf("insert plugin instance: %v", err)
		}
	}

	fake := container.NewFake()
	alloc, err := NewSubnetAllocator(s.Queries(), netip.MustParsePrefix("10.83.0.0/16"), func() string { return gcStamp(gcFrozen) })
	if err != nil {
		t.Fatalf("NewSubnetAllocator: %v", err)
	}
	r, err := New(Config{
		Runtime: fake,
		Store:   s.Queries(),
		GC:      s.Queries(),
		Subnets: alloc,
		Now:     func() time.Time { return gcFrozen },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, fake, r, ids
}

// insertImage records a loaded image and makes the Fake report it present, so
// "the row says it exists" and "the daemon says it exists" agree at the start
// of every test — the interesting cases are the ones that make them disagree.
func insertImage(t *testing.T, s *db.Store, fake *container.Fake, digest string, size int64) {
	t.Helper()
	sz := size
	if _, err := s.Queries().UpsertContainerImage(context.Background(), db.UpsertContainerImageParams{
		Digest:    digest,
		Reference: "ghcr.io/example/plugin:1.0.0",
		PluginID:  strPtrGC("pl1"),
		SizeBytes: &sz,
		LoadedAt:  gcStamp(gcFrozen.Add(-72 * time.Hour)),
	}); err != nil {
		t.Fatalf("UpsertContainerImage(%s): %v", digest, err)
	}
	if fake != nil {
		fake.AddImage(container.ImageInfo{ID: digest, SizeBytes: size})
	}
}

func insertGeneration(t *testing.T, s *db.Store, instanceID string, generation int64, digest, status string) db.PluginContainerGeneration {
	t.Helper()
	row, err := s.Queries().CreateContainerGeneration(context.Background(), db.CreateContainerGenerationParams{
		ID:               fmt.Sprintf("gen-%s-%d", instanceID, generation),
		PluginInstanceID: instanceID,
		Generation:       generation,
		ImageDigest:      digest,
		ConfigHash:       "cfg",
		TokenHash:        fmt.Sprintf("hash-%s-%d", instanceID, generation),
		Status:           status,
		CreatedAt:        gcStamp(gcFrozen.Add(-time.Hour)),
		UpdatedAt:        gcStamp(gcFrozen.Add(-time.Hour)),
	})
	if err != nil {
		t.Fatalf("CreateContainerGeneration: %v", err)
	}
	return row
}

func insertDesiredContainer(t *testing.T, s *db.Store, instanceID, digest string) {
	t.Helper()
	if _, err := s.Queries().CreatePluginContainer(context.Background(), db.CreatePluginContainerParams{
		ID:               "pc-" + instanceID,
		PluginInstanceID: instanceID,
		ImageRef:         "ghcr.io/example/plugin:1.0.0",
		ImageDigest:      digest,
		ConfigHash:       "cfg",
		NetworkName:      "gleipnir-plugin-" + instanceID,
		DesiredState:     "running",
		CreatedAt:        gcStamp(gcFrozen),
		UpdatedAt:        gcStamp(gcFrozen),
	}); err != nil {
		t.Fatalf("CreatePluginContainer: %v", err)
	}
}

func revokeToken(t *testing.T, s *db.Store, genID string, at time.Time) {
	t.Helper()
	n, err := s.Queries().RevokeContainerGenerationToken(context.Background(), db.RevokeContainerGenerationTokenParams{
		ID:             genID,
		TokenRevokedAt: strPtrGC(gcStamp(at)),
		UpdatedAt:      gcStamp(at),
	})
	if err != nil {
		t.Fatalf("RevokeContainerGenerationToken: %v", err)
	}
	if n != 1 {
		t.Fatalf("RevokeContainerGenerationToken affected %d rows, want 1", n)
	}
}

func strPtrGC(s string) *string { return &s }

func tokenHashOf(t *testing.T, s *db.Store, genID string) string {
	t.Helper()
	var hash string
	if err := s.DB().QueryRow(`SELECT token_hash FROM plugin_container_generations WHERE id = ?`, genID).Scan(&hash); err != nil {
		t.Fatalf("reading token_hash for %s: %v", genID, err)
	}
	return hash
}

// --- image GC ---------------------------------------------------------------

// The core safety claim, stated as a table over every generation status. The
// draining row is the one that matters: during a rotation the OLD generation
// keeps serving until the new one passes its gate, so its image is in use in
// exactly the window it looks stalest.
func TestReconcileGC_ImageReferenceSafety(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		wantReclaimed bool
	}{
		{name: "pending generation holds its image", status: "pending"},
		{name: "starting generation holds its image", status: "starting"},
		{name: "healthy generation holds its image", status: "healthy"},
		{name: "active generation holds its image", status: "active"},
		{name: "draining generation still holds its image", status: "draining"},
		{name: "stopped generation releases its image", status: "stopped", wantReclaimed: true},
		{name: "failed generation releases its image", status: "failed", wantReclaimed: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, fake, r, ids := gcFixture(t, 1)
			insertImage(t, s, fake, "sha256:aaa", 1000)
			insertGeneration(t, s, ids[0], 1, "sha256:aaa", tc.status)

			result, err := r.ReconcileGC(context.Background())
			if err != nil {
				t.Fatalf("ReconcileGC: %v", err)
			}

			if got := result.ImagesReclaimed > 0; got != tc.wantReclaimed {
				t.Errorf("reclaimed = %v, want %v (status %q)", got, tc.wantReclaimed, tc.status)
			}
			if _, err := fake.ImageInspect(context.Background(), "sha256:aaa"); tc.wantReclaimed {
				if !errors.Is(err, container.ErrImageNotFound) {
					t.Error("image survived on the daemon after being reclaimed")
				}
			} else if err != nil {
				t.Errorf("image was removed while a %q generation still runs it: %v", tc.status, err)
			}
		})
	}
}

// A mid-rotation instance has two generations on two digests. Only the retired
// one is reclaimable, and getting this wrong would pull the image out from
// under a container that is still finishing work.
func TestReconcileGC_MidRotationKeepsBothLiveImages(t *testing.T) {
	s, fake, r, ids := gcFixture(t, 1)
	insertImage(t, s, fake, "sha256:old", 100)
	insertImage(t, s, fake, "sha256:new", 200)
	insertImage(t, s, fake, "sha256:retired", 300)

	insertGeneration(t, s, ids[0], 1, "sha256:retired", "stopped")
	insertGeneration(t, s, ids[0], 2, "sha256:old", "draining")
	insertGeneration(t, s, ids[0], 3, "sha256:new", "active")

	result, err := r.ReconcileGC(context.Background())
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}

	if result.ImagesReclaimed != 1 {
		t.Fatalf("reclaimed %d images, want only the retired one: %+v", result.ImagesReclaimed, result.ImagesReclaimable)
	}
	if len(fake.ImageRemovals) != 1 || fake.ImageRemovals[0] != "sha256:retired" {
		t.Errorf("removed %v, want only sha256:retired", fake.ImageRemovals)
	}
	if result.BytesReclaimed != 300 {
		t.Errorf("BytesReclaimed = %d, want 300", result.BytesReclaimed)
	}
}

// A bounded pass reclaims its budget and SAYS how much it left behind. A
// bounded pass that reports nothing about what it skipped reads as "there was
// nothing else to do", which is the reading that makes a backlog invisible.
func TestReconcileGC_BoundedPerPass(t *testing.T) {
	s, fake, r, _ := gcFixture(t, 1)
	r.imagesPerPass = 2
	for i := 0; i < 5; i++ {
		insertImage(t, s, fake, fmt.Sprintf("sha256:img%d", i), 10)
	}

	first, err := r.ReconcileGC(context.Background())
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}
	if first.ImagesReclaimed != 2 {
		t.Errorf("first pass reclaimed %d, want the budget of 2", first.ImagesReclaimed)
	}
	if first.ImagesDeferred != 3 {
		t.Errorf("ImagesDeferred = %d, want 3", first.ImagesDeferred)
	}
	if len(first.ImagesReclaimable) != 5 {
		t.Errorf("ImagesReclaimable = %d, want all 5 reported", len(first.ImagesReclaimable))
	}

	// Convergence: repeated passes drain the backlog rather than stalling on it.
	for i := 0; i < 3; i++ {
		if _, err := r.ReconcileGC(context.Background()); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	final, err := r.ReconcileGC(context.Background())
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}
	if len(final.ImagesReclaimable) != 0 {
		t.Errorf("after draining, %d images are still reclaimable", len(final.ImagesReclaimable))
	}
}

// The daemon refusing a removal means Gleipnir's records disagreed with it. The
// accounting row must survive that refusal — dropping it would leave bytes on
// disk that nothing would ever look at again.
func TestReconcileGC_DaemonRefusalKeepsTheRecord(t *testing.T) {
	s, fake, r, _ := gcFixture(t, 1)
	insertImage(t, s, fake, "sha256:aaa", 100)
	fake.RemoveImageErr = errors.New("conflict: unable to delete: image is being used by running container")

	result, err := r.ReconcileGC(context.Background())
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}
	if result.Errors != 1 {
		t.Errorf("Errors = %d, want 1", result.Errors)
	}
	if result.ImagesReclaimed != 0 {
		t.Errorf("counted %d reclaims despite the refusal", result.ImagesReclaimed)
	}
	if _, err := s.Queries().GetContainerImage(context.Background(), "sha256:aaa"); err != nil {
		t.Errorf("the accounting row was dropped after a refused removal: %v", err)
	}
}

// An image already gone from the daemon is the goal state, not a failure: the
// record is cleared so the pass converges instead of retrying forever.
func TestReconcileGC_AlreadyGoneImageIsReclaimed(t *testing.T) {
	s, _, r, _ := gcFixture(t, 1)
	insertImage(t, s, nil, "sha256:ghost", 100) // recorded, never present on the Fake

	result, err := r.ReconcileGC(context.Background())
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}
	if result.ImagesReclaimed != 1 || result.Errors != 0 {
		t.Fatalf("result = %+v, want one clean reclaim", result)
	}
	if _, err := s.Queries().GetContainerImage(context.Background(), "sha256:ghost"); err == nil {
		t.Error("the record survived; the next pass would keep finding it")
	}
}

// The pre-removal re-check closes the gap between "the list said nothing needs
// this" and the socket call. A rotation starting inside that gap is the case,
// and it is reported rather than silently absorbed.
func TestReconcileGC_RecheckRetainsAnImageClaimedMidPass(t *testing.T) {
	s, fake, r, ids := gcFixture(t, 1)
	insertImage(t, s, fake, "sha256:aaa", 100)

	r.gc = &claimingGCStore{
		GCStore: s.Queries(),
		onList: func() {
			// A rotation begins on this digest between the list and the removal.
			insertGeneration(t, s, ids[0], 1, "sha256:aaa", "pending")
		},
	}

	result, err := r.ReconcileGC(context.Background())
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}
	if result.ImagesRetainedByRecheck != 1 {
		t.Errorf("ImagesRetainedByRecheck = %d, want 1", result.ImagesRetainedByRecheck)
	}
	if result.ImagesReclaimed != 0 {
		t.Error("an image a pending generation claimed was reclaimed anyway")
	}
	if _, err := fake.ImageInspect(context.Background(), "sha256:aaa"); err != nil {
		t.Errorf("the image was removed from the daemon: %v", err)
	}
}

// claimingGCStore runs a hook after the unreferenced-image list, so a test can
// make the world change inside the window the re-check exists to cover.
type claimingGCStore struct {
	GCStore
	onList func()
}

func (c *claimingGCStore) ListUnreferencedContainerImages(ctx context.Context) ([]db.PluginContainerImage, error) {
	rows, err := c.GCStore.ListUnreferencedContainerImages(ctx)
	if c.onList != nil {
		c.onList()
	}
	return rows, err
}

// --- subnet cleanup ---------------------------------------------------------

// The conjunction is the point: an allocation is releasable only when nothing
// claims the instance AND no network for it survives.
func TestOrphanedSubnets(t *testing.T) {
	alloc := func(id string) db.PluginContainerSubnet {
		return db.PluginContainerSubnet{PluginInstanceID: id}
	}
	network := func(id string) container.NetworkInfo {
		return container.NetworkInfo{Labels: map[string]string{LabelManaged: ManagedValue, LabelInstance: id}}
	}

	tests := []struct {
		name        string
		allocations []db.PluginContainerSubnet
		desired     []db.PluginContainer
		networks    []container.NetworkInfo
		want        []string
	}{
		{
			name:        "a desired row claims its allocation",
			allocations: []db.PluginContainerSubnet{alloc("a")},
			desired:     []db.PluginContainer{{PluginInstanceID: "a"}},
		},
		{
			name:        "a surviving network claims its allocation even with no desired row",
			allocations: []db.PluginContainerSubnet{alloc("a")},
			networks:    []container.NetworkInfo{network("a")},
		},
		{
			name:        "neither claims it, so it is orphaned",
			allocations: []db.PluginContainerSubnet{alloc("a")},
			want:        []string{"a"},
		},
		{
			name:        "output is sorted so two passes over an unchanged world read the same",
			allocations: []db.PluginContainerSubnet{alloc("c"), alloc("a"), alloc("b")},
			want:        []string{"a", "b", "c"},
		},
		{
			name:        "an unlabelled network claims nothing",
			allocations: []db.PluginContainerSubnet{alloc("a")},
			networks:    []container.NetworkInfo{{Labels: map[string]string{LabelManaged: ManagedValue}}},
			want:        []string{"a"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := orphanedSubnets(tc.allocations, tc.desired, tc.networks)
			if len(got) != len(tc.want) {
				t.Fatalf("orphans = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("orphans[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestReconcileGC_ReleasesOnlyUnclaimedSubnets(t *testing.T) {
	s, fake, r, ids := gcFixture(t, 3)
	ctx := context.Background()
	for _, id := range ids {
		if _, err := r.subnets.Allocate(ctx, id); err != nil {
			t.Fatalf("Allocate(%s): %v", id, err)
		}
	}

	// ids[0] still has a desired row; ids[1] still has a network; ids[2] has
	// neither and is the only orphan.
	insertDesiredContainer(t, s, ids[0], "sha256:aaa")
	if _, err := fake.CreateNetwork(ctx, container.NetworkOptions{
		Name:     "gleipnir-plugin-" + ids[1],
		Labels:   map[string]string{LabelManaged: ManagedValue, LabelInstance: ids[1]},
		Subnet:   "10.83.1.0/24",
		Internal: true,
	}); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	result, err := r.ReconcileGC(ctx)
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}
	if result.SubnetsReleased != 1 {
		t.Fatalf("released %d subnets, want 1: %v", result.SubnetsReleased, result.SubnetsReleasable)
	}
	for _, id := range ids[:2] {
		if _, err := s.Queries().GetContainerSubnetByInstance(ctx, id); err != nil {
			t.Errorf("instance %s lost its allocation while something still claimed it: %v", id, err)
		}
	}
	if _, err := s.Queries().GetContainerSubnetByInstance(ctx, ids[2]); err == nil {
		t.Error("the orphaned allocation was not released")
	}
}

// Release and allocate racing must never hand two live instances the same
// subnet. The DB's UNIQUE(pool_base, slot) is what makes that true; this pins
// it against real contention, since the allocator reads the taken set and then
// writes without holding a lock across the gap.
func TestSubnetRelease_ConcurrentWithAllocateNeverDoubleAssigns(t *testing.T) {
	s, _, r, ids := gcFixture(t, 24)
	ctx := context.Background()

	// Seed half the instances, then release those while allocating the rest.
	seeded := ids[:12]
	fresh := ids[12:]
	for _, id := range seeded {
		if _, err := r.subnets.Allocate(ctx, id); err != nil {
			t.Fatalf("seeding %s: %v", id, err)
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(ids))
	for _, id := range seeded {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := r.subnets.Release(ctx, id); err != nil {
				errCh <- fmt.Errorf("release %s: %w", id, err)
			}
		}(id)
	}
	for _, id := range fresh {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := r.subnets.Allocate(ctx, id); err != nil {
				errCh <- fmt.Errorf("allocate %s: %w", id, err)
			}
		}(id)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("%v", err)
	}

	rows, err := s.Queries().ListContainerSubnets(ctx)
	if err != nil {
		t.Fatalf("ListContainerSubnets: %v", err)
	}
	bySubnet := make(map[string]string, len(rows))
	byInstance := make(map[string]bool, len(rows))
	for _, row := range rows {
		if other, dup := bySubnet[row.Subnet]; dup {
			t.Fatalf("subnet %s is held by both %s and %s", row.Subnet, other, row.PluginInstanceID)
		}
		bySubnet[row.Subnet] = row.PluginInstanceID
		if byInstance[row.PluginInstanceID] {
			t.Fatalf("instance %s holds two allocations", row.PluginInstanceID)
		}
		byInstance[row.PluginInstanceID] = true
	}
	for _, id := range fresh {
		if !byInstance[id] {
			t.Errorf("instance %s finished the race with no allocation", id)
		}
	}
}

// --- token hygiene ----------------------------------------------------------

func TestReconcileGC_PurgesOnlyLongRevokedTokens(t *testing.T) {
	s, _, r, ids := gcFixture(t, 1)
	ctx := context.Background()

	live := insertGeneration(t, s, ids[0], 1, "sha256:aaa", "active")
	recent := insertGeneration(t, s, ids[0], 2, "sha256:aaa", "stopped")
	old := insertGeneration(t, s, ids[0], 3, "sha256:aaa", "stopped")

	revokeToken(t, s, recent.ID, gcFrozen.Add(-time.Hour)) // inside the window
	revokeToken(t, s, old.ID, gcFrozen.Add(-48*time.Hour)) // outside it
	liveHash := tokenHashOf(t, s, live.ID)
	recentHash := tokenHashOf(t, s, recent.ID)

	result, err := r.ReconcileGC(ctx)
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}
	if result.TokensPurged != 1 {
		t.Fatalf("purged %d tokens, want only the one past retention", result.TokensPurged)
	}

	if got := tokenHashOf(t, s, live.ID); got != liveHash {
		t.Error("an unrevoked generation's token was purged; a serving container would stop authenticating")
	}
	if got := tokenHashOf(t, s, recent.ID); got != recentHash {
		t.Error("a token revoked inside the retention window was purged; the end of that generation is now unattributable")
	}
	if got := tokenHashOf(t, s, old.ID); !strings.HasPrefix(got, purgedTokenPrefix) {
		t.Errorf("token_hash = %q, want a %q tombstone", got, purgedTokenPrefix)
	}
}

// The row survives the purge. Deleting it would discard the rotation history an
// operator reads to answer "what has this instance run", and deleting the
// highest-numbered row would let the next rotation reuse a generation number a
// stale container may still be labelled with.
func TestReconcileGC_PurgeKeepsTheGenerationRow(t *testing.T) {
	s, _, r, ids := gcFixture(t, 1)
	ctx := context.Background()

	gen := insertGeneration(t, s, ids[0], 7, "sha256:aaa", "stopped")
	revokeToken(t, s, gen.ID, gcFrozen.Add(-48*time.Hour))

	if _, err := r.ReconcileGC(ctx); err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}

	latest, err := s.Queries().GetLatestContainerGeneration(ctx, ids[0])
	if err != nil {
		t.Fatalf("GetLatestContainerGeneration: %v", err)
	}
	if latest.Generation != 7 {
		t.Errorf("latest generation = %d, want 7 — the next rotation would reuse a number", latest.Generation)
	}
	if latest.TokenRevokedAt == nil {
		t.Error("the revocation timestamp was lost")
	}
}

// A second pass over already-purged rows must do nothing, or the sweep would
// rewrite every historical row on every pass forever.
func TestReconcileGC_PurgeIsIdempotent(t *testing.T) {
	s, _, r, ids := gcFixture(t, 1)
	ctx := context.Background()

	gen := insertGeneration(t, s, ids[0], 1, "sha256:aaa", "stopped")
	revokeToken(t, s, gen.ID, gcFrozen.Add(-48*time.Hour))

	first, err := r.ReconcileGC(ctx)
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}
	second, err := r.ReconcileGC(ctx)
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}
	if first.TokensPurged != 1 || second.TokensPurged != 0 {
		t.Errorf("purged %d then %d, want 1 then 0", first.TokensPurged, second.TokensPurged)
	}
}

// --- manual posture ---------------------------------------------------------

// Manual mode reports and removes nothing. The operator loaded those images and
// owns those networks; naming what is unreferenced is all Gleipnir may do.
func TestReconcileGC_ManualPostureObservesOnly(t *testing.T) {
	s, fake, _, ids := gcFixture(t, 1)
	ctx := context.Background()

	insertImage(t, s, fake, "sha256:aaa", 100)
	if _, err := s.Queries().AllocateContainerSubnet(ctx, db.AllocateContainerSubnetParams{
		Subnet:           "10.83.0.0/24",
		PluginInstanceID: ids[0],
		PoolBase:         "10.83.0.0/16",
		Slot:             0,
		AllocatedAt:      gcStamp(gcFrozen),
	}); err != nil {
		t.Fatalf("AllocateContainerSubnet: %v", err)
	}
	gen := insertGeneration(t, s, ids[0], 1, "sha256:aaa", "stopped")
	revokeToken(t, s, gen.ID, gcFrozen.Add(-48*time.Hour))

	counting := &countingRuntime{Runtime: container.NewReadOnlyRuntime(fake)}
	alloc, err := NewSubnetAllocator(s.Queries(), netip.MustParsePrefix("10.83.0.0/16"), func() string { return gcStamp(gcFrozen) })
	if err != nil {
		t.Fatalf("NewSubnetAllocator: %v", err)
	}
	manual, err := New(Config{
		Runtime: counting,
		Store:   s.Queries(),
		GC:      s.Queries(),
		Subnets: alloc,
		Posture: container.PostureManual,
		Now:     func() time.Time { return gcFrozen },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := manual.ReconcileGC(ctx)
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}

	if result.Applied {
		t.Error("Applied = true in manual posture")
	}
	if len(result.ImagesReclaimable) != 1 || len(result.SubnetsReleasable) != 1 {
		t.Errorf("manual GC reported %d images / %d subnets, want it to name both",
			len(result.ImagesReclaimable), len(result.SubnetsReleasable))
	}
	if result.ImagesReclaimed != 0 || result.SubnetsReleased != 0 || result.Errors != 0 {
		t.Errorf("manual GC acted: %+v", result)
	}
	if counting.writes() != 0 {
		t.Errorf("manual GC performed %d socket writes", counting.writes())
	}
	if _, err := s.Queries().GetContainerImage(ctx, "sha256:aaa"); err != nil {
		t.Errorf("manual GC deleted the image record: %v", err)
	}
	if _, err := s.Queries().GetContainerSubnetByInstance(ctx, ids[0]); err != nil {
		t.Errorf("manual GC released the subnet: %v", err)
	}

	// Token hygiene is the exception, and deliberately so: a token hash in
	// Gleipnir's own database is not the operator's resource, and leaving
	// credential material to accumulate in the posture chosen for caution
	// would be the wrong way round.
	if result.TokensPurged != 1 {
		t.Errorf("TokensPurged = %d, want 1 — manual posture does not exempt Gleipnir's own material", result.TokensPurged)
	}
}

func TestReconcileGC_RequiresAStore(t *testing.T) {
	r, err := New(Config{Runtime: container.NewFake(), Store: &fakeStore{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.ReconcileGC(context.Background()); err == nil {
		t.Error("ReconcileGC succeeded with no GC store; 'ran and reclaimed zero' and 'never ran' must not read alike")
	}
}
