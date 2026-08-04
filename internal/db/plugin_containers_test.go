package db

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

const testNow = "2024-01-01T00:00:00Z"

// containerFixture inserts a plugin and n instances, returning their IDs.
func containerFixture(t *testing.T, s *Store, n int) []string {
	t.Helper()
	insertPlugin(t, s, "pl1")
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "inst" + string(rune('a'+i))
		insertPluginInstance(t, s, ids[i], "pl1")
	}
	return ids
}

func createContainer(t *testing.T, s *Store, id, instanceID string) PluginContainer {
	t.Helper()
	memory := int64(512 << 20)
	cpu := int64(1500)
	row, err := s.Queries().CreatePluginContainer(context.Background(), CreatePluginContainerParams{
		ID:                 id,
		PluginInstanceID:   instanceID,
		ImageRef:           "gleipnir/slack:1.2.0",
		ImageDigest:        "sha256:aaaa",
		ConfigHash:         "cfg-1",
		NetworkName:        "gleipnir-" + instanceID,
		MemoryLimitBytes:   &memory,
		CpuLimitMillicores: &cpu,
		DesiredState:       "running",
		CreatedAt:          testNow,
		UpdatedAt:          testNow,
	})
	if err != nil {
		t.Fatalf("CreatePluginContainer %s: %v", id, err)
	}
	return row
}

func createGeneration(t *testing.T, s *Store, id, instanceID string, generation int64, tokenHash, status string) PluginContainerGeneration {
	t.Helper()
	row, err := s.Queries().CreateContainerGeneration(context.Background(), CreateContainerGenerationParams{
		ID:               id,
		PluginInstanceID: instanceID,
		Generation:       generation,
		ImageDigest:      "sha256:aaaa",
		ConfigHash:       "cfg-1",
		TokenHash:        tokenHash,
		Status:           status,
		CreatedAt:        testNow,
		UpdatedAt:        testNow,
	})
	if err != nil {
		t.Fatalf("CreateContainerGeneration %s: %v", id, err)
	}
	return row
}

func TestPluginContainerDesiredState(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	instances := containerFixture(t, s, 2)

	created := createContainer(t, s, "c1", instances[0])
	if created.Version != 0 {
		t.Errorf("Version = %d, want 0", created.Version)
	}
	if created.MemoryLimitBytes == nil || *created.MemoryLimitBytes != 512<<20 {
		t.Errorf("MemoryLimitBytes = %v, want 536870912", created.MemoryLimitBytes)
	}

	got, err := s.Queries().GetPluginContainerByInstance(ctx, instances[0])
	if err != nil {
		t.Fatalf("GetPluginContainerByInstance: %v", err)
	}
	if got.ID != "c1" {
		t.Errorf("ID = %q, want c1", got.ID)
	}

	// One desired container per instance: a second row for the same instance
	// is a modelling error, not a second container.
	if _, err := s.Queries().CreatePluginContainer(ctx, CreatePluginContainerParams{
		ID:               "c2",
		PluginInstanceID: instances[0],
		ImageRef:         "gleipnir/slack:1.2.0",
		ImageDigest:      "sha256:bbbb",
		ConfigHash:       "cfg-2",
		NetworkName:      "other",
		DesiredState:     "running",
		CreatedAt:        testNow,
		UpdatedAt:        testNow,
	}); err == nil {
		t.Error("second desired container for one instance was accepted, want a UNIQUE violation")
	}

	createContainer(t, s, "c3", instances[1])
	all, err := s.Queries().ListPluginContainers(ctx)
	if err != nil {
		t.Fatalf("ListPluginContainers: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListPluginContainers returned %d rows, want 2", len(all))
	}
}

// The CAS guard is what makes a stale read visible instead of silently
// overwriting a concurrent change (ADR-038).
func TestUpdatePluginContainerDesiredState_CAS(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	instances := containerFixture(t, s, 1)
	createContainer(t, s, "c1", instances[0])

	params := UpdatePluginContainerDesiredStateParams{
		ImageRef:        "gleipnir/slack:1.3.0",
		ImageDigest:     "sha256:bbbb",
		ConfigHash:      "cfg-2",
		NetworkName:     "gleipnir-" + instances[0],
		DesiredState:    "running",
		UpdatedAt:       "2024-01-02T00:00:00Z",
		ID:              "c1",
		ExpectedVersion: 0,
	}
	rows, err := s.Queries().UpdatePluginContainerDesiredState(ctx, params)
	if err != nil {
		t.Fatalf("UpdatePluginContainerDesiredState: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}

	got, err := s.Queries().GetPluginContainer(ctx, "c1")
	if err != nil {
		t.Fatalf("GetPluginContainer: %v", err)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1", got.Version)
	}
	if got.ImageDigest != "sha256:bbbb" {
		t.Errorf("ImageDigest = %q, want the updated digest", got.ImageDigest)
	}
	if got.MemoryLimitBytes != nil {
		t.Errorf("MemoryLimitBytes = %v, want NULL — the update clears an omitted cap", got.MemoryLimitBytes)
	}

	// Replaying the same expected version must lose.
	rows, err = s.Queries().UpdatePluginContainerDesiredState(ctx, params)
	if err != nil {
		t.Fatalf("UpdatePluginContainerDesiredState (stale): %v", err)
	}
	if rows != 0 {
		t.Errorf("stale CAS affected %d rows, want 0", rows)
	}
}

func TestPluginContainerDesiredStateCheckConstraint(t *testing.T) {
	s := newTestStore(t)
	instances := containerFixture(t, s, 1)

	_, err := s.Queries().CreatePluginContainer(context.Background(), CreatePluginContainerParams{
		ID:               "c1",
		PluginInstanceID: instances[0],
		ImageRef:         "gleipnir/slack:1.2.0",
		ImageDigest:      "sha256:aaaa",
		ConfigHash:       "cfg-1",
		NetworkName:      "net",
		DesiredState:     "paused", // not in the CHECK vocabulary
		CreatedAt:        testNow,
		UpdatedAt:        testNow,
	})
	if err == nil {
		t.Fatal("desired_state 'paused' was accepted, want a CHECK violation")
	}
}

// Deleting the instance takes its container, generations, and subnet with it —
// nothing about a removed instance is left for the reconciler to converge.
func TestPluginContainerCascadesFromInstance(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	instances := containerFixture(t, s, 1)

	createContainer(t, s, "c1", instances[0])
	createGeneration(t, s, "g1", instances[0], 1, "hash-1", "active")
	if _, err := s.Queries().AllocateContainerSubnet(ctx, AllocateContainerSubnetParams{
		Subnet:           "10.83.0.0/24",
		PluginInstanceID: instances[0],
		PoolBase:         "10.83.0.0/16",
		Slot:             0,
		AllocatedAt:      testNow,
	}); err != nil {
		t.Fatalf("AllocateContainerSubnet: %v", err)
	}

	if _, err := s.DB().Exec(`DELETE FROM plugin_instances WHERE id = ?`, instances[0]); err != nil {
		t.Fatalf("delete instance: %v", err)
	}

	containers, err := s.Queries().ListPluginContainers(ctx)
	if err != nil {
		t.Fatalf("ListPluginContainers: %v", err)
	}
	if len(containers) != 0 {
		t.Errorf("%d containers survived the instance, want 0", len(containers))
	}
	gens, err := s.Queries().ListLiveContainerGenerations(ctx)
	if err != nil {
		t.Fatalf("ListLiveContainerGenerations: %v", err)
	}
	if len(gens) != 0 {
		t.Errorf("%d generations survived the instance, want 0", len(gens))
	}
	subnets, err := s.Queries().ListContainerSubnets(ctx)
	if err != nil {
		t.Fatalf("ListContainerSubnets: %v", err)
	}
	if len(subnets) != 0 {
		t.Errorf("%d subnets survived the instance, want 0 — a leaked allocation is a permanently lost slot", len(subnets))
	}
}

// A generation's token is its identity. Two generations cannot share one, and
// a revoked token stops authenticating immediately.
func TestContainerGenerationTokens(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	instances := containerFixture(t, s, 2)

	createGeneration(t, s, "g1", instances[0], 1, "hash-1", "active")

	if _, err := s.Queries().CreateContainerGeneration(ctx, CreateContainerGenerationParams{
		ID:               "g2",
		PluginInstanceID: instances[1],
		Generation:       1,
		ImageDigest:      "sha256:aaaa",
		ConfigHash:       "cfg-1",
		TokenHash:        "hash-1", // same token as g1, different instance
		Status:           "pending",
		CreatedAt:        testNow,
		UpdatedAt:        testNow,
	}); err == nil {
		t.Fatal("a duplicate token hash was accepted, want a UNIQUE violation")
	}

	found, err := s.Queries().GetContainerGenerationByTokenHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("GetContainerGenerationByTokenHash: %v", err)
	}
	if found.ID != "g1" {
		t.Errorf("token resolved to %q, want g1", found.ID)
	}

	revokedAt := "2024-01-02T00:00:00Z"
	rows, err := s.Queries().RevokeContainerGenerationToken(ctx, RevokeContainerGenerationTokenParams{
		TokenRevokedAt: &revokedAt,
		UpdatedAt:      revokedAt,
		ID:             "g1",
	})
	if err != nil {
		t.Fatalf("RevokeContainerGenerationToken: %v", err)
	}
	if rows != 1 {
		t.Fatalf("revoke affected %d rows, want 1", rows)
	}

	if _, err := s.Queries().GetContainerGenerationByTokenHash(ctx, "hash-1"); err == nil {
		t.Error("a revoked token still authenticates, want no rows")
	}

	// Revoking twice is a no-op rather than a rewrite: the first revocation
	// time is the one that matters.
	secondRevoke := "2024-01-03T00:00:00Z"
	rows, err = s.Queries().RevokeContainerGenerationToken(ctx, RevokeContainerGenerationTokenParams{
		TokenRevokedAt: &secondRevoke,
		UpdatedAt:      secondRevoke,
		ID:             "g1",
	})
	if err != nil {
		t.Fatalf("RevokeContainerGenerationToken (repeat): %v", err)
	}
	if rows != 0 {
		t.Errorf("repeat revoke affected %d rows, want 0", rows)
	}
	got, err := s.Queries().GetContainerGeneration(ctx, "g1")
	if err != nil {
		t.Fatalf("GetContainerGeneration: %v", err)
	}
	if got.TokenRevokedAt == nil || *got.TokenRevokedAt != revokedAt {
		t.Errorf("TokenRevokedAt = %v, want the original revocation time", got.TokenRevokedAt)
	}
}

func TestContainerGenerationLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	instances := containerFixture(t, s, 1)
	createGeneration(t, s, "g1", instances[0], 1, "hash-1", "pending")

	// Generation numbers are unique per instance.
	if _, err := s.Queries().CreateContainerGeneration(ctx, CreateContainerGenerationParams{
		ID:               "g-dup",
		PluginInstanceID: instances[0],
		Generation:       1,
		ImageDigest:      "sha256:aaaa",
		ConfigHash:       "cfg-1",
		TokenHash:        "hash-other",
		Status:           "pending",
		CreatedAt:        testNow,
		UpdatedAt:        testNow,
	}); err == nil {
		t.Fatal("a duplicate generation number was accepted, want a UNIQUE violation")
	}

	// The container id is written exactly once: a second create attempt must
	// not overwrite the only reference to the first container.
	rows, err := s.Queries().SetContainerGenerationContainerID(ctx, SetContainerGenerationContainerIDParams{
		ContainerID: strPtr("ctr-abc"),
		UpdatedAt:   testNow,
		ID:          "g1",
	})
	if err != nil {
		t.Fatalf("SetContainerGenerationContainerID: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}
	rows, err = s.Queries().SetContainerGenerationContainerID(ctx, SetContainerGenerationContainerIDParams{
		ContainerID: strPtr("ctr-def"),
		UpdatedAt:   testNow,
		ID:          "g1",
	})
	if err != nil {
		t.Fatalf("SetContainerGenerationContainerID (repeat): %v", err)
	}
	if rows != 0 {
		t.Errorf("overwrite affected %d rows, want 0 — the first container would be orphaned", rows)
	}

	// Status moves are CAS: each step asserts where it thought the generation was.
	steps := []struct{ from, to string }{
		{"pending", "starting"},
		{"starting", "healthy"},
		{"healthy", "active"},
		{"active", "draining"},
		{"draining", "stopped"},
	}
	for _, step := range steps {
		rows, err := s.Queries().UpdateContainerGenerationStatus(ctx, UpdateContainerGenerationStatusParams{
			Status:         step.to,
			UpdatedAt:      testNow,
			ID:             "g1",
			ExpectedStatus: step.from,
		})
		if err != nil {
			t.Fatalf("UpdateContainerGenerationStatus %s→%s: %v", step.from, step.to, err)
		}
		if rows != 1 {
			t.Fatalf("UpdateContainerGenerationStatus %s→%s affected %d rows, want 1", step.from, step.to, rows)
		}
	}

	// A move from a status the generation is no longer in must lose.
	rows, err = s.Queries().UpdateContainerGenerationStatus(ctx, UpdateContainerGenerationStatusParams{
		Status:         "active",
		UpdatedAt:      testNow,
		ID:             "g1",
		ExpectedStatus: "healthy",
	})
	if err != nil {
		t.Fatalf("UpdateContainerGenerationStatus (stale): %v", err)
	}
	if rows != 0 {
		t.Errorf("stale status CAS affected %d rows, want 0", rows)
	}
}

// Generation numbers are never reused, so the next number comes from the
// highest row regardless of whether that generation is still alive.
func TestGetLatestContainerGenerationIncludesTerminal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	instances := containerFixture(t, s, 1)

	createGeneration(t, s, "g1", instances[0], 1, "hash-1", "stopped")
	createGeneration(t, s, "g2", instances[0], 2, "hash-2", "failed")

	latest, err := s.Queries().GetLatestContainerGeneration(ctx, instances[0])
	if err != nil {
		t.Fatalf("GetLatestContainerGeneration: %v", err)
	}
	if latest.Generation != 2 {
		t.Errorf("Generation = %d, want 2 — terminal generations still consume their number", latest.Generation)
	}

	live, err := s.Queries().ListLiveContainerGenerations(ctx)
	if err != nil {
		t.Fatalf("ListLiveContainerGenerations: %v", err)
	}
	if len(live) != 0 {
		t.Errorf("ListLiveContainerGenerations returned %d rows, want 0", len(live))
	}
}

func TestListLiveContainerGenerations(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	instances := containerFixture(t, s, 2)

	createGeneration(t, s, "g1", instances[0], 1, "h1", "draining")
	createGeneration(t, s, "g2", instances[0], 2, "h2", "active")
	createGeneration(t, s, "g3", instances[1], 1, "h3", "stopped")
	createGeneration(t, s, "g4", instances[1], 2, "h4", "starting")

	live, err := s.Queries().ListLiveContainerGenerations(ctx)
	if err != nil {
		t.Fatalf("ListLiveContainerGenerations: %v", err)
	}
	got := make([]string, len(live))
	for i, g := range live {
		got[i] = g.ID
	}
	want := []string{"g1", "g2", "g4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("live generations = %v, want %v (ordered by instance, then generation)", got, want)
	}
}

// Subnets are a finite resource: a double allocation would put two instances
// on one network and break east-west isolation. The UNIQUE constraint, not
// caller discipline, is what prevents it — so the test races real writers at
// the same slot rather than asserting on a code path.
func TestAllocateContainerSubnet_ConcurrentClaimsOneWinner(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	const contenders = 8
	instances := containerFixture(t, s, contenders)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded []string
	)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(instanceID string) {
			defer wg.Done()
			// Every goroutine goes for slot 0 of the same pool.
			row, err := s.Queries().AllocateContainerSubnet(ctx, AllocateContainerSubnetParams{
				Subnet:           "10.83.0.0/24",
				PluginInstanceID: instanceID,
				PoolBase:         "10.83.0.0/16",
				Slot:             0,
				AllocatedAt:      testNow,
			})
			if err != nil {
				return
			}
			mu.Lock()
			succeeded = append(succeeded, row.PluginInstanceID)
			mu.Unlock()
		}(instances[i])
	}
	wg.Wait()

	if len(succeeded) != 1 {
		t.Fatalf("%d allocators claimed slot 0, want exactly 1", len(succeeded))
	}
	all, err := s.Queries().ListContainerSubnets(ctx)
	if err != nil {
		t.Fatalf("ListContainerSubnets: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("%d subnet rows exist, want 1", len(all))
	}
}

// The same race, run the way a real allocator runs it: on a collision, take
// the next free slot. Every instance must end up with its own subnet, and no
// slot may be handed out twice.
func TestAllocateContainerSubnet_ConcurrentAllocatorsGetDistinctSlots(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	const contenders = 8
	instances := containerFixture(t, s, contenders)

	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(instanceID string) {
			defer wg.Done()
			// Retry-on-conflict, the loop the allocator itself will run.
			for slot := int64(0); slot < contenders*4; slot++ {
				_, err := s.Queries().AllocateContainerSubnet(ctx, AllocateContainerSubnetParams{
					Subnet:           subnetFor(slot),
					PluginInstanceID: instanceID,
					PoolBase:         "10.83.0.0/16",
					Slot:             slot,
					AllocatedAt:      testNow,
				})
				if err == nil {
					return
				}
			}
			t.Errorf("instance %s never obtained a subnet", instanceID)
		}(instances[i])
	}
	wg.Wait()

	all, err := s.Queries().ListContainerSubnets(ctx)
	if err != nil {
		t.Fatalf("ListContainerSubnets: %v", err)
	}
	if len(all) != contenders {
		t.Fatalf("%d subnets allocated, want %d", len(all), contenders)
	}
	seenSlot := map[int64]bool{}
	seenInstance := map[string]bool{}
	for _, row := range all {
		if seenSlot[row.Slot] {
			t.Errorf("slot %d was allocated twice", row.Slot)
		}
		if seenInstance[row.PluginInstanceID] {
			t.Errorf("instance %s holds more than one subnet", row.PluginInstanceID)
		}
		seenSlot[row.Slot] = true
		seenInstance[row.PluginInstanceID] = true
	}

	slots, err := s.Queries().ListContainerSubnetSlots(ctx, "10.83.0.0/16")
	if err != nil {
		t.Fatalf("ListContainerSubnetSlots: %v", err)
	}
	if len(slots) != contenders {
		t.Errorf("ListContainerSubnetSlots returned %d slots, want %d", len(slots), contenders)
	}
	for i := 1; i < len(slots); i++ {
		if slots[i] <= slots[i-1] {
			t.Fatalf("slots are not ascending: %v", slots)
		}
	}
}

func subnetFor(slot int64) string {
	return "10.83." + itoa(slot) + ".0/24"
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

// A released slot returns to the pool and can be handed to another instance —
// otherwise every rotation would permanently consume address space.
func TestReleaseContainerSubnet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	instances := containerFixture(t, s, 2)

	if _, err := s.Queries().AllocateContainerSubnet(ctx, AllocateContainerSubnetParams{
		Subnet:           "10.83.0.0/24",
		PluginInstanceID: instances[0],
		PoolBase:         "10.83.0.0/16",
		Slot:             0,
		AllocatedAt:      testNow,
	}); err != nil {
		t.Fatalf("AllocateContainerSubnet: %v", err)
	}

	rows, err := s.Queries().ReleaseContainerSubnet(ctx, instances[0])
	if err != nil {
		t.Fatalf("ReleaseContainerSubnet: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}

	// Releasing again is an ordinary no-op, not an error: cleanup passes run
	// more than once.
	rows, err = s.Queries().ReleaseContainerSubnet(ctx, instances[0])
	if err != nil {
		t.Fatalf("ReleaseContainerSubnet (repeat): %v", err)
	}
	if rows != 0 {
		t.Errorf("repeat release affected %d rows, want 0", rows)
	}

	if _, err := s.Queries().AllocateContainerSubnet(ctx, AllocateContainerSubnetParams{
		Subnet:           "10.83.0.0/24",
		PluginInstanceID: instances[1],
		PoolBase:         "10.83.0.0/16",
		Slot:             0,
		AllocatedAt:      testNow,
	}); err != nil {
		t.Fatalf("reallocating a released slot: %v", err)
	}
}

func TestContainerImageAccounting(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	instances := containerFixture(t, s, 1)

	size := int64(120 << 20)
	loadedAt := testNow
	img, err := s.Queries().UpsertContainerImage(ctx, UpsertContainerImageParams{
		Digest:     "sha256:aaaa",
		Reference:  "gleipnir/slack:1.2.0",
		PluginID:   strPtr("pl1"),
		SizeBytes:  &size,
		LoadedAt:   loadedAt,
		LastUsedAt: &loadedAt,
	})
	if err != nil {
		t.Fatalf("UpsertContainerImage: %v", err)
	}
	if img.Digest != "sha256:aaaa" {
		t.Errorf("Digest = %q, want sha256:aaaa", img.Digest)
	}

	// Loading the same digest again refreshes rather than fails.
	later := "2024-01-05T00:00:00Z"
	img, err = s.Queries().UpsertContainerImage(ctx, UpsertContainerImageParams{
		Digest:     "sha256:aaaa",
		Reference:  "gleipnir/slack:1.3.0",
		PluginID:   strPtr("pl1"),
		SizeBytes:  &size,
		LoadedAt:   later,
		LastUsedAt: &later,
	})
	if err != nil {
		t.Fatalf("UpsertContainerImage (repeat): %v", err)
	}
	if img.Reference != "gleipnir/slack:1.3.0" {
		t.Errorf("Reference = %q, want the refreshed tag", img.Reference)
	}
	if img.LoadedAt != loadedAt {
		t.Errorf("LoadedAt = %q, want the original load time %q", img.LoadedAt, loadedAt)
	}

	// No generation runs it yet, so it is reclaimable.
	count, err := s.Queries().CountContainerImageReferences(ctx, "sha256:aaaa")
	if err != nil {
		t.Fatalf("CountContainerImageReferences: %v", err)
	}
	if count != 0 {
		t.Errorf("references = %d, want 0", count)
	}
	unreferenced, err := s.Queries().ListUnreferencedContainerImages(ctx)
	if err != nil {
		t.Fatalf("ListUnreferencedContainerImages: %v", err)
	}
	if len(unreferenced) != 1 {
		t.Fatalf("%d unreferenced images, want 1", len(unreferenced))
	}

	// A live generation pins it; a terminal one does not.
	createGeneration(t, s, "g1", instances[0], 1, "hash-1", "active")
	count, err = s.Queries().CountContainerImageReferences(ctx, "sha256:aaaa")
	if err != nil {
		t.Fatalf("CountContainerImageReferences: %v", err)
	}
	if count != 1 {
		t.Errorf("references = %d, want 1", count)
	}
	unreferenced, err = s.Queries().ListUnreferencedContainerImages(ctx)
	if err != nil {
		t.Fatalf("ListUnreferencedContainerImages: %v", err)
	}
	if len(unreferenced) != 0 {
		t.Errorf("%d unreferenced images while a generation runs one, want 0", len(unreferenced))
	}

	if _, err := s.Queries().UpdateContainerGenerationStatus(ctx, UpdateContainerGenerationStatusParams{
		Status:         "stopped",
		UpdatedAt:      testNow,
		ID:             "g1",
		ExpectedStatus: "active",
	}); err != nil {
		t.Fatalf("UpdateContainerGenerationStatus: %v", err)
	}
	unreferenced, err = s.Queries().ListUnreferencedContainerImages(ctx)
	if err != nil {
		t.Fatalf("ListUnreferencedContainerImages: %v", err)
	}
	if len(unreferenced) != 1 {
		t.Errorf("%d unreferenced images after the generation stopped, want 1", len(unreferenced))
	}
}

// Image accounting outlives the plugin it came from: an uninstall must not
// delete the row that says a digest is still on disk.
func TestContainerImageSurvivesPluginDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	containerFixture(t, s, 1)

	loadedAt := testNow
	if _, err := s.Queries().UpsertContainerImage(ctx, UpsertContainerImageParams{
		Digest:    "sha256:aaaa",
		Reference: "gleipnir/slack:1.2.0",
		PluginID:  strPtr("pl1"),
		LoadedAt:  loadedAt,
	}); err != nil {
		t.Fatalf("UpsertContainerImage: %v", err)
	}

	if _, err := s.DB().Exec(`DELETE FROM plugins WHERE id = ?`, "pl1"); err != nil {
		t.Fatalf("delete plugin: %v", err)
	}

	img, err := s.Queries().GetContainerImage(ctx, "sha256:aaaa")
	if err != nil {
		t.Fatalf("GetContainerImage after plugin delete: %v", err)
	}
	if img.PluginID != nil {
		t.Errorf("PluginID = %v, want NULL after the plugin was removed", img.PluginID)
	}
}

func TestTouchContainerImage(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Queries().UpsertContainerImage(ctx, UpsertContainerImageParams{
		Digest:    "sha256:aaaa",
		Reference: "gleipnir/slack:1.2.0",
		LoadedAt:  testNow,
	}); err != nil {
		t.Fatalf("UpsertContainerImage: %v", err)
	}

	used := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.Queries().TouchContainerImage(ctx, TouchContainerImageParams{
		LastUsedAt: &used,
		Digest:     "sha256:aaaa",
	})
	if err != nil {
		t.Fatalf("TouchContainerImage: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}

	img, err := s.Queries().GetContainerImage(ctx, "sha256:aaaa")
	if err != nil {
		t.Fatalf("GetContainerImage: %v", err)
	}
	if img.LastUsedAt == nil || *img.LastUsedAt != used {
		t.Errorf("LastUsedAt = %v, want %q", img.LastUsedAt, used)
	}
}
