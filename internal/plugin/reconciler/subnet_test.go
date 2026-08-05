package reconciler

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// allocatorFixture stands up a real store with n plugin instances and returns
// an allocator over it. The allocator's contract is partly the DB's — the
// UNIQUE constraint is what makes concurrent allocation safe — so these tests
// run against the real queries rather than a double.
func allocatorFixture(t *testing.T, pool string, n int) (*db.Store, *SubnetAllocator, []string) {
	t.Helper()
	s := testutil.NewTestStore(t)

	if _, err := s.DB().Exec(
		`INSERT INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, version, created_at, updated_at)
		 VALUES ('pl1', 'slack', '1.0.0', '{}', 'pubkey', 'active', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert plugin: %v", err)
	}

	ids := make([]string, n)
	for i := range ids {
		ids[i] = "inst" + string(rune('a'+i))
		if _, err := s.DB().Exec(
			`INSERT INTO plugin_instances(id, plugin_id, instance_name, config_json, subscription_scope_json, handshake_versions, health_state, version, created_at, updated_at)
			 VALUES (?, 'pl1', ?, '{}', '{}', '{}', 'healthy', 0, '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
			ids[i], "instance-"+ids[i],
		); err != nil {
			t.Fatalf("insert plugin instance %s: %v", ids[i], err)
		}
	}

	a, err := NewSubnetAllocator(s.Queries(), netip.MustParsePrefix(pool), func() string { return "2024-01-01T00:00:00Z" })
	if err != nil {
		t.Fatalf("NewSubnetAllocator: %v", err)
	}
	return s, a, ids
}

func TestSubnetForSlot(t *testing.T) {
	tests := []struct {
		name string
		pool string
		slot int
		want string
	}{
		{name: "first slot of a /16", pool: "10.83.0.0/16", slot: 0, want: "10.83.0.0/24"},
		{name: "middle slot of a /16", pool: "10.83.0.0/16", slot: 7, want: "10.83.7.0/24"},
		{name: "last slot of a /16", pool: "10.83.0.0/16", slot: 255, want: "10.83.255.0/24"},
		{name: "a /20 carves 16 subnets", pool: "172.20.16.0/20", slot: 15, want: "172.20.31.0/24"},
		{name: "a /24 pool is a single subnet", pool: "192.168.9.0/24", slot: 0, want: "192.168.9.0/24"},
		{name: "an unmasked pool is masked first", pool: "10.83.4.9/16", slot: 1, want: "10.83.1.0/24"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := subnetForSlot(netip.MustParsePrefix(tc.pool), tc.slot)
			if err != nil {
				t.Fatalf("subnetForSlot: %v", err)
			}
			if got.String() != tc.want {
				t.Errorf("subnetForSlot(%s, %d) = %s, want %s", tc.pool, tc.slot, got, tc.want)
			}
		})
	}
}

func TestSubnetForSlot_OutOfRange(t *testing.T) {
	pool := netip.MustParsePrefix("10.83.0.0/16")
	for _, slot := range []int{-1, 256, 1000} {
		if _, err := subnetForSlot(pool, slot); err == nil {
			t.Errorf("subnetForSlot(%s, %d) succeeded, want a range error", pool, slot)
		}
	}
}

// Capacity is what tells an operator whether their pool is big enough, so the
// arithmetic is pinned rather than left implicit.
func TestPoolCapacity(t *testing.T) {
	tests := []struct {
		pool string
		want int
	}{
		{pool: "10.83.0.0/16", want: 256},
		{pool: "10.0.0.0/8", want: 65536},
		{pool: "172.20.16.0/20", want: 16},
		{pool: "192.168.9.0/24", want: 1},
	}

	for _, tc := range tests {
		t.Run(tc.pool, func(t *testing.T) {
			if got := poolCapacity(netip.MustParsePrefix(tc.pool)); got != tc.want {
				t.Errorf("poolCapacity(%s) = %d, want %d", tc.pool, got, tc.want)
			}
		})
	}
}

func TestValidatePool(t *testing.T) {
	tests := []struct {
		name    string
		pool    netip.Prefix
		wantErr bool
	}{
		{name: "a /16 is fine", pool: netip.MustParsePrefix("10.83.0.0/16")},
		{name: "a /24 holds exactly one", pool: netip.MustParsePrefix("192.168.9.0/24")},
		{name: "a /25 holds none", pool: netip.MustParsePrefix("192.168.9.0/25"), wantErr: true},
		{name: "IPv6 is unsupported", pool: netip.MustParsePrefix("fd00::/48"), wantErr: true},
		{name: "the zero prefix is invalid", pool: netip.Prefix{}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePool(tc.pool)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("ValidatePool(%v) error = %v, want error = %v", tc.pool, err, tc.wantErr)
			}
			if err != nil {
				var invalid *InvalidPoolError
				if !errors.As(err, &invalid) {
					t.Errorf("error = %v, want *InvalidPoolError", err)
				}
			}
		})
	}
}

// Allocation is dense and idempotent: instances take the lowest free slots, and
// asking again returns what the instance already holds rather than a second
// subnet.
func TestSubnetAllocator_AllocateIsDenseAndIdempotent(t *testing.T) {
	ctx := context.Background()
	_, a, ids := allocatorFixture(t, "10.83.0.0/16", 3)

	want := []string{"10.83.0.0/24", "10.83.1.0/24", "10.83.2.0/24"}
	for i, id := range ids {
		got, err := a.Allocate(ctx, id)
		if err != nil {
			t.Fatalf("Allocate(%s): %v", id, err)
		}
		if got.String() != want[i] {
			t.Errorf("Allocate(%s) = %s, want %s", id, got, want[i])
		}
	}

	again, err := a.Allocate(ctx, ids[1])
	if err != nil {
		t.Fatalf("re-Allocate: %v", err)
	}
	if again.String() != want[1] {
		t.Errorf("re-Allocate returned %s, want the instance's existing %s", again, want[1])
	}
}

// A released slot is reused, so rotation does not slowly consume the pool.
func TestSubnetAllocator_ReleasedSlotIsReused(t *testing.T) {
	ctx := context.Background()
	_, a, ids := allocatorFixture(t, "10.83.0.0/16", 3)

	for _, id := range ids[:2] {
		if _, err := a.Allocate(ctx, id); err != nil {
			t.Fatalf("Allocate(%s): %v", id, err)
		}
	}
	if err := a.Release(ctx, ids[0]); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Releasing again is an ordinary no-op — cleanup passes run more than once.
	if err := a.Release(ctx, ids[0]); err != nil {
		t.Fatalf("repeat Release: %v", err)
	}

	got, err := a.Allocate(ctx, ids[2])
	if err != nil {
		t.Fatalf("Allocate after release: %v", err)
	}
	if got.String() != "10.83.0.0/24" {
		t.Errorf("Allocate = %s, want the released 10.83.0.0/24", got)
	}
}

// Exhaustion is an operator-facing error naming the pool and the fix — not a
// daemon-level failure about address space that says nothing actionable.
func TestSubnetAllocator_ExhaustionIsActionable(t *testing.T) {
	ctx := context.Background()
	// A /24 pool holds exactly one instance.
	_, a, ids := allocatorFixture(t, "192.168.9.0/24", 2)

	if _, err := a.Allocate(ctx, ids[0]); err != nil {
		t.Fatalf("first Allocate: %v", err)
	}

	_, err := a.Allocate(ctx, ids[1])
	if err == nil {
		t.Fatal("Allocate: want a pool-exhausted error")
	}
	var exhausted *PoolExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("error = %v, want *PoolExhaustedError", err)
	}
	if exhausted.Capacity != 1 {
		t.Errorf("Capacity = %d, want 1", exhausted.Capacity)
	}
	for _, want := range []string{"192.168.9.0/24", "GLEIPNIR_PLUGIN_SUBNET_POOL", "exhausted"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Concurrent allocators must never hand out one subnet twice. The DB's UNIQUE
// constraint is the arbiter; this proves the retry loop above it converges to
// distinct subnets rather than failing or colliding.
func TestSubnetAllocator_ConcurrentAllocationsAreDistinct(t *testing.T) {
	ctx := context.Background()
	const instances = 6
	_, a, ids := allocatorFixture(t, "10.83.0.0/16", instances)

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		got    = map[string]string{} // instance → subnet
		errsCh []error
	)
	for _, id := range ids {
		wg.Add(1)
		go func(instanceID string) {
			defer wg.Done()
			subnet, err := a.Allocate(ctx, instanceID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errsCh = append(errsCh, err)
				return
			}
			got[instanceID] = subnet.String()
		}(id)
	}
	wg.Wait()

	if len(errsCh) != 0 {
		t.Fatalf("concurrent allocation errors: %v", errsCh)
	}
	if len(got) != instances {
		t.Fatalf("%d instances got a subnet, want %d", len(got), instances)
	}
	seen := map[string]string{}
	for instanceID, subnet := range got {
		if prev, dup := seen[subnet]; dup {
			t.Errorf("subnet %s handed to both %s and %s", subnet, prev, instanceID)
		}
		seen[subnet] = instanceID
	}
}

func TestNewSubnetAllocator_RejectsBadInput(t *testing.T) {
	s := testutil.NewTestStore(t)

	if _, err := NewSubnetAllocator(nil, netip.MustParsePrefix("10.83.0.0/16"), nil); err == nil {
		t.Error("NewSubnetAllocator: want an error for a nil store")
	}
	if _, err := NewSubnetAllocator(s.Queries(), netip.MustParsePrefix("192.168.9.0/25"), nil); err == nil {
		t.Error("NewSubnetAllocator: want an error for a pool that holds no subnets")
	}
}
