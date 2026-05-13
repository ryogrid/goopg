# 0100-0002 — Eager XID Materialisation for ON CONFLICT Wait Propagation

**Status:** accepted
**Date:** 2026-05-13
**Milestone:** M0100-0002
**Closes:** M0096-0005 (ON CONFLICT executor correctness — wait-state propagation)

## Problem

M0096-0005 wired the ON CONFLICT row-wait path:
- `internal/executor/operators_upsert.go:264-294` adds `probeArbiterWaiting`
  + `findInProgressConflict`.
- `internal/mvcc/manager.go:338-358` `WaitForXID` blocks on `commitCond.Wait`
  until the holder XID leaves `m.active`.

The mechanism is correct in isolation, but the `insert-conflict-do-update`
and `insert-conflict-do-nothing` specs still defer — donothing2 / insert2
never emit `<waiting …>`. Root cause:

**M0093 (read-only commit skip) made XID allocation lazy.** A transaction
that has only run SELECTs has no XID. When session 2 then runs
`INSERT … ON CONFLICT`, `findInProgressConflict` scans the arbiter index
for tuples with an `xmin` belonging to an `m.active` XID — but session 1
(which ran a still-uncommitted INSERT) hasn't necessarily flushed its
XID into the active set by the time session 2 probes, or session 2's
probe is racing the XID materialisation.

The fix is to make sure that **once an INSERT has actually written a
heap tuple, that session's XID is in `m.active` and the tuple's `xmin`
matches**, so a concurrent `findInProgressConflict` will both see the
in-progress XID and successfully `WaitForXID` on it.

## Investigation prerequisite (loop opening)

The XID is supposed to be materialised at the first write — that is
M0093's contract. Before changing anything, verify the actual code
path: find every site that calls `MaterializeWriterXID`
(`grep -n MaterializeWriterXID internal/...`) and confirm that the
INSERT path through `operators_insert.go` / `operators_upsert.go`
materialises *before* the heap-insert returns. If a write returns
with an unassigned XID, that is the bug; fix it at the write site, not
at BEGIN.

If materialisation is correct but the upsert *probe* runs before the
other session has finished its INSERT, the issue is timing: the probe
must re-poll or rely on row-lock contention, not on a one-shot scan.

## Solution (two-layer)

### Layer A: XID-on-first-write invariant

Audit and (if necessary) tighten:

- `internal/executor/operators_insert.go` — call `ctx.MaterializeWriterXID`
  before the heap page is written, not after.
- `internal/executor/operators_upsert.go` — same; the upsert's INSERT
  branch must materialise before `findInProgressConflict` returns to
  the caller.

This preserves M0093's read-only TPS win (read-only txns never reach
these sites) and closes any window where a heap tuple is visible with
a not-yet-active XID.

### Layer B: probe + wait, not probe-once

`probeArbiterWaiting` currently does one scan and returns. Update it to:

1. Scan arbiter index for an existing committed row → conflict path.
2. Scan for an in-progress row (`xmin` is an active XID) → wait path:
   call `WaitForXID(xmin)`, then re-scan.
3. No conflict → proceed to insert.

`WaitForXID` already drains `commitCond` correctly; the re-scan on
wakeup is what closes the race when session 1 INSERTs *during*
session 2's probe.

### What we explicitly do NOT do

- We **do not** materialise XID at `BEGIN`. Doing so would regress M0093's
  pgbench-S 2,740 TPS (XID allocation under contention is what M0093
  removed for read-only commits).
- We do not change `connTxState.Begin`. The XID lives on `mvcc.Transaction`
  via M0093's `TxnHandle`; the dispatcher already plumbs it.

## Files touched

- `internal/executor/operators_insert.go` — ensure `MaterializeWriterXID`
  precedes the heap write.
- `internal/executor/operators_upsert.go` — same; plus probe+wait+re-scan
  loop in `probeArbiterWaiting`.
- `internal/mvcc/manager.go` — no behavioural change. `WaitForXID` already
  correct.

## Reference (upstream)

- `postgres/src/backend/access/heap/heapam.c` — `heap_insert` calls
  `GetCurrentTransactionId()` which materialises the XID before stamping
  `t_xmin`.
- `postgres/src/backend/executor/nodeModifyTable.c` —
  `ExecOnConflictUpdate` and `ExecInsert` interact via
  `XactLockTableWait` + re-fetch on the arbiter scan.

## Verification

- `TestPort_IsolationInsertConflictDoUpdate` (1–4), `TestPort_IsolationInsertConflictDoNothing`,
  `TestPort_IsolationInsertConflictSpecconflict` reach `pass` — donothing2
  or insert2 emits `<waiting …>` in the rendered output.
- pgbench-S regression check: `-c 10 -T 30` reports ≥ 2,000 TPS (M0093
  baseline 2,740; small regressions OK, large ones are a blocker).
- `go test -race ./internal/executor/... ./internal/mvcc/...` clean.

## Risks

- Re-scan loop in `probeArbiterWaiting` could deadlock if two upserts
  arbitrate on each other. Mitigation: `WaitForXID` already participates
  in deadlock detection via `lockmgr` (M0012 work); confirm via a
  symmetric test (`insert-conflict-specconflict` exercises this).
- M0093 invariant: `OldestXmin` continues to track read-only RR snapshots.
  Eager-materialisation-on-first-write does not change the read-only
  invariant; verify by running the M0093 acceptance test if present.
