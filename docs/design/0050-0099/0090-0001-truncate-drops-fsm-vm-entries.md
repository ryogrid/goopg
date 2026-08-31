# Design 0090-0001 — TRUNCATE / DROP drops FSM + VM entries

**Status:** authoritative for M0090-0001 implementation.
**Milestone:** [M0090](../../milestones/0090-pgbench-scale-100-mvcc-and-insert-bugs.md).

## Problem

`internal/executor/operators_ddl.go::execTruncate` resets the
on-disk relation file via `Manager.TruncateRelation` (nblocks →
0) and invalidates buffer-pool slots via `Pool.InvalidateRel`,
but it never clears the in-memory Free Space Map (FSM) or
Visibility Map (VM) state for the affected relations.

After a TRUNCATE, the FSM still answers
`GetPageWithFreeSpace(rel, _)` with block numbers that no longer
exist on disk. The next INSERT consults the FSM, gets a stale
`fsmBlk` (e.g. 0), calls `tryAppendToBlock(0)` →
`Pool.Pin({rel, blk=0})` → `Manager.ReadBlock(rel, 0, buf)` —
which returns `ErrShortRead` because `blk >= nblocks` (0 >= 0
post-truncate). The transaction aborts with `ERROR: short read
at block`.

Same gap for `execDropTable` and any other path that
`TruncateRelation` / `DropRelation`s a file — the FSM/VM
entries become stale immediately and corrupt subsequent
operations on the new relation that gets created in the same
oid slot (or on the recreated empty file).

`FSM.DropRelation` already exists at
`internal/storage/fsm.go:89-100` with the exact doc-comment
"Called on DROP TABLE / TRUNCATE to prevent stale entries from
directing inserts to non-existent pages" — but is never called.
The VM has no equivalent method yet; this design adds one.

## Approach

1. Add `VisibilityMap.DropRelation(rel)` mirroring
   `FSM.DropRelation`'s shape.
2. In `execTruncate`, after `InvalidateRel + TruncateRelation`,
   call `ctx.FSM.DropRelation(rel)` and `ctx.VM.DropRelation(rel)`
   for both the heap rel AND each index relfile that gets
   truncated.
3. Apply the same cleanup to `execDropTable` (around the
   `Manager.DropRelation` call).
4. Audit any other site that resets nblocks (currently only
   `Manager.TruncateRelation` + `Manager.DropRelation`). If
   future code adds another path, it must also call the FSM/VM
   cleanup.

The cleanup runs under the executor's per-statement scope and
is idempotent — repeated calls on an already-cleared relation
are no-ops.

## Why this hasn't bitten harder before

- TRUNCATE is rare in normal workloads; only DDL-heavy paths
  trigger it.
- pgbench's pre-run step does `TRUNCATE pgbench_history`
  before EVERY workload run, which is why this surfaced as a
  scale-100 pgbench failure.
- Tests cover INSERT and UPDATE flow but not the
  TRUNCATE-then-INSERT chain at scale.

## Edge cases

- TRUNCATE followed by no subsequent operation: FSM cleared,
  no functional change.
- TRUNCATE then CREATE INDEX: the index rel is fresh, FSM
  starts empty; no impact.
- Concurrent TRUNCATE + INSERT from another connection: PG
  documents this as undefined; goopg's TRUNCATE doesn't take
  a relation-level lock, so the same caveat applies. Out of
  scope here.

## Test coverage

`internal/executor/operators_ddl_truncate_fsm_test.go` (NEW):
1. Open a runtime with FSM + VM attached.
2. INSERT a row into a small table; capture
   `FSM.GetPageWithFreeSpace(rel, 1)` returns `(_, true)`.
3. `TRUNCATE` the table.
4. Assert `FSM.GetPageWithFreeSpace(rel, 1)` returns `(_, false)`.
5. INSERT again; assert it succeeds (no `short read at block`
   error).

## Out of scope (in M0090)

- `FSM.DropRelation` is not WAL-logged. A crash between
  TRUNCATE and the next checkpoint would replay
  `XLOG_SMGR_TRUNCATE` (if implemented) which would
  re-establish the empty state — but the FSM cleanup is in-
  memory only and re-runs on every restart. A real
  durability story for FSM cleanup needs a follow-up
  milestone.
- `VM.DropRelation` similarly.
