# 0104-0006 — Pre-Commit Dangerous-Structure Detection (SSI)

**Status:** accepted (M0104-0006 landed 2026-05-14)
**Date:** 2026-05-14
**Milestone:** M0104
**Tracks:** `.ralph/fix_plan.md` M0104-0006
**Upstream oracle:** `postgres/src/backend/storage/lmgr/predicate.c`
(`PreCommit_CheckForSerializationFailure`,
`OnConflict_CheckForSerializationFailure`, `FlagRWConflict`,
`SXACT_FLAG_DOOMED`).

## Problem

M0104-0003..0005 built the SSI substrate: predicate locks (SIREAD),
read-path conflict-out edges, write-path conflict-in edges, and a
peer-scrub invariant that keeps the rw-conflict graph consistent
across commit/abort. None of that bookkeeping is *enforcing* anything
yet — a SERIALIZABLE workload with the graph fully populated still
commits whatever it wants, indistinguishable from REPEATABLE READ.

The DoD for M0104 requires "at least one known serializable anomaly
pattern (write-skew / dangerous rw-cycle shape) deterministically
rejected with SQLSTATE 40001". The canonical write-skew shape is the
2-cycle `T1 -rw-> T2 -rw-> T1`: each xact reads what the other writes,
neither sees the other's writes, and the rows are unrelated so no
prior MVCC machinery catches the conflict. SI commits both. SSI must
abort one.

## Goals

1. Public API on `mvcc.Manager`:

   ```go
   func (m *Manager) PreCommitCheckForSerializationFailure(handle TxnHandle) error
   ```

   Runs the dangerous-structure scan over the rw-conflict graph
   reachable from the committing SERIALIZABLE transaction. Returns
   `nil` for non-SERIALIZABLE handles, read-only SERIALIZABLE handles
   with empty graphs, and SERIALIZABLE handles whose scan finds no
   dangerous structure. Returns `*SerializationFailureError` (SQLSTATE
   40001) iff the committing xact has been doomed.

2. Typed error `mvcc.SerializationFailureError` carrying SQLSTATE
   `40001` plus an upstream-style "Reason code" detail string so test
   scaffolding written against upstream errordetail recognises the
   goopg variant verbatim. Convenience predicate
   `mvcc.IsSerializationFailure(err)` for executor callers that do not
   want to type-assert.

3. Hook into `Manager.finish` for `kind == XactCommit && isolation ==
   IsolationSerializable`. On error return, the transaction stays in
   `m.active` so the caller (executor) calls `Rollback` to perform the
   actual cleanup. This mirrors PostgreSQL's flow out of
   `PreCommit_CheckForSerializationFailure` —
   `ereport(ERROR, ...)` longjmps out, and the abort path runs the
   real cleanup.

4. Doom-the-pivot in place: the scan walks `me.inConflicts →
   pivot.inConflicts` and sets `pivot.Doomed = true` for every pivot
   that participates in a dangerous structure. The pivot then fails at
   its own pre-commit attempt with the "Canceled on identification as
   a pivot, during commit attempt" reason. The committing xact itself
   is not doomed by its own scan — that progress guarantee mirrors
   upstream's "letting it commit ensures progress" policy.

5. Zero footprint for RC/RR workloads. RC/RR xacts are never
   registered in `ssiState.xacts`, so the scan exits on the very first
   map probe.

## Non-Goals (This Slice)

- The `OnConflict_CheckForSerializationFailure` per-edge synchronous
  check that runs from `FlagRWConflict` in upstream. That check catches
  earlier (during edge installation) instead of at commit; goopg's
  edge-installation paths from M0104-0004/0005 already register edges
  without invoking it. The pre-commit scan is sufficient for the M0104
  DoD because every dangerous structure manifests by the time *some*
  participant commits, and the scan dooms the pivot before peers can
  miss the detection window.

- Retention of finished-but-still-conflict-relevant xacts past their
  `FinishedAt`. The first-slice substrate (M0104-0003..0005) scrubs
  finished xacts from peer slices at release; the pre-commit scan is
  correct under this policy because the scan runs *while the
  committing xact is still addressable* and *before* its peers are
  scrubbed — exactly the upstream window. False negatives at the
  boundary case "T0 commits, then Me's scan would have walked into T0
  but it's gone" are acceptable because the dangerous structure still
  required a 2- or 3-cycle that the first-committing xact's own scan
  would have caught. Long-term retention work is staged for a future
  slice once the executor-driven workload exposes a concrete miss
  pattern.

- READ ONLY distinct lifecycle. Upstream's pre-commit check has an
  optimisation that skips Tin when Tin is READ ONLY and overlaps the
  writer (because a read-only Tin cannot cause an anomaly downstream).
  goopg does not yet model READ ONLY transactions distinctly; the scan
  is conservatively absent the optimisation, producing only false
  positives (retried xacts), never false negatives.

- Executor-side wiring at every commit call site. The executor's
  `execCommit` already calls `Manager.Commit`, which now invokes the
  pre-commit scan. The executor's `XX000`-wrapping of `Commit` errors
  is the only follow-up; M0104-0007 (oracle test promotion) will be
  the driver that surfaces the typed error through `ExecError{Code:
  "40001"}`. The mvcc-level contract is the load-bearing piece.

- Prepared-transaction (`PREPARE TRANSACTION`) interactions. goopg
  does not yet implement 2PC; upstream's `SxactIsPrepared` branch is
  not relevant here.

## Implementation

### Algorithm

The scan mirrors upstream's `PreCommit_CheckForSerializationFailure`
pseudocode:

```
preCommit(me):
    if me is not in ssiState.xacts:
        return nil                   # RC/RR — no-op
    if me.Doomed:
        return 40001 ("Canceled on identification as a pivot, during commit attempt")
    for pivot in me.inConflicts:
        if pivot.FinishedAt != 0 or pivot.Doomed:
            continue
        for tin in pivot.inConflicts:
            if tin == me                       # 2-cycle (write-skew)
               or (tin.FinishedAt == 0 and not tin.Doomed):  # 3-cycle
                pivot.Doomed = true
                break
    return nil
```

The structure being detected is `Tin -rw-> Tpivot -rw-> Me`, where
Tpivot is the "near" entry in my `inConflicts` (`Tpivot -rw-> Me` =
"Tpivot read what I wrote"), and Tin is the upstream of Tpivot
(`Tin -rw-> Tpivot` = "Tin read what Tpivot wrote"). Edge direction in
goopg matches upstream: `registerRWConflictLocked(reader, writer)`
appends `writer` to `reader.outConflicts` and `reader` to
`writer.inConflicts`.

The two trigger conditions encode the two anomaly shapes the scan
must catch:

- **2-cycle (write-skew):** Tin == Me. The classic case. Me reads
  something Tpivot wrote AND wrote something Tpivot read; symmetrically
  Tpivot did the same. Both xacts believe they have a consistent
  snapshot, but the combined effect violates serializability. Dooming
  Tpivot lets Me commit cleanly and forces Tpivot's later commit
  attempt to fail.

- **3-cycle (generic dangerous structure):** Tin is a different
  in-flight, non-doomed xact. Even though Me does not directly
  participate in a cycle with Tin yet, the structure
  `Tin -> Tpivot -> Me` is enough for upstream to abort — when Tin
  later acquires its own out-edges, the cycle closes. Dooming Tpivot
  preemptively is the conservative choice (false positives, never
  false negatives).

### Doom-the-pivot semantics

Setting `pivot.Doomed = true` does not abort `pivot` immediately. The
pivot continues executing; only its next commit attempt fails. This
matches upstream's `SXACT_FLAG_DOOMED` semantics and is what makes
"letting Me commit ensures progress" possible — if we aborted the
pivot now, the pivot would retry and potentially fail again on the
same anomaly, while Me's writes are still uncommitted.

### Self never dooms itself

The scan never sets `me.Doomed = true`. The committing xact is allowed
to commit (assuming it was not already doomed by an earlier scan from
a peer that completed before it). The only way `me.Doomed` becomes
true is via a peer's earlier pre-commit scan. This is the progress
guarantee. (See upstream's comment: "If we canceled the far conflict,
it might immediately fail again on retry.")

### Wiring into `Manager.finish`

```go
if state.isolation == IsolationSerializable && kind == XactCommit {
    if err := m.preCommitCheckForSerializationFailureLocked(tx.Handle); err != nil {
        return err
    }
}
```

The scan runs BEFORE the WAL xact-marker hook, BEFORE
`releaseSerializableLocked`, BEFORE `delete(m.active, tx.Handle)`. On
error, the transaction remains in `m.active`; the caller MUST invoke
`Manager.Rollback(tx)` to drive the actual abort and drain the active
set. Side effects observed: SSI bookkeeping cleared (with `kind ==
XactAbort` semantics), no WAL XactCommit record, no commit-LSN
durability wait.

This contract is identical to upstream's flow out of `PreCommit_*` —
the longjmp lands in the abort path, which calls the SSI release with
abort semantics.

### Doomed-pivot's own commit

When the doomed pivot later calls `Manager.Commit`, the scan re-runs
and finds `me.Doomed == true` first, returning the
SerializationFailureError immediately. The doomed pivot's caller then
calls `Rollback`, which finishes cleanup.

### Error type

```go
type SerializationFailureError struct{ Reason string }
func (e *SerializationFailureError) Error() string
func (e *SerializationFailureError) SQLSTATE() string  // returns "40001"
func IsSerializationFailure(err error) bool             // errors.As wrapper
```

`Reason` is the upstream "Reason code" detail phrase. The pre-commit
scan currently emits one reason: "Canceled on identification as a
pivot, during commit attempt". When the on-conflict per-edge check
lands in a future slice it will add other reasons; the public surface
is already future-compatible.

## Test-Only Helpers

Two test-only mutators expose internal state so the scan can be
exercised in isolation:

- `Manager.MarkDoomedForTest(handle TxnHandle) bool` — sets
  `SerializableXact.Doomed = true` for the given handle. Mirrors
  upstream's `SXACT_FLAG_DOOMED` so the "self is already doomed"
  branch can be pinned without driving a full 2-cycle setup.
- `Manager.IsDoomedForTest(handle TxnHandle) bool` — reads the flag.

Both names end in `ForTest` so production callers must reach the
Doomed bit through `PreCommitCheckForSerializationFailure` instead of
poking it directly. The intent is to keep `Doomed` an *internal*
state machine — production code only consumes the typed error.

## Regression Pins (`internal/mvcc/ssi_precommit_test.go`)

- `TestPreCommitCheck_NoOpForRC` — RC xact never registered; commit
  cleanly returns nil.
- `TestPreCommitCheck_NoOpForReadOnlySerializable` — SERIALIZABLE xact
  with empty graph commits cleanly; no false positive against an
  unconnected node.
- `TestPreCommitCheck_AlreadyDoomedReturns40001` — `MarkDoomedForTest`
  + scan returns `*SerializationFailureError` with SQLSTATE 40001;
  `IsSerializationFailure` recognises the error; `Manager.Commit`
  propagates the same error; `Rollback` drains the active set.
- `TestPreCommitCheck_WriteSkewDoomsPivot` — canonical 2-cycle: T1
  reads X, T2 reads Y, T1 writes Y, T2 writes X. Both edges installed
  via the write-path hook. T1 commits cleanly; T2's commit returns
  40001. Pins the M0104 DoD anomaly pattern end-to-end through the
  mvcc layer.
- `TestPreCommitCheck_ThreeNodeCycleDoomsPivot` — generic 3-node
  dangerous structure `T0 -rw-> Pivot -rw-> Me`. Me commits cleanly;
  Pivot is doomed; T0 commits cleanly; Pivot's commit returns 40001.
- `TestPreCommitCheck_LinearChainIsSafe` — `T0 -rw-> Pivot -rw-> Me`
  where T0 already committed (its edges are scrubbed by the
  M0104-0004 invariant). Me's scan finds nothing to doom; all three
  xacts commit cleanly. Pins that the scan does not false-positive on
  linear chains where the upstream node is already finished.
- `TestPreCommitCheck_FinishedPivotIgnored` — defensive pin against a
  future change to retain finished xacts in `ssiState.xacts`: a
  manually-stamped FinishedAt on Pivot makes the scan skip it even if
  the graph would otherwise trigger detection.
- `TestPreCommitCheck_IdempotentDoomedPivot` — second pre-commit scan
  on the same graph is a no-op; pivot stays doomed; T2's later commit
  still returns 40001.

## Forward-looking notes

**M0104-0007 (oracle test promotion).** Deferred D-002 isolation tests
under `postgres/src/test/isolation` include `simple-write-skew.spec`
(the canonical write-skew shape pinned by
`TestPreCommitCheck_WriteSkewDoomsPivot`) plus several other
serializable spec files (`read-write-unique-*`, `ssi-*`,
`receipt-report`, etc.). M0104-0007 will:

1. Wire executor read-path / write-path call sites into
   `Manager.CheckForSerializableConflictOut` and
   `Manager.CheckForSerializableConflictIn` (currently the hooks are
   public on Manager but no executor call site invokes them — that
   wiring is the gating dependency for any real spec to exercise this
   scan).
2. Surface `*SerializationFailureError` through the executor's
   `execCommit` wrapper as `ExecError{Code: "40001"}` instead of the
   current `XX000`-wrap of generic Commit errors. The cleanup path
   (Rollback + EndExplicitTransaction + clearCtxTransaction) is
   already present in `execRollback`; the wiring change is purely
   converting the error type and routing the cleanup.
3. Promote the applicable deferred isolation specs to `pass_required:
   yes` in `docs/test-port/postgres-oracle-port-status.csv` and
   regenerate the markdown view.

**Post-commit retention.** When real executor workloads land in
M0104-0007 and isolation-spec timing exposes the "T0 commits, then
Me's scan would have walked into T0 but it's gone" miss pattern, the
substrate will need to retain finished xacts in `ssiState.xacts` past
their `FinishedAt` (mirroring upstream's `OldCommittedSxact` or a
deferred-cleanup queue keyed on `FinishedAt`). The pre-commit scan in
this slice is forward-compatible with that change — the scan reads
`pivot.FinishedAt != 0` to skip finished pivots, which under the
deferred-cleanup model becomes "skip but the pivot is still
addressable for in-flight tin candidates to find through their own
inConflicts slices".

**OnConflict per-edge check.** Upstream's
`OnConflict_CheckForSerializationFailure` runs from `FlagRWConflict`
on edge installation and catches some anomalies earlier than commit
time. The pre-commit scan in this slice is sufficient for the M0104
DoD but does not subsume the per-edge check — there exist anomaly
shapes where the per-edge check kills a worse-positioned xact than
the pre-commit scan would. Adding the per-edge variant is a pure
addition; the polarity-agnostic `registerRWConflictLocked` helper is
the natural injection point, and `SerializationFailureError`'s
`Reason` field is already future-proofed for the upstream
"Canceled on conflict out to pivot %u, during read." phrasing.
