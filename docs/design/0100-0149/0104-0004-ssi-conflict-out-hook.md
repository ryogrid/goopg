# 0104-0004 — Read-Path SSI Conflict-In Hook

**Status:** accepted (M0104-0004 landed 2026-05-14)
**Date:** 2026-05-14
**Milestone:** M0104
**Tracks:** `.ralph/fix_plan.md` M0104-0004
**Upstream oracle:** `postgres/src/backend/storage/lmgr/predicate.c`
(`CheckForSerializableConflictOut`, `RWConflict` linkage).

## Problem

M0104-0002 carved out empty `inConflicts` / `outConflicts` slices on
`SerializableXact`, and M0104-0003 wired the SIREAD predicate-lock
substrate. The two slices put data structures in place but did not
install the rw-conflict edges that M0104-0006's pre-commit
dangerous-structure check needs to walk.

This slice lands the read-path half of the conflict-edge machinery:
the entry point that runs on the read side and registers an
rw-conflict edge between a SERIALIZABLE reader and any concurrent
SERIALIZABLE writer whose modification the reader just observed. The
symmetric write-path entry point (`CheckForSerializableConflictIn`)
and the pre-commit check are M0104-0005 / M0104-0006's slices.

## Goals

1. Public API on `mvcc.Manager` named identically to PostgreSQL's
   `CheckForSerializableConflictOut`. A single call captures: "I am
   a SERIALIZABLE reader, I just read a tuple version whose
   xmin/xmax was written by transaction X — register the rw-conflict
   edge."
2. Symmetric bookkeeping: every edge is installed in both directions
   (`reader.outConflicts += writer`, `writer.inConflicts += reader`)
   so M0104-0006 can walk in either direction in O(deg(node)).
3. Idempotent under repeat calls: many tuples can map to the same
   `(reader, writer)` pair (e.g. multiple rows from one xact's
   modifications), so the entry point must dedupe.
4. Lifecycle correctness: when a SerializableXact finishes, every
   peer's slice must be scrubbed of its references. Without scrub,
   surviving peers retain dangling pointers to xacts that are no
   longer in `ssiState.xacts`.
5. Zero footprint for RC/RR/non-SERIALIZABLE workloads — every entry
   point is a fast no-op for handles that are not registered in
   `ssiState.xacts`.

## Non-Goals (This Slice)

- Wiring the hook into every executor call site (heap scan, index
  scan, tuple-by-CTID fetch). The mvcc API is the contract; call-site
  wiring lands when M0104-0007 promotes deferred isolation tests and
  forces the read paths through the hook.
- Write-path `CheckForSerializableConflictIn` — M0104-0005.
- Pre-commit `Doomed` evaluation and SQLSTATE `40001` abort —
  M0104-0006.
- Post-commit retention for finished-but-still-conflict-relevant
  writers. The first slice silently drops edges against finished
  writers; M0104-0006 will revisit retention.

## Implementation

### Entry point

```go
func (m *Manager) CheckForSerializableConflictOut(
    readerHandle TxnHandle,
    writerXID    storage.TransactionID,
) bool
```

Returns `true` iff a new edge was installed.

No-op cases (return `false` without touching state):

- `writerXID ∈ {Invalid, Bootstrap, Frozen}` — system xacts do not
  participate in SSI cycles.
- `readerHandle` is not in `ssiState.xacts` — RC/RR or already
  finished; RC/RR cannot participate in SSI cycles, and finished
  readers have their edges scrubbed at release.
- `reader.XID == writerXID` — self-modify is not a conflict.
- `writerXID` resolves to no live SerializableXact via
  `serializableXactByXIDLocked` — either RC/RR writer (no SSI
  bookkeeping) or the writer has finished. Retention of finished
  writers is M0104-0006's concern.

### Writer lookup by XID

```go
func (m *Manager) serializableXactByXIDLocked(
    xid storage.TransactionID,
) *SerializableXact
```

Linear scan of `ssiState.xacts`. The active SERIALIZABLE set is
bounded by `max_connections`; a dedicated XID-keyed map would add
allocation and synchronisation cost the workload does not benefit
from at the current scale. If profiling later shows the scan
dominates, the substrate's `attachPredicateLockLocked` /
lifecycle code is the natural place to maintain a parallel
`xid → *SerializableXact` index.

### Edge installation

```go
func registerRWConflictLocked(from, to *SerializableXact) bool
```

Walks `from.outConflicts` for an existing pointer to `to`; returns
`false` if found (idempotent). Otherwise appends `to` to
`from.outConflicts` and `from` to `to.inConflicts`. Duplicate
detection is O(deg(from.outConflicts)), bounded by the small number
of concurrent SERIALIZABLE writers.

### Peer scrub on finish

`releaseSerializableLocked` now calls
`removeSerializableXactFromPeersLocked(sx)` *before* nulling the
dying xact's own slices and removing it from `ssiState.xacts`. The
scrub walks `sx.outConflicts` and removes `sx` from each peer's
`inConflicts`; then walks `sx.inConflicts` and removes `sx` from
each peer's `outConflicts`. `removeSerializableXactFromSlice` does
the in-place compaction and zeros the released tail so the dying
xact's pointer is eligible for GC.

This invariant is load-bearing for M0104-0006: the dangerous-
structure walker must never dereference a stale pointer to an
already-released SerializableXact.

### Diagnostic helpers

`OutConflictCount`, `InConflictCount`, and `HasRWConflict` expose
the slices for tests and future tooling. M0104-0006's pre-commit
check walks the slices directly via the package-internal accessors,
so the public diagnostics never become a critical path.

## Regression Pins (`internal/mvcc/ssi_conflict_test.go`)

- `TestCheckForSerializableConflictOut_RegistersEdgeBetweenSerializable`
  — happy path: SERIALIZABLE reader against SERIALIZABLE writer
  installs the edge in both directions; the reverse direction is not
  installed.
- `TestCheckForSerializableConflictOut_IdempotentEdgeInstall` —
  second call returns false; out/in counts stay at 1.
- `TestCheckForSerializableConflictOut_NoOpForRCReader` /
  `_NoOpForRRReader` — RC and RR readers never install edges.
- `TestCheckForSerializableConflictOut_NoOpForRCWriter` — writer is
  RC (not in `ssiState.xacts`); call is a no-op.
- `TestCheckForSerializableConflictOut_NoOpForSelfXID` — reader's
  own XID does not generate an edge.
- `TestCheckForSerializableConflictOut_NoOpForReservedXIDs` —
  Invalid/Bootstrap/Frozen are skipped.
- `TestCheckForSerializableConflictOut_NoOpForFinishedWriter` —
  finished writer (no longer in `ssiState.xacts`) yields a silent
  no-op. Retention is deferred to M0104-0006.
- `TestCheckForSerializableConflictOut_NoOpForUnknownReader` —
  defensive: bad handle returns false without panicking.
- `TestSerializableXact_PeerEdgesScrubbedOnCommit` — after writer
  commits, the still-live reader's `outConflicts` no longer
  contains the writer pointer.
- `TestSerializableXact_PeerEdgesScrubbedOnAbort` — after reader
  rolls back, the still-live writer's `inConflicts` no longer
  contains the reader pointer.
- `TestCheckForSerializableConflictOut_MultiplePeersDistinctEdges`
  — one reader, three writers; the reader's outConflicts grows to 3,
  each writer's inConflicts stays at 1.

## Forward-looking notes

When M0104-0005's write-path hook lands, it will reuse
`registerRWConflictLocked` from this slice with the polarity
reversed: the writer-side hook installs `(reader → writer)` edges
discovered through the SIREAD lock holder set, while this slice's
read-side hook installs the same edge orientation but discovers
peers via the tuple's xmin/xmax. Both call sites must produce the
*same* edge orientation (R → W), so the helper is intentionally
polarity-agnostic.

M0104-0006's pre-commit check will walk a SerializableXact's
in/outConflicts slices to detect dangerous structures (pivot xacts
with both an inbound and outbound rw-conflict against still-active
peers). The peer-scrub invariant established here guarantees the
walker never dereferences a stale pointer.

Post-commit retention (so committed writers remain conflict-
relevant until all in-flight readers finish) is M0104-0006's
concern. The release-on-finish policy in this slice mirrors the
release-on-finish policy M0104-0003 set for predicate locks: both
will move to a deferred-cleanup queue keyed on `FinishedAt` when
M0104-0006 lands, without changing the public API surface.
