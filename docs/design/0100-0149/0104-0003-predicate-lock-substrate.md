# 0104-0003 — SIREAD Predicate-Lock Substrate

**Status:** accepted (M0104-0003 landed 2026-05-14)
**Date:** 2026-05-14
**Milestone:** M0104
**Tracks:** `.ralph/fix_plan.md` M0104-0003
**Upstream oracle:** `postgres/src/backend/storage/lmgr/predicate.c` and
`postgres/src/include/storage/predicate_internals.h`.

## Problem

M0104-0001/0002 added a SERIALIZABLE isolation level and a
per-transaction `SerializableXact` lifecycle, but the
`predicateLocks` slot on `SerializableXact` was a forward-declared
empty placeholder. Without an actual predicate-lock (SIREAD) substrate
there is no way to track which rows / pages / relations a SERIALIZABLE
reader observed, which is the load-bearing input to M0104-0004's
conflict-in hook, M0104-0005's conflict-out hook, and M0104-0006's
pre-commit dangerous-structure check.

This slice lands the substrate only — the data structures, registry,
coverage semantics, coarsening policy, and GUCs. The read-path /
write-path hooks that *call* `AcquirePredicateLock` and traverse the
holder set land in the next two slices.

## Goals

1. PostgreSQL-shaped predicate-lock identity:
   `(database, relation, page, tuple)` tagged record with granularity
   derived from sentinel fields.
2. Implicit-coverage relation so the substrate never stores two tags
   where one covers the other for the same xact.
3. Coarsening (escalation) policy under three thresholds
   (`PerPage`, `PerRelation`, `PerXact`) so SIREAD memory stays
   bounded under long-running serializable transactions.
4. GUC parity for the three sizing knobs upstream exposes.
5. Zero footprint for RC/RR/non-SERIALIZABLE workloads — every
   substrate allocation is lazy on first SERIALIZABLE acquire.
6. Automatic release on commit/abort wired into the existing
   `Manager.finish` lifecycle so SIREAD coverage can never leak past
   transaction boundaries in the first slice. Retention across commit
   for finished-but-still-relevant xacts is M0104-0006's concern.

## Non-Goals (First Slice)

- Read-path / write-path hooks — those acquire and traverse the
  substrate; deferred to M0104-0004 / M0104-0005.
- Cross-xact rw-edge bookkeeping — `inConflicts` /
  `outConflicts` remain empty placeholders on `SerializableXact`.
- Post-commit retention for finished xacts whose locks must still
  participate in dangerous-structure detection — M0104-0006 will
  revisit retention once the conflict graph exists.
- Index-am-specific predicate-target shapes (B-tree page-range
  locks, GIN/GiST nuances). The substrate intentionally exposes only
  the three classic levels; index-range coverage will piggyback on
  page-level coverage when the read-path hooks land.

## Implementation

### Tag shape

`PredicateLockTag` is a four-field struct `(DB, Rel, Page, Offset)`.
Granularity is encoded in sentinel fields:

| Granularity | `Rel` | `Page`              | `Offset` |
|-------------|-------|---------------------|----------|
| invalid     | 0     | any                 | any      |
| relation    | ≠ 0   | `InvalidBlockNumber`| 0        |
| page        | ≠ 0   | ≠ `InvalidBlockNumber` | 0     |
| tuple       | ≠ 0   | ≠ `InvalidBlockNumber` | ≠ 0   |

Constructors `RelationLockTag`, `PageLockTag`, `TupleLockTag`
enforce sentinel discipline by panicking on invalid inputs — callers
that have a granularity in mind must pick the right constructor.

The `Covers(other)` predicate captures the implicit-coverage
hierarchy: a relation-level lock covers every page/tuple in that
relation; a page-level lock covers every tuple on that page; a tuple-
level lock covers only itself. `Covers` short-circuits on `(DB, Rel)`
mismatch so cross-relation walks are O(1).

### Per-xact owned set

`SerializableXact.predicateLocks` becomes a
`map[PredicateLockTag]struct{}`. Set (not slice) so coverage checks
and coarsening pruning are O(holdings) without slice reshuffling.
Nil until first acquire so the cost contract from M0104-0002 holds.

### Global target map

`Manager.predicateLocks` is a `predicateLocksRegistry`:

- `targets map[PredicateLockTag]*predicateLockTarget` — inverted
  index from a tag to the set of `SerializableXact` handles that
  hold it. M0104-0005's conflict-out hook will walk this to discover
  which serializable readers must take a conflict-out edge from a
  serializable writer's heap modification.
- `limits PredicateLockLimits` — active coarsening thresholds,
  refreshed via `Manager.SetPredicateLockLimits` (the GUC bridge).

Empty target slots are evicted on the last holder release so the
global map size tracks live `(target, holder)` pairs exactly.

### Acquire

`Manager.AcquirePredicateLock(handle, tag)`:

1. Reject invalid tag (`Rel == 0`) — defensive against zero-value
   tags slipping through buggy callers.
2. No-op if `handle` is not in `ssiState.xacts` (RC/RR/finished xact
   passes through silently).
3. Coverage gate: if any owned tag `Covers(tag)`, return true
   without touching either map.
4. Pruning: detach every owned tag the new tag itself covers
   (e.g. acquiring a relation lock prunes tuple/page locks that
   relation already had from the same xact).
5. Install the new tag in both the per-xact set and the global
   target map.
6. Run coarsening (see below).

Step 3 makes acquire idempotent under monotonic coarsening; step 4
preserves the invariant that an xact never holds two tags where one
covers the other.

### Coarsening

`coarsenAfterAcquireLocked` is the only place that promotes locks.
Three stages, finest first:

1. **Per-page** — if the new tag is tuple-level and the xact now
   holds more than `PerPage` tuple-level locks on the same page,
   replace those tuples with a single page-level lock.
2. **Per-relation** — if (after step 1) the xact holds more than
   `PerRelation` page-level locks on the same relation, replace
   those pages (and any remaining tuple-level locks on that
   relation) with a single relation-level lock.
3. **Per-xact ceiling** — if the total predicate-lock count still
   exceeds `PerXact`, find the busiest `(db, rel)` footprint and
   promote it to a relation-level lock. Tie-breaker is `(db, rel)`
   ascending for deterministic test behaviour under Go's randomised
   map iteration.

`promoteToPageLocked` and `promoteToRelationLocked` are the two
primitive promotions. Both detach every tag the coarser tag will
cover before attaching the coarser tag, so the substrate-wide
invariant holds at every transition.

### Release

`Manager.releasePredicateLocksLocked(handle)` walks the xact's
owned set, removes each `(handle, tag)` pair from the global target
map, evicts emptied targets, and nulls the per-xact map. Called from
`releaseSerializableLocked` *before* `FinishedAt` is stamped — the
`SerializableXact` is still addressable via the registry while the
release runs, so future tooling that wants to log "released N
predicate locks at finish" can rejoin both pieces.

### GUC parity

Three GUCs registered in `internal/config/defaults.go`:

| GUC | BootVal | Range | Context |
|-----|---------|-------|---------|
| `max_predicate_locks_per_xact`    | 64 | [10, 1<<30] | postmaster |
| `max_predicate_locks_per_relation`| -2 | [-1<<30, 1<<30] | sighup |
| `max_predicate_locks_per_page`    | 2  | [0, 1<<16]  | sighup |

Names, defaults, and ranges mirror
`postgres/src/backend/utils/misc/guc_tables.c`. The `-N` shorthand
for `per_relation` is surfaced verbatim — the server-side bridge
into `Manager.SetPredicateLockLimits` is the only place that
resolves it into positive coarsening thresholds. That bridge lands
naturally when M0104-0004 wires the read-path hook through the
executor; for this slice the GUCs exist (so `pg_settings` / `SHOW`
both report PG-parity values) and the Manager uses
`DefaultPredicateLockLimits()` until SetPredicateLockLimits is
called.

## Regression Pins (`internal/mvcc/predlock_test.go`)

- `TestPredicateLockTag_Granularity` — sentinel-encoded granularity
  for all four cases.
- `TestPredicateLockTag_Covers` — implicit-coverage relation for
  every (granularity × granularity) pair.
- `TestPredicateLock_AcquireOnlyForSerializable` — RC/RR Acquire is
  a no-op with zero allocation footprint.
- `TestPredicateLock_AcquireAndReleaseOnCommit` — global target map
  drains on commit and rollback (table-driven over both kinds).
- `TestPredicateLock_IdempotentUnderCoarserOwnership` — tuple
  acquire after a covering relation lock is a no-op.
- `TestPredicateLock_AcquireCoarserPrunesFiner` — relation lock
  prunes finer tags from the per-xact set and from the global map.
- `TestPredicateLock_PerPageCoarsening` — three tuples → one page
  lock with covered-tuple semantics preserved.
- `TestPredicateLock_PerRelationCoarsening` — three pages × three
  tuples → one relation lock.
- `TestPredicateLock_PerXactOverflowCoarsens` — ceiling promotion
  targets the busiest relation, preserves other relations.
- `TestPredicateLock_GlobalTargetHoldersTrackMultipleXacts` —
  inverted index correctness across two xacts.
- `TestPredicateLock_InvalidTagRejected` — zero tag refused.
- `TestPredicateLock_LimitsRoundTrip` — partial setter doesn't
  clobber other dimensions; defaults survive.

GUC pin (`internal/config/guc_test.go::TestPredicateLockGUCDefaults`):
- Boot values match `64 / -2 / 2`.
- `per_xact` rejects values below the upstream floor of 10.
- `per_relation` accepts the negative shorthand.
- `per_page` rejects negatives.

## Forward-looking notes

The substrate's release-on-finish policy is intentionally simple for
the first slice: SIREAD coverage disappears at the moment its owning
xact finishes. M0104-0006's dangerous-structure check needs to
observe lock state from finished-but-still-conflict-relevant xacts;
when that slice lands, the release path moves from "drop targets" to
"hand targets off to a deferred-cleanup queue keyed on
`FinishedAt`," and the conflict-graph walker drains the queue once
all in-flight xacts have committed past those CommitSeqNos. The
substrate's API surface (Acquire/Holds/Release/Count) does not need
to change for that revision.
