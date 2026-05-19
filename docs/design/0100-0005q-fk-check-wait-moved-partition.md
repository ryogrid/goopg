# 0100-0005q — FK INSERT check waits for in-flight xmax and surfaces cross-partition moves

Status: accepted (2026-05-15)
Milestone: M0100-0005q
Suite gate: `partition-key-update-1.spec` (range-parted leg, 6 permutations)

## Problem

`partition-key-update-1.spec`'s third group of permutations (`s1u3pc` / `s1u3npc`
/ `s2i`) exercises FK INSERT against `bar(a REFERENCES foo_range_parted1(a))`
while session s1 holds an in-flight UPDATE that either moves the row across
partitions (`s1u3pc`: `a=11 WHERE a=7`) or merely rewrites a non-key column
(`s1u3npc`: `b='XYZ' WHERE a=7`).  PostgreSQL's expected output is:

```
step s1u3pc:  UPDATE foo_range_parted SET a=11 WHERE a=7;
step s2i:     INSERT INTO bar VALUES(7); <waiting ...>
step s1c:     COMMIT;
step s2i:     <... completed>
ERROR:  tuple to be locked was already moved to another partition due to concurrent update
```

Before this fix goopg's FK INSERT check called `scanTableForMatch`, which:

- Found `a=7` in `foo_range_parted1` (visible to s2 — xmax was in-flight, not
  yet committed, so MVCC visibility kept the row live for s2).
- Returned `found=true` immediately.
- INSERT completed with no wait state, no error.

The failure mode was a silent shape mismatch: the spec's L72 diff captured
`<waiting ...>` absent, no `<... completed>` echo, no
`tuple to be locked was already moved to another partition` error.

## Why a wait is required

PostgreSQL's RI_FKey_check executes
`SELECT 1 FROM <reftable> WHERE refkey = $1 FOR KEY SHARE`.  The FOR KEY SHARE
clause acquires a row-level key lock and **serialises against in-flight
key-changing UPDATEs**: if a concurrent transaction holds an xmax stamp on
the matching parent row, the lock waits until that transaction commits or
aborts.  After the wait:

1. **Updater aborted** → the row is still live; the FK check succeeds.
2. **Updater committed, key preserved** (e.g., `s1u3npc` updated `b`, not
   `a`) → re-scan finds the new visible version that still matches the FK
   key; the FK check succeeds.
3. **Updater committed, cross-partition move** (`s1u3pc`) → the source
   partition no longer contains the key; the old slot's `t_ctid` carries
   the `MovedPartitionsOffsetNumber` sentinel.  Upstream raises SQLSTATE
   `0A000` "tuple to be locked was already moved to another partition due
   to concurrent update".

Without (1)–(3) the FK check is incoherent: it either reports a false-positive
"parent exists" when the parent was concurrently moved out of the referenced
partition, or it reports a false-negative `23503` when the parent was
concurrently updated in a non-key column.

## Solution

### 1. Wait-aware scan: `scanRelForFKMatch` + `scanTableForMatchFKWait`

`internal/executor/operators_fk.go` gains two functions, both confined to the
FK INSERT path:

- `scanRelForFKMatch(ctx, tbl, colNames, vals) → (found, pending, err)` is
  `scanRelForMatch` with one extra outcome: when the scan finds a matching
  visible row whose xmax is from an in-flight non-self transaction
  (`ctx.TxnMgr.IsXIDActive(xmax)`, not lock-only, not self), it returns
  `pending = &fkPendingRef{xid, rel, blk, slot}` and `found=false`.  A
  "clean" match (no in-flight non-self updater) still returns `found=true`
  immediately, matching `SELECT FOR KEY SHARE`'s prefer-clean-rows
  semantics.

- `scanTableForMatchFKWait(ctx, tbl, colNames, vals, pos)` wraps
  `scanRelForFKMatch` in a wait+retry loop (bounded at 8 iterations).  When
  a single scan reports `pending != nil`:

  ```
  WaitForXID(qctx, pending.xid)            // block on the updater
  ctx.Snap = SnapshotFor(ctx.Tx)            // refresh: picks up commit/abort
  if !ctx.Snap.HasAborted(pending.xid)      // committed path
      && epqChainCheckMovedPartition(...) {
      return errMovedToAnotherPartition(pos) // SQLSTATE 0A000
  }
  // retry: scan once more under refreshed snapshot
  ```

  The snapshot refresh runs **before** the sentinel check so that
  `HasAborted` correctly suppresses the spurious-0A000 path when the
  updater rolled back.  Sentinel bytes are stamped at UPDATE time (not
  commit time) and persist across ROLLBACK, so the abort guard is
  load-bearing.

`assertParentExists` routes through `scanTableForMatchFKWait`; existing
DELETE-side callers (`assertNoChildRows`, `fullTableFKCheck`) keep the
non-waiting `scanTableForMatch` because their semantics ("no child rows
exist") don't need to serialise against in-flight key-changing UPDATEs.

### 2. Chain-walking sentinel detector: `epqChainCheckMovedPartition`

`internal/executor/operators_storage.go` adds
`epqChainCheckMovedPartition(ctx, rel, blk, slot)`, a two-strategy
sentinel detector:

- **Strategy 1 (fast path): t_ctid chain walk.**  PG always updates the
  old tuple's `t_ctid` to point to the new version on UPDATE; goopg's
  HOT-style in-partition update path (`PageStampHotOldTuple`) does too.
  The walk follows `t_ctid` for up to 64 hops, checking
  `IsMovedToAnotherPartition` at each step.  Terminates on a self-CTID
  (latest version), invalid offset, or sentinel hit.

- **Strategy 2 (fallback scan).**  goopg's non-HOT UPDATE path
  (`PageSetHeapTupleXmax`-then-`writeHeapRow`, line 1534 / 1552 of
  `operators_storage.go`) does **not** update the old slot's `t_ctid` —
  it stays at the as-inserted `{InvalidBlockNumber, 0}`.  The chain walk
  thus terminates after one hop and misses the sentinel that
  `PageSetHeapTupleMovedPartition` stamped on a sibling slot (the
  intermediate version produced by `s1u3npc` and then sentinel-stamped
  by `s1u3pc`).  The fallback reads the recorded slot's xmax and scans
  the entire `pending.rel` for any tuple whose xmax matches AND whose
  `t_ctid` carries the sentinel — exactly the source-of-move tuple.

The fallback is filtered by xmax to avoid cross-transaction false
positives: a sentinel-stamped tuple from an earlier, unrelated committed
xact must not be reported as "the current updater moved this row".

### 3. Why no chain-link fix in the UPDATE path

Updating non-HOT UPDATEs to thread the old tuple's `t_ctid` forward is
correct in principle (it brings goopg closer to PG's tuple-versioning
shape) but it is a much larger surface than M0100-0005q's spec gate
requires.  Touching every `PageSetHeapTupleXmax` site for UPDATE (lines
1534, 1794, plus several lockmgr edges) risks regressing visibility and
EPQ paths that depend on the current behaviour.  The fallback scan
captures the same semantic outcome (sentinel reachable from the recorded
slot via the updater's xact identity) at FK-check cost only.  When the
broader chain-link work lands, the fallback can be deleted with no
behavioural change.

## Verified

`partition-key-update-1.spec` non-trigger range-parted leg (6 permutations:
`s1u3pc s2i s1c s2c`, `s1u3pc s2i s1r s2c`, `s1u3npc s1u3pc s2i s1c s2c`,
`s1u3npc s1u3pc s2i s1r s2c`, `s1u3npc s1u3pc s1u3pc s2i s1c s2c`,
`s1u3npc s1u3pc s1u3pc s2i s1r s2c`) now PASS end-to-end:

```
$ go test -count=1 -v -run TestPort_IsolationPartitionKeyUpdate1 ./internal/testport/
--- PASS: TestPort_IsolationPartitionKeyUpdate1 (4.28s)
```

Adjacent isolation tests unchanged: `InsertConflictDoNothing` PASS,
`InsertConflictDoUpdate` PASS, `LockCommittedUpdate` PASS.  Unit-test
suites green with `-race`:

```
go test -race ./internal/executor/ ./internal/storage/ ./internal/server/
              ./internal/mvcc/ ./internal/planner/ ./internal/parser/
              ./internal/analyzer/
```

## Regression pins

`internal/executor/operators_fk_wait_test.go`:

- `TestEpqChainCheckMovedPartitionDirectSentinel` — single-step chain,
  sentinel on the recorded slot.
- `TestEpqChainCheckMovedPartitionViaFallbackScan` — fallback scan finds
  a sibling sentinel slot stamped by the same xmax as the original.
- `TestEpqChainCheckMovedPartitionNoSentinel` — control: plain xmax stamp
  with no sentinel anywhere → `false`.
- `TestEpqChainCheckMovedPartitionFallbackIgnoresUnrelatedSentinel` — the
  fallback's xmax filter rejects sentinels stamped by a different xact.

End-to-end pin: `TestPort_IsolationPartitionKeyUpdate1` flips from
`SKIP (deferred)` to `PASS`.

## Out of scope

- Trigger-driven partition row movement (`footrg` leg of
  `partition-key-update-1.spec`) — closed under M0100-0005o + M0100-0005p.
- DELETE-side FK actions (`enforceFKOnDelete`, `assertNoChildRows`) — they
  don't need wait semantics for v0; the trigger SkipFK paths already
  exclude them.
- General non-HOT UPDATE chain-link maintenance — see §2.3 above.
