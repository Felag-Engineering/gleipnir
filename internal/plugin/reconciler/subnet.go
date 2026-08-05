package reconciler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// subnetBits is the prefix length of every per-instance subnet. A /24 is far
// more address space than one plugin container needs, and that is deliberate:
// the unit of allocation matches what operators read in `docker network ls`
// and what the runtime's own tooling assumes, so an operator debugging a
// network never has to do prefix arithmetic to know which instance they are
// looking at.
const subnetBits = 24

// maxAllocationAttempts bounds the retry loop when a concurrent allocator wins
// a slot. Each attempt moves to the next free slot, so a handful covers any
// realistic contention — a host does not create plugin instances in a tight
// loop from many goroutines.
const maxAllocationAttempts = 8

// PoolExhaustedError reports that the configured base pool has no free /24
// left. It is operator-facing on purpose: the failure mode this replaces is a
// container-runtime daemon refusing to create a network with a message about
// address space that says nothing about which pool, how many instances, or what
// to do next.
type PoolExhaustedError struct {
	Pool     netip.Prefix
	Capacity int
}

func (e *PoolExhaustedError) Error() string {
	return fmt.Sprintf(
		"plugin subnet pool %s is exhausted: all %d /%d subnets are allocated; "+
			"widen GLEIPNIR_PLUGIN_SUBNET_POOL (a /16 holds 256 instances) or remove unused plugin instances",
		e.Pool, e.Capacity, subnetBits)
}

// InvalidPoolError reports a base pool the allocator cannot carve /24s from.
type InvalidPoolError struct {
	Pool   netip.Prefix
	Reason string
}

func (e *InvalidPoolError) Error() string {
	return fmt.Sprintf("invalid plugin subnet pool %s: %s", e.Pool, e.Reason)
}

// SubnetStore is the persistence the allocator needs. *db.Queries satisfies it.
//
// The store is the arbiter, not this package: AllocateContainerSubnet is a bare
// INSERT behind UNIQUE(pool_base, slot), so two allocators racing for one slot
// cannot both commit. That is why Allocate can read the taken set and then
// write without holding a lock across the gap — losing the race is an ordinary
// outcome it retries, not a corruption it has to prevent.
type SubnetStore interface {
	GetContainerSubnetByInstance(ctx context.Context, instanceID string) (db.PluginContainerSubnet, error)
	ListContainerSubnetSlots(ctx context.Context, poolBase string) ([]int64, error)
	AllocateContainerSubnet(ctx context.Context, arg db.AllocateContainerSubnetParams) (db.PluginContainerSubnet, error)
	ReleaseContainerSubnet(ctx context.Context, instanceID string) (int64, error)
}

// SubnetAllocator hands each plugin instance its own /24 out of a configurable
// base pool (spec §7).
//
// East-west isolation is the reason this exists: one dedicated internal network
// per instance means a compromised plugin cannot reach a sibling's MCP endpoint,
// which would be an ADR-001 violation by topology rather than by policy. That
// makes subnets a finite resource — stock daemon defaults exhaust at roughly 30
// networks, which is why Gleipnir allocates explicitly instead of letting the
// runtime pick.
type SubnetAllocator struct {
	store SubnetStore
	pool  netip.Prefix
	now   func() string
}

// NewSubnetAllocator validates the pool and returns an allocator for it.
func NewSubnetAllocator(store SubnetStore, pool netip.Prefix, now func() string) (*SubnetAllocator, error) {
	if store == nil {
		return nil, errors.New("reconciler: SubnetStore is required")
	}
	if err := ValidatePool(pool); err != nil {
		return nil, err
	}
	if now == nil {
		now = utcNow
	}
	return &SubnetAllocator{store: store, pool: pool.Masked(), now: now}, nil
}

// ValidatePool reports whether pool can back per-instance /24 allocations.
// Exported so configuration can be rejected at startup with the same message
// the allocator would produce, rather than at first plugin install.
func ValidatePool(pool netip.Prefix) error {
	if !pool.IsValid() {
		return &InvalidPoolError{Pool: pool, Reason: "not a valid CIDR"}
	}
	if !pool.Addr().Is4() {
		return &InvalidPoolError{Pool: pool, Reason: "must be IPv4; IPv6 plugin networks are not supported"}
	}
	if pool.Bits() > subnetBits {
		return &InvalidPoolError{
			Pool:   pool,
			Reason: fmt.Sprintf("prefix /%d is longer than the /%d carved per instance, so it holds no subnets", pool.Bits(), subnetBits),
		}
	}
	return nil
}

// Capacity returns how many instances the pool can hold.
func (a *SubnetAllocator) Capacity() int { return poolCapacity(a.pool) }

// Pool returns the base pool, for logging and error messages.
func (a *SubnetAllocator) Pool() netip.Prefix { return a.pool }

// Allocate returns the instance's subnet, allocating one if it has none.
//
// It is idempotent by design: the reconciler is level-triggered and calls this
// on every pass that finds a missing network, so "already allocated" is the
// common case, not an error. An instance keeps its subnet until Release.
func (a *SubnetAllocator) Allocate(ctx context.Context, instanceID string) (netip.Prefix, error) {
	existing, err := a.store.GetContainerSubnetByInstance(ctx, instanceID)
	if err == nil {
		subnet, parseErr := netip.ParsePrefix(existing.Subnet)
		if parseErr != nil {
			// A stored subnet that no longer parses means the row was written
			// by something other than this allocator. Surfacing it beats
			// silently re-allocating and leaving two networks behind.
			return netip.Prefix{}, fmt.Errorf("stored subnet %q for instance %s does not parse: %w",
				existing.Subnet, instanceID, parseErr)
		}
		return subnet, nil
	}
	if !isNoRows(err) {
		return netip.Prefix{}, fmt.Errorf("looking up existing subnet for instance %s: %w", instanceID, err)
	}

	poolBase := a.pool.String()
	for attempt := 0; attempt < maxAllocationAttempts; attempt++ {
		taken, err := a.store.ListContainerSubnetSlots(ctx, poolBase)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("listing allocated subnet slots: %w", err)
		}

		slot, ok := lowestFreeSlot(taken, a.Capacity())
		if !ok {
			return netip.Prefix{}, &PoolExhaustedError{Pool: a.pool, Capacity: a.Capacity()}
		}

		subnet, err := subnetForSlot(a.pool, slot)
		if err != nil {
			return netip.Prefix{}, err
		}

		_, err = a.store.AllocateContainerSubnet(ctx, db.AllocateContainerSubnetParams{
			Subnet:           subnet.String(),
			PluginInstanceID: instanceID,
			PoolBase:         poolBase,
			Slot:             int64(slot),
			AllocatedAt:      a.now(),
		})
		if err == nil {
			return subnet, nil
		}
		// A constraint violation means a concurrent allocator took this slot
		// (or this instance) first. Re-read and try the next free one; the
		// INSERT, not the read above, is what arbitrates.
		if !isConstraintViolation(err) {
			return netip.Prefix{}, fmt.Errorf("allocating subnet for instance %s: %w", instanceID, err)
		}

		// The loser of a race for THIS INSTANCE (rather than for the slot) now
		// has a row: re-read rather than burning attempts on a slot race that
		// is not ours to win.
		if existing, getErr := a.store.GetContainerSubnetByInstance(ctx, instanceID); getErr == nil {
			return netip.ParsePrefix(existing.Subnet)
		}
	}

	return netip.Prefix{}, fmt.Errorf("allocating subnet for instance %s: %d attempts all lost to concurrent allocations",
		instanceID, maxAllocationAttempts)
}

// Release returns an instance's subnet to the pool. Releasing an instance that
// holds none is not an error: cleanup passes run more than once.
func (a *SubnetAllocator) Release(ctx context.Context, instanceID string) error {
	if _, err := a.store.ReleaseContainerSubnet(ctx, instanceID); err != nil {
		return fmt.Errorf("releasing subnet for instance %s: %w", instanceID, err)
	}
	return nil
}

// poolCapacity returns how many /24s fit in the pool.
func poolCapacity(pool netip.Prefix) int {
	return 1 << (subnetBits - pool.Bits())
}

// lowestFreeSlot returns the smallest slot in [0, capacity) not present in
// taken. Reusing the lowest free slot rather than always appending keeps the
// allocated range dense, so an operator scanning `docker network ls` sees a
// contiguous block instead of gaps left by removed instances.
func lowestFreeSlot(taken []int64, capacity int) (int, bool) {
	occupied := make(map[int64]bool, len(taken))
	for _, s := range taken {
		occupied[s] = true
	}
	for slot := 0; slot < capacity; slot++ {
		if !occupied[int64(slot)] {
			return slot, true
		}
	}
	return 0, false
}

// subnetForSlot carves the nth /24 out of the base pool.
//
// The arithmetic is done on the 4-byte address rather than with big integers
// because the pool is IPv4-only (ValidatePool enforces it) and a /24 boundary
// is a whole number of bytes: slot n starts at the pool's base address plus
// n << 8 hosts.
func subnetForSlot(pool netip.Prefix, slot int) (netip.Prefix, error) {
	if slot < 0 || slot >= poolCapacity(pool) {
		return netip.Prefix{}, &PoolExhaustedError{Pool: pool, Capacity: poolCapacity(pool)}
	}

	base := pool.Masked().Addr().As4()
	offset := uint32(slot) << 8 //nolint:gosec // slot < capacity ≤ 2^24, so the shift cannot overflow
	value := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	value += offset

	addr := netip.AddrFrom4([4]byte{
		byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value),
	})
	return netip.PrefixFrom(addr, subnetBits), nil
}

// isNoRows reports whether err is the "no such row" sentinel. Kept local so
// this package does not spread database/sql knowledge beyond one function.
func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// isConstraintViolation reports whether err is a UNIQUE/PRIMARY KEY violation.
// modernc.org/sqlite does not export a typed error for this, so the check is on
// the message — the same pragmatic approach the plugin instance-create handler
// already takes for its race path.
func isConstraintViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "CONSTRAINT")
}

// utcNow is the allocator's default timestamp source, injectable so tests get
// deterministic allocated_at values.
func utcNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }
