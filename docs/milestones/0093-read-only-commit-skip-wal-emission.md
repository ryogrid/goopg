# Milestone 0093 — Skip WAL emission for read-only commits (PG-parity)

**Status:** accepted (2026-05-11 — implementation via Design B
landed; pgbench-S TPS jumped 317 → 2,740 (8.6×) at -c 10,
walwriter flush count dropped from ~19,600 / 60 s to 0,
M0091's ≥ 1,000 TPS bar met with margin; crash recovery clean;
no read-write regression).
**Depends on:** M0091 (TPS regression recovery), M0092
(broadly-distributed allocation cuts) — M0093 attacks the
bottleneck that M0092's measurement uncovered.
**Drives:** unlock pgbench select-only TPS toward M0091's
≥ 1,000 bar by eliminating the per-query commit-record WAL
fsync that read-only transactions don't need.

## Context

The M0092 follow-up re-measurement
(`bench/pgbench-compare/results/20260511_goopg_select-only_m0092_followup_summary.md`)
identified that goopg's pgbench-S `-c 10` workload runs at
**0.17 % CPU utilisation on the server** with each client
backend spending ~31 ms blocked per query. The server is
essentially idle; the per-query allocation cuts from M0092-
0004 / -0005 / -0006 / -0007 landed but don't move TPS
because CPU isn't the constraint.

The 60-s server log captured during the run contains
**19,684 `walwriter flush` lines** with the WAL LSN advancing
by ~13 bytes per flush. Cross-referencing the transaction
count (~28,953 transactions / 60 s) shows the flush rate
matches the transaction rate.

User-raised diagnostic (2026-05-11): does goopg emit WAL for
read-only `SELECT` transactions? PostgreSQL's design only
writes a commit record when the transaction has been
assigned an XID — read-only transactions skip
`RecordTransactionCommit` entirely (the `xid == InvalidXid`
fast path). goopg should follow the same design.

## Root cause (confirmed by code audit)

`internal/mvcc/manager.go::Manager.finish`:

```go
func (m *Manager) finish(tx Transaction, kind XactMarker) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    state, ok := m.active[tx.XID]
    ...
    if m.xactMarker != nil {
        if err := m.xactMarker(tx.XID, kind); err != nil { ... }
    }
    delete(m.active, tx.XID)
    return nil
}
```

The `xactMarker` hook is invoked **unconditionally** for
every `Commit` and `Rollback`, regardless of whether the
transaction actually wrote any data.

The hook itself is installed in
`internal/initdb/open.go::Open` (around line 575) and is
wired to:

```go
txnMgr.SetXactMarkerLogger(func(xid storage.TransactionID, kind mvcc.XactMarker) error {
    payload := wal.EncodeXactCommit(xid)    // or XactAbort
    _, endLSN, err := walWriter.Append(payload)
    if kind == mvcc.XactCommit {
        if werr := walWriter.FlushUpTo(endLSN); werr != nil { ... }  // sync fsync
    }
    ... clog ...
})
```

So every implicit `BEGIN ... SELECT ... COMMIT` from a
pgbench-S client backend:

1. Allocates an XID (unconditional, even for read-only).
2. Appends a ~13-byte `XactCommit` record to WAL.
3. Calls `walWriter.FlushUpTo(endLSN)` — synchronous fsync.
4. Persists to clog.

Steps 2-4 are unnecessary for read-only transactions and are
the root cause of the fsync-serialised bottleneck.

## PostgreSQL behaviour (reference)

Upstream PG's `RecordTransactionCommit` (`src/backend/access/transam/xact.c`):

```c
void
RecordTransactionCommit(void)
{
    TransactionId xid = GetTopTransactionIdIfAny();
    bool        markXidCommitted = TransactionIdIsValid(xid);
    ...
    if (!markXidCommitted)
    {
        /*
         * If we didn't have any transactional data to write,
         * we reach here ... There's no need to write a commit
         * record.  But if synchronous replication is on, we may
         * still need to wait for ...
         */
        if (wrote_xlog && markXidCommitted)
            XactLogCommitRecord(...);
        ...
    }
    else
    {
        XactLogCommitRecord(...);
        ...
        XLogFlush(XactLastRecEnd);
        ...
    }
}
```

The PG invariant: **a commit record is written only when an
XID was assigned**, and an XID is assigned **lazily**
(`GetCurrentTransactionId` → `AssignTransactionId`) the first
time the transaction does something that needs one — INSERT /
UPDATE / DELETE, locking a tuple, etc. Read-only `SELECT`
transactions never call `GetCurrentTransactionId` and never
get an XID.

There's a separate hint-bit / VM / FSM consideration that
DOES write WAL during reads (HOT pruning, all-visible bit
setting, FPW on first page modification after checkpoint),
but those are bounded by checkpoint boundaries — not per
query.

## Goal

Bring goopg's commit-record emission into PG parity:

1. Read-only transactions (no heap / index / catalog write,
   no row lock) do NOT emit `XactCommit` WAL records.
2. Read-only transactions do NOT call `walWriter.FlushUpTo`.
3. Read-only transactions do NOT write to clog.
4. Read-write transactions retain today's behaviour exactly.
5. After-the-fact hint-bit / VM / FSM updates remain
   WAL-logged when on (M0080 already handles these via
   their own record kinds — out of M0093 scope).

## Approach

Two compatible designs (pick one per the design doc):

### Design A — Track "transaction wrote WAL" on the transaction state

Carry a `wroteWAL bool` (or equivalent) on the
`mvcc.Transaction` / per-xact state. Every WAL Append call
made on behalf of a transaction (HEAP_INSERT,
HEAP_UPDATE, HEAP_DELETE, HEAP2_MULTI_INSERT, BTREE_INSERT,
HEAP_LOCK, etc.) flips the flag to `true`. At
`Manager.finish` time, only invoke the `xactMarker` hook
when `wroteWAL` is `true`.

Pros:
- Strictly correct: any actual WAL emission triggers a
  commit record. No corner cases.
- Minimal API surface — the flag flips inside existing
  WAL-Append wrappers.

Cons:
- Needs plumbing through every WAL-Append site so the
  transaction can be located.

### Design B — Lazy XID assignment (PG-equivalent)

Don't allocate an XID at `Begin` time. Allocate lazily on
first write. Reads use a virtual XID (or a placeholder).
At `Commit` time, check `xid == InvalidXid` and skip the
commit record + flush + clog entirely when so.

Pros:
- Matches PG semantics most directly. Side effects:
  pg_stat_activity reports backend.xact_start without
  blocking on XID allocation; large idle pools don't burn
  XID space.

Cons:
- Bigger change. Snapshot / MVCC code already uses
  `tx.XID` widely; needs a sentinel value or a
  per-transaction "have I been assigned yet?" check at
  every read of `tx.XID`.
- M0090 concurrent-xmax detection relies on having a real
  XID at write time — fine if we lazy-assign at the
  point of the first write, but we must ensure the lazy
  path can't race.

**Initial recommendation: Design A.** Smaller blast
radius, achieves the same TPS outcome for pgbench-S, and
leaves the lazy-XID work as a future M0094 if needed.

## Required design docs

- `docs/design/0093-0001-readonly-commit-skip-wal.md`
  (authoritative for the chosen design's implementation;
  must enumerate every WAL-Append call site and the
  transaction-wiring approach).
- `docs/design/0093-0002-pgbench-remeasurement-target.md`
  (re-measurement methodology, expected TPS lift,
  acceptance threshold).

## Tasks

- [x] **M0093-0001** — Design doc 0093-0001 (Design A vs B,
      chosen approach). Decision: **Design B (lazy XID
      assignment, PG-parity)**. Design doc updated to mark
      Design B chosen and status accepted (2026-05-11,
      commit pending).
- [x] **M0093-0002** — Implementation. Landed via 5 commits
      on 2026-05-11:
      - `c00caa5` — mvcc TxnHandle + lazy AssignXID +
        OldestXmin snapshotXmin fix (R-B6).
      - `0a17eed` — executor.Context.MaterializeWriterXID
        helper + BasicSession.OnTopLevelXIDAssigned.
      - `54f53d4` — wire MaterializeWriterXID at INSERT /
        TOAST (writeHeapRowReturning).
      - `e383a61` — wire at UPDATE / HOT / DELETE /
        LockRows (R-B1: before isConcurrentlyUpdated).
      - `40a1da0` — test fixups for sites that AssignXID
        was eagerly assumed.
      Full unit + testport suite passes (only the pre-
      existing replcluster failure remains, unrelated).
- [x] **M0093-0003** — Re-measurement landed 2026-05-11:
      pgbench-S `-c 10 -j 10 -T 180` reaches **2705-2742 TPS
      across 3 runs** (median 2,740). Walwriter flush rate
      over a 90 s window: **0** (down from ~19,600 / 60 s).
      M0091's ≥ 1,000 TPS bar met with 2.74× margin.
      Acceptance criteria all met: 8.6× of re-measured 317
      TPS baseline; zero failed transactions; no replication-
      test regression (TestReplicationEndToEnd was pre-
      existing-failing on this branch). Full report:
      `bench/pgbench-compare/results/20260511_goopg_select-only_m0093_summary.md`.
- [x] **M0093-0004** — pgbench standard / simple-update at
      `-c 10 -T 60` post-M0093:
      - standard: 58.43 TPS, 2 failed (0.057 % — M0090's
        documented concurrent-UPDATE 40001 path, not a
        regression).
      - simple-update: 109.55 TPS, 0 failed.
      Crash-recovery test (kill -9 mid-pgbench-S, restart):
      row count preserved (10M / 10M). Read-write paths
      still emit WAL XactCommit + fsync; only read-only
      commits skip them.
- [x] **M0093-0005 — TPC-H Q1-Q22 regression sweep**
      (2026-05-11): fresh setup with M0093 binary
      (`setup_goopg.sh --reset` → `build_schema_goopg.sh`
      SF=1 → HammerDB ANALYZE → `tpch-runner` Q1..Q22).
      **22/22 OK** within the 600-s per-query budget, zero
      errors, zero timeouts. Q5 18.63s rows=5 (no
      regression vs M0077's 26s baseline). Q9 175 rows
      (matches canonical). Q12/Q13 non-zero
      (pre-commit-gate parity). Total elapsed ~1,248 s.
      Full report:
      `analysis/tpch/m0093-q1-q22-regression-sweep.md`.

## Risk

- **R1.** If `wroteWAL` is missed on some WAL-Append site,
  a transaction that actually changed state could finish
  without a commit record — a durability bug. Mitigation:
  enumerate every Append call site in the design doc
  before implementation, plus tests that exercise INSERT,
  UPDATE, DELETE, BTREE_INSERT, HEAP_LOCK, HEAP_PRUNE,
  and assert each ends with a commit record.
- **R2.** Logical decoding (`internal/wal/classifier.go`)
  may rely on seeing a commit record for every transaction
  to flush its reorder-buffer state. Audit: a read-only
  transaction never enters the reorder buffer (no INSERT /
  UPDATE / DELETE records to enqueue), so dropping its
  commit record should be safe. Verify with the M0008
  logical-decoding tests.
- **R3.** pg_stat_activity / xact_start tracking might
  expect every transaction to commit-mark. Audit the
  activity package's `XactCommitted` / `XactRolledBack`
  call sites and update if needed.
- **R4.** Read-only transactions still allocate XIDs.
  Even with M0093-0002, the XID counter advances per
  query. For Design A this is acceptable; Design B
  removes it. Decide in 0093-0001.

## Definition of Done

- Read-only pgbench-S transactions emit ZERO WAL records.
- pgbench-S TPS at scale 100 -c 10 -T 180: ≥ 1,000 TPS
  (M0091's acceptance bar) AND zero failed transactions.
- pgbench standard / simple-update TPS unchanged within
  noise vs the M0092 baseline (zero correctness regression).
- All existing unit + testport + replication tests pass
  (replcluster pre-existing failure on this branch is
  unrelated).
- Crash-recovery test: a server crash mid-pgbench-S run
  recovers cleanly (no missing commit records → all
  visible transactions stay visible after replay).
- Design docs 0093-0001 and 0093-0002 marked accepted.

## Out-of-scope (defer to M0094+)

- Lazy XID assignment (design B). File as M0094 if needed.
- Plan cache / extended-protocol prepared-statement plan
  reuse (M0092-0008 deferral).
- WAL group commit (multi-txn fsync batching) — separate
  performance lever that complements but doesn't replace
  read-only commit skip.
