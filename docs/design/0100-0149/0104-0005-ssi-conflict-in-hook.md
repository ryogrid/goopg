# 0104-0005 — Write-Path SSI Conflict-In Hook

**Status:** accepted (M0104-0005 landed 2026-05-14)
**Date:** 2026-05-14
**Milestone:** M0104
**Tracks:** `.ralph/fix_plan.md` M0104-0005
**Upstream oracle:** `postgres/src/backend/storage/lmgr/predicate.c`
(`CheckForSerializableConflictIn`, `GetParentPredicateLockTag`,
`RWConflict` linkage).

## Problem

M0104-0004 landed the read-path entry point that registers an
rw-conflict edge `R → W` when a SERIALIZABLE reader observes a tuple
written by a concurrent SERIALIZABLE writer; discovery is by the
tuple's `xmin`/`xmax`. The other half of the conflict-edge machinery
is the write-path entry point that runs on the writer side and
registers the *same* edge `R → W` when a SERIALIZABLE writer touches
a target a concurrent SERIALIZABLE reader holds an SIREAD predicate
lock on; discovery is by walking the predicate-lock holder set.

Both sides must produce the same edge orientation (`R → W`) so the
M0104-0006 dangerous-structure walker sees a single, consistent
graph regardless of which side discovered the conflict first.

## Goals

1. Public API on `mvcc.Manager` named identically to PostgreSQL's
   `CheckForSerializableConflictIn`. A single call captures: "I am
   a SERIALIZABLE writer, I am about to modify the target identified
   by `tag` — register an rw-conflict edge against every concurrent
   SERIALIZABLE reader holding a SIREAD covering that target."
2. Reuse the polarity-agnostic `registerRWConflictLocked` helper from
   M0104-0004 so both sides install identical edges.
3. Coverage discovery follows upstream's "walk upward" pattern:
   holders on the exact tag plus holders on every ancestor tag that
   covers it (tuple → page → relation). The substrate's
   `PredicateLockTag.Covers` already defines the hierarchy; this
   slice does not re-derive coverage rules.
4. Idempotent under repeat calls and self-write — a single execution
   may fire the same `(reader, writer)` pair multiple times across
   per-row hooks; the entry point must dedupe and must not register
   `writer → writer` against itself if it happens to hold its own
   SIREAD on the target it's now writing.
5. Zero footprint for RC/RR/non-SERIALIZABLE workloads — every entry
   point is a fast no-op for handles that are not registered in
   `ssiState.xacts`. The hot-path "no covering holder" case must
   return `false` without allocating.

## Non-Goals (This Slice)

- Wiring the hook into every executor write call site
  (`executor.heapInsert`, `executor.heapUpdate`, `executor.heapDelete`,
  index-AM mutators). The mvcc API is the contract; call-site wiring
  lands when M0104-0007 promotes deferred D-002 isolation tests and
  forces real integration through this hook.
- Pre-commit `Doomed` evaluation and SQLSTATE `40001` abort —
  M0104-0006.
- Post-commit retention for finished-but-still-conflict-relevant
  readers. The first-slice substrate releases predicate locks at
  finish time, so finished readers are silently invisible to this
  hook; M0104-0006 will revisit retention together with the
  finished-writer retention in M0104-0004.
- Index-am-specific predicate-target shapes (B-tree page-range
  locks, GIN/GiST nuances). The substrate currently uses heap
  page/tuple targets only.

## Implementation

### Entry point

```go
func (m *Manager) CheckForSerializableConflictIn(
    writerHandle TxnHandle,
    tag          PredicateLockTag,
) bool
```

Returns `true` iff at least one new edge was installed. Existing
edges (idempotent calls, self-references) do not count toward the
return value.

No-op cases (return `false` without touching state):

- `tag.Granularity() == InvalidPredicateGranularity` — defensive
  against a caller that forgot to materialise the target tag from
  the actual write target.
- `writerHandle` is not in `ssiState.xacts` — RC/RR or already
  finished. RC/RR cannot participate in SSI cycles, and finished
  writers are scrubbed of their edges at release.
- `m.predicateLocks.targets == nil` — no predicate locks have ever
  been acquired in this Manager; a single map-nil short-circuit
  keeps the cost zero for SERIALIZABLE workloads that have not yet
  read anything.
- No holder is found on the exact or any covering tag — the most
  common hot-path case in practice.

### Coverage discovery

```go
func coveringPredicateLockTags(tag PredicateLockTag) []PredicateLockTag
```

Returns `tag` itself plus every coarser ancestor that, by the
substrate's coverage hierarchy, would also imply SIREAD on `tag`:

- `TupleGranularity` → `[tuple, page, relation]`
- `PageGranularity`  → `[page, relation]`
- `RelationGranularity` → `[relation]`

This is the goopg analogue of upstream's iterated
`GetParentPredicateLockTag` walk in `predicate.c`. The list is
finest-first; iteration order is observable in tests but never
load-bearing for correctness because `registerRWConflictLocked` is
idempotent. Finer descendants are deliberately not included —
PostgreSQL's `CheckForSerializableConflictIn` uses the same
upward-only pattern, and goopg's heap workload writes at the finest
granularity, so the descendant case does not arise.

### Edge installation

For each ancestor tag in the upward walk, the hook looks up the
substrate-internal `predicateLockTarget` for that tag and walks its
`holders` set:

- Skip the writer's own handle — a SERIALIZABLE xact may legitimately
  read a tuple (acquiring a SIREAD lock) and then write it; that is
  not a self-conflict.
- Skip holders not present in `ssiState.xacts` — defensive against
  the edge case where a holder slot outlives the SerializableXact
  (this should never happen because `releaseSerializableLocked`
  drops predicate locks before clearing the registry entry, but the
  guard keeps the hook robust to future lifecycle changes).
- Otherwise call `registerRWConflictLocked(reader, writer)` —
  identical to the read-path's edge orientation, identical
  bookkeeping, identical idempotence semantics.

The duplicate-edge check inside `registerRWConflictLocked` is
O(out-degree of the reader), bounded by the small number of
concurrent SERIALIZABLE writers in practice.

### Self-conflict handling

A SERIALIZABLE xact may legitimately hold a SIREAD lock on a target
and subsequently write that same target. Upstream's
`CheckForSerializableConflictIn` skips itself in the holder walk;
goopg matches that behaviour with a `holder == writerHandle` guard
inside the loop. This is distinct from the read-path's
`reader.XID == writerXID` self-XID guard — the write-path discovers
holders by handle, so the comparison is on handle, not XID.

### Peer scrub on finish

No new lifecycle code in this slice — the M0104-0004 invariant
(`releaseSerializableLocked` calls
`removeSerializableXactFromPeersLocked(sx)` before nulling its own
slices and removing from `ssiState.xacts`) already covers edges
installed by the write-path hook, because both sides use the same
in/outConflicts slices.

The symmetric counterpart of M0104-0004's
`TestSerializableXact_PeerEdgesScrubbedOnCommit` —
`TestCheckForSerializableConflictIn_PeerEdgesScrubbedOnReaderCommit`
— pins the invariant from the write-path side.

## Regression Pins (`internal/mvcc/ssi_conflict_test.go`)

- `TestCheckForSerializableConflictIn_RegistersEdgeForExactSIREADHolder`
  — happy path: SERIALIZABLE reader holding a tuple-level SIREAD;
  SERIALIZABLE writer modifies the same tuple; the edge is installed
  in both directions; the reverse direction is not installed.
- `TestCheckForSerializableConflictIn_IdempotentEdgeInstall` —
  second call returns false; out/in counts stay at 1.
- `TestCheckForSerializableConflictIn_FiresOnPageLockHoldingForTupleWrite`
  — reader holds a page-level lock; writer touches a tuple on that
  page; the upward walk surfaces the page-level holder.
- `TestCheckForSerializableConflictIn_FiresOnRelationLockHoldingForTupleWrite`
  — reader holds a relation-level lock; writer touches a tuple in
  that relation; the upward walk surfaces the relation-level holder.
- `TestCheckForSerializableConflictIn_NoOpForFinerDescendantHolder`
  — reader holds a tuple-level lock on a different tuple of the
  same page; writer touching its own tuple does not surface the
  unrelated reader lock.
- `TestCheckForSerializableConflictIn_NoOpForDifferentRelation` —
  reader holds a relation-level lock on relation A; writer touches
  relation B; coverage hierarchy keeps these disjoint.
- `TestCheckForSerializableConflictIn_NoOpForRCWriter` — RC writer
  is not in `ssiState.xacts`; the call is a no-op.
- `TestCheckForSerializableConflictIn_NoOpForSelfHolder` — writer
  holds its own SIREAD on the target; self-conflict is skipped.
- `TestCheckForSerializableConflictIn_NoOpForInvalidTag` — defensive
  against caller bugs.
- `TestCheckForSerializableConflictIn_NoOpForUnknownWriter` —
  defensive: bad handle returns false without panicking.
- `TestCheckForSerializableConflictIn_NoHoldersIsSilentNoOp` — hot
  path: most writes touch tags no concurrent reader covers; the
  hook returns false without allocating.
- `TestCheckForSerializableConflictIn_MultipleReadersDistinctEdges`
  — one writer, three readers covering at tuple/page/relation
  granularities; all three receive the edge in a single call.
- `TestCheckForSerializableConflictIn_PeerEdgesScrubbedOnReaderCommit`
  — symmetric counterpart of M0104-0004's commit-scrub test;
  validates that the M0104-0004 lifecycle invariant covers
  write-path-installed edges too.

## Forward-looking notes

M0104-0006's pre-commit dangerous-structure check walks the
in/outConflicts slices to detect pivot xacts (a transaction with
both an inbound and outbound rw-conflict against still-active
peers). The peer-scrub invariant established in M0104-0004 — and
relied on here — guarantees the walker never dereferences a stale
pointer to a released SerializableXact, regardless of which side
installed the edge.

When M0104-0007 wires the hooks into the executor, the write-path
hook will be called from the heap-AM mutators (`heapInsert`,
`heapUpdate`, `heapDelete`) at the same point that the per-row WAL
record is written, and from the index-AM mutators when index entries
are inserted/deleted. The hook's hot-path no-op cost (one map-nil
check + one handle lookup + one targets map probe per ancestor) is
designed to make this wiring safe to leave on for non-SERIALIZABLE
workloads.

Post-commit retention so committed readers stay conflict-relevant
until in-flight writers finish is M0104-0006's concern, paired with
the finished-writer retention deferred from M0104-0004. The
release-on-finish policy in this slice mirrors the policy
M0104-0003/0004 set for predicate locks and out-edges respectively;
the deferred-cleanup queue keyed on `FinishedAt` will land in
M0104-0006 without changing the public API surface.
