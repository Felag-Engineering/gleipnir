-- AllocateContainerSubnet claims one slot in the base pool for an instance.
--
-- This INSERT is the allocation: there is no read-then-write window to lose,
-- because UNIQUE(pool_base, slot) and UNIQUE(plugin_instance_id) are what
-- arbitrate. Two callers racing for the same slot cannot both commit -- the
-- loser gets a constraint violation and picks the next free slot. The pool
-- arithmetic that turns a slot into a CIDR stays in the allocator, not here:
-- SQL is the wrong place to reason about address space.
-- name: AllocateContainerSubnet :one
INSERT INTO plugin_container_subnets (subnet, plugin_instance_id, pool_base, slot, allocated_at)
VALUES (:subnet, :plugin_instance_id, :pool_base, :slot, :allocated_at)
RETURNING *;

-- name: GetContainerSubnetByInstance :one
SELECT * FROM plugin_container_subnets WHERE plugin_instance_id = :plugin_instance_id;

-- ListContainerSubnetSlots returns the slots already taken in one base pool,
-- ascending. The allocator reads this to pick its next candidate; the INSERT
-- above, not this read, is what makes the choice safe.
-- name: ListContainerSubnetSlots :many
SELECT slot FROM plugin_container_subnets WHERE pool_base = :pool_base ORDER BY slot;

-- name: ListContainerSubnets :many
SELECT * FROM plugin_container_subnets ORDER BY pool_base, slot;

-- ReleaseContainerSubnet returns an instance's slot to the pool.
-- rows_affected == 0 means the instance held no allocation, which is a
-- perfectly ordinary outcome on a cleanup pass that runs twice.
-- name: ReleaseContainerSubnet :execrows
DELETE FROM plugin_container_subnets WHERE plugin_instance_id = :plugin_instance_id;
