# Container substrate garbage collection

**Spec:** `docs/developer/mcp-realignment-spec.md` §7 reconciler (ADR-056). **Issue:** #818.
**Code:** `internal/plugin/reconciler/gc.go`.

Three resources accumulate as plugins are installed, rotated, and removed: OCI
images loaded from signed bundles, per-instance networks and the subnets carved
for them, and the hashes of instance tokens minted per generation. This is the
pass that reclaims them.

## Level-triggered, like everything else here

There is no delete queue and no "pending cleanup" column. Each pass asks what
is unreferenced *right now* and reclaims a bounded slice of it. A crashed pass
costs one interval; a missed reclaim is found again next time. Cleanup is
convergence toward "nothing unreferenced exists", not a sequence that has to be
driven to completion.

`ReconcileGC` is a **separate pass** from `ReconcileOnce`, not a branch inside
it, and is meant for a slower cadence. The convergence loop is latency-sensitive
— an operator who starts an instance wants a container within one interval —
while nothing gets worse for a few minutes because a dead image is still on
disk. Running both at the same rate would spend the reclaim cost on every
convergence pass for no benefit.

## Image GC

An image is reclaimable when no *live* generation pins its digest. Live means
`pending`, `starting`, `healthy`, `active`, or `draining`; the two terminal
statuses (`stopped`, `failed`) have no container left, so nothing about them
can need the bytes.

**`draining` is the one that matters.** During a rotation the old generation
keeps serving until the new one passes its health gate, so its image is in use
in exactly the window it looks stalest. An image GC that reasoned about "the
current image" rather than "every live generation's image" would pull the bytes
out from under a container still finishing work.

Three layers stand between a live plugin and a removed image, and each exists
because the one after it is not sufficient alone:

| Layer | Catches |
|---|---|
| `ListUnreferencedContainerImages` | Every digest a live generation pins, mid-rotation ones included |
| `CountContainerImageReferences` re-check, immediately before the socket call | A rotation that started *between* the list and the removal |
| The daemon's own "in use by a container" refusal | Gleipnir's records being wrong |

The removal is therefore never forced and never prunes children. A forced
removal untags an image a container still uses, leaving a running plugin on an
image nothing can name — the exact state a digest pin exists to prevent. And
the layers under a plugin image may be shared with an image Gleipnir did not
load; reclaiming space is not worth touching something outside what it owns.

**Bounded per pass** (`ImagesPerPass`, default 5). Image removal is the slowest
thing this package asks a daemon to do — it unlinks layers and can block for
seconds. An unbounded pass after several uninstalls would hold the socket long
enough to delay the convergence work an operator is actually waiting on.

`GCResult.ImagesDeferred` reports what the bound left behind. A bounded pass
that says nothing about what it skipped reads as "there was nothing else to
do", which is how a backlog becomes invisible.

Ordering within a reclaim: **the image goes first, the accounting row second.**
The reverse would drop the only record that a digest was ever loaded while the
bytes were still on disk, leaving an image nothing would reclaim again.

## Network and subnet cleanup

The convergence loop already tears an instance's network down and releases its
subnet as the second half of that teardown. GC is the **backstop** for what
that never got to do — a desired row deleted while the network removal kept
failing, or a host that stopped between the two steps.

An allocation is releasable only when **both** hold:

1. No `plugin_containers` row claims the instance, and
2. No managed network labelled for the instance still exists.

The conjunction is load-bearing. A subnet handed to another instance while the
old network is still up produces an address overlap the runtime refuses at
create time — turning a clean teardown into a stuck one, which is the failure
the network-then-release ordering exists to avoid in the first place.

`orphanedSubnets` is pure over its inputs (like `planFor` and `Discover`), so
the whole condition is testable without a socket, and its output is sorted so
two passes over an unchanged world read identically.

## Token hygiene

A generation's instance token is stored only as a SHA-256 hash, revoked when
the generation retires, and **purged** — hash replaced by a per-row tombstone —
once it has outlived the retention window.

**Retention window: 24 hours** (`TokenRetention`).

The window exists because a revoked token is briefly still evidence. A host RPC
that arrived moments before the revocation is correlated to its generation by
that hash, and purging on the instant of revocation would make the end of a
generation's life unattributable — which is precisely the window a rotation's
problems show up in. A day is long enough to investigate a rotation that went
wrong and short enough that material does not accumulate indefinitely.

**The row survives the purge.** `token_hash` is `NOT NULL UNIQUE`, so the hash
is replaced by `purged:<row id>` rather than cleared. Deleting the row instead
would be worse in two ways:

- it discards the rotation history an operator reads to answer "what has this
  instance actually run", and
- deleting the highest-numbered row for an instance would let the next rotation
  reuse a generation number that a stale container may still be labelled with.

The `'purged:%'` guard appears in three places — the constant
`purgedTokenPrefix`, `ListPurgeableGenerationTokens`, and
`PurgeContainerGenerationToken`. Keep them in step: a mismatch would make the
sweep rewrite every historical row on every pass forever.

## Manual posture

GC **reports and removes nothing**, with one deliberate exception.

| Resource | Managed | Manual |
|---|---|---|
| Unreferenced images | reclaimed | named in `ImagesReclaimable`, left alone |
| Orphaned subnets | released | named in `SubnetsReleasable`, left alone |
| Revoked token hashes | purged | **purged** |

`GCResult.Applied` is `false` in manual posture, and the write-impossibility is
structural rather than conditional: `container.ReadOnlyRuntime.ImageRemove`
returns `ErrManualModeWrite`, so even a future code path that forgot the
posture check cannot reach the daemon.

The token exception is principled, not an oversight. Manual mode constrains
what Gleipnir does to the **operator's** resources — their containers, their
images, their networks. A token hash in Gleipnir's own database is none of
those: it is credential material Gleipnir minted, and declining to clean it up
in the posture chosen for caution would leave it accumulating exactly where
someone was being careful.

## Configuration

| `reconciler.Config` field | Default | What it bounds |
|---|---|---|
| `GC` | *(required for `ReconcileGC`)* | The cleanup store. Nil makes `ReconcileGC` refuse rather than silently do nothing — "ran and reclaimed zero" and "never ran" are answers an operator watching disk usage must be able to tell apart. |
| `TokenRetention` | `24h` | How long a revoked token hash is kept |
| `ImagesPerPass` | `5` | Image reclaims per pass |
| `Now` | `time.Now` | The retention cutoff's clock; tests inject a fixed instant |

Token purges are additionally capped at 100 per pass — higher than the image
bound because a purge is a single indexed `UPDATE`, not a socket call.

## Out of scope

Host-wide `docker system prune` behaviour. Gleipnir only ever touches resources
carrying its own labels or recorded in its own tables; an image an operator
pulled for something else is not Gleipnir's to reclaim, however unreferenced it
looks from here.
