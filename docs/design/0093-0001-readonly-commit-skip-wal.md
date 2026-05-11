# Design 0093-0001 — Skip WAL commit-record emission for read-only transactions

**Status:** accepted (filed 2026-05-11; Design B chosen + implemented 2026-05-11).
**Milestone:** [M0093](../milestones/0093-read-only-commit-skip-wal-emission.md).

## Decision: Design B (lazy XID assignment, PostgreSQL-parity)

After review, the user chose **Design B** over Design A. The
"flag on transaction state" approach (A) was the simpler
mechanical change, but Design B matches PostgreSQL's semantics
exactly and avoids the audit-completeness risk of "did every
WAL-Append site call NoteWrote?" — under B, the absence of an
XID at finish() time IS the signal that no write happened, by
construction.

Implementation shape (landed in commits c00caa5 / 0a17eed /
54f53d4 / e383a61 / 40a1da0):

- New `mvcc.TxnHandle uint64`. Re-keys `m.active` from
  `map[TxnID]` to `map[TxnHandle]`.
- `Transaction` carries both Handle and XID. `Begin` returns
  Handle != 0, XID == InvalidTransactionID.
- New `mvcc.Manager.AssignXID(tx) (TxnID, error)` — idempotent
  lazy materialisation under existing wrap-around guards.
- `executor.(*Context).MaterializeWriterXID()` — the
  executor-side helper that write-path operators call before
  any xmin/xmax stamp. Updates `ctx.Tx.XID` in place and
  syncs the cached `BasicSession.tx.XID` via
  `OnTopLevelXIDAssigned`.
- `Manager.finish` invokes the `xactMarker` hook ONLY when
  `state.xid != InvalidTransactionID`. Read-only commits emit
  zero WAL bytes + zero fsync + zero clog write.
- Snapshot construction filters `m.active` values for
  materialised XIDs; read-only txns contribute nothing to
  InProgress.
- VACUUM correctness preserved by a new per-state
  `snapshotXmin` field that pins OldestXmin for long-running
  read-only REPEATABLE READ transactions (without this, a
  read-only RR snapshot would stop pinning the horizon and
  VACUUM could prematurely reclaim tuples it still needs —
  the M0093 R-B6 risk).

Write-site call sites materialising the XID (Commits 3, 4):

- `executor.writeHeapRowReturning` — covers INSERT, UPDATE-
  fallback insert, UPSERT insert, COPY FROM, and the TOAST
  chunk-table writes called recursively from
  `ToastLargeColumnsIfNeeded`.
- `executor.tryApplyHOTUpdate` (top of function) — before the
  encode → NewHeapTuple → page-Lock → isConcurrentlyUpdated
  sequence (R-B1 invariant).
- `executor.updateOp.Next` / `executor.deleteOp.Next` (top of
  function) — UPDATE / DELETE are unconditionally writes; the
  scan phase needs a real XID for the foreignLockOnly checks.
- `executor.lockRowsOp.stampLock` — SELECT FOR UPDATE / SHARE.

The previous "Recommendation: Design A" subsection below
documented the alternative; the rest of the doc describes the
Design B implementation (the A description is preserved for
historical context).

## Original problem statement

## Problem

`internal/mvcc/manager.go::Manager.finish` invokes the
installed `xactMarker` hook unconditionally on every
`Commit` / `Rollback`. The hook (wired in
`internal/initdb/open.go::Open` around line 575) appends an
`XactCommit` or `XactAbort` record to WAL and — on commit —
calls `walWriter.FlushUpTo(endLSN)` for a synchronous fsync.

For read-only `SELECT` transactions, none of this is
necessary. PostgreSQL's `RecordTransactionCommit` skips the
commit-record path entirely when no XID was assigned to the
transaction (the lazy-XID-allocation invariant), so a
read-only commit costs zero WAL bytes and zero fsync.

This design doc selects the implementation strategy for
M0093-0002 and enumerates every WAL-Append call site that
needs to participate in the chosen strategy.

## Designs considered

### Design A — "Transaction wrote WAL" flag

Carry a `wroteWAL bool` (or `int64` LSN of first write)
on the per-transaction state in `mvcc.Manager`. Every
WAL-Append call made on behalf of a transaction flips the
flag to true. `Manager.finish` invokes the `xactMarker`
hook only when the flag is set.

API additions (sketch):

```go
// in internal/mvcc/manager.go
type txnState struct {
    isolation IsolationLevel
    snap      Snapshot
    wroteWAL  bool   // M0093-0002: set by NoteWrote
}

// NoteWrote marks the given transaction as having emitted
// data WAL. M0093-0002: read-only transactions skip the
// commit-record + flush at finish() time.
func (m *Manager) NoteWrote(xid TransactionID) {
    m.mu.Lock()
    defer m.mu.Unlock()
    if s, ok := m.active[xid]; ok {
        s.wroteWAL = true
    }
}

func (m *Manager) finish(tx Transaction, kind XactMarker) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    state, ok := m.active[tx.XID]
    ...
    if state.wroteWAL && m.xactMarker != nil {
        if err := m.xactMarker(tx.XID, kind); err != nil { ... }
    }
    delete(m.active, tx.XID)
    return nil
}
```

Producers (every WAL-Append from a transactional context)
call `ctx.TxnMgr.NoteWrote(ctx.Tx.XID)` immediately before
or after their `walWriter.Append`. The flag is one-shot
(set-once), so the cost is two atomic-ish ops via the
manager's existing mutex.

**Pros:**
- Strictly correct by construction. Any actual WAL emission
  triggers a commit record; missing a `NoteWrote` would
  surface as a durability bug in the M0093-0002 unit tests
  (so the call-site enumeration is auditable).
- Snapshot / MVCC code is untouched — `tx.XID` continues to
  hold a real XID for every transaction.
- Smallest blast radius.

**Cons:**
- Read-only transactions still allocate an XID at Begin
  time. The XID counter advances per pgbench query
  (~29 k / 60 s @ 10 clients). Not a correctness issue
  but a missed long-run optimisation.
- `NoteWrote` must be wired at every WAL-Append site —
  miss one and a durability bug ships. The enumeration
  below is the audit boundary.

### Design B — Lazy XID assignment (PG-parity)

Mirror PG's lazy XID model: `Begin` does NOT allocate an
XID. The first write-path operation (HEAP_INSERT, etc.)
calls a "get or assign XID" routine that mutates the
transaction state in place. `Commit` checks `xid ==
InvalidXid` and skips everything when so.

**Pros:**
- PG-equivalent semantics. XID counter doesn't burn on
  read-only workloads.
- Snapshot's `xmin` / `xmax` continue to be valid
  (snapshots are taken at Begin and don't need our XID).

**Cons:**
- Snapshot construction in `mvcc.Manager.SnapshotFor`
  currently uses `tx.XID` widely. Changes to those paths
  carry a higher chance of subtle correctness regressions
  (M0090's concurrent-xmax detection is sensitive to XID
  semantics).
- Every code site that reads `tx.XID` needs to handle
  `InvalidXid` correctly — much wider audit than Design A.
- M0090's concurrent-UPDATE detection assumes both
  transactions have real XIDs at write time. The lazy
  path must allocate atomically before any tuple stamp
  to preserve that invariant.

### Original recommendation (superseded): Design A

The initial recommendation was Design A for smaller blast
radius. The user chose Design B for PG-parity semantics; see
the "Decision: Design B" section at the top of this doc for
the implementation actually landed.

## Implementation plan (Design A)

### Step 1 — Add `NoteWrote` API on `mvcc.Manager`

In `internal/mvcc/manager.go`:

- Add `wroteWAL bool` to the per-xact `state` struct.
- Add `func (m *Manager) NoteWrote(xid TransactionID)`.
- Gate `Manager.finish`'s `xactMarker` invocation on
  `state.wroteWAL`.

Unit tests:

```go
TestManagerCommit_ReadOnlyDoesNotInvokeHook
TestManagerCommit_AfterNoteWroteInvokesHook
TestManagerRollback_AfterNoteWroteInvokesHook
TestManagerRollback_ReadOnlyDoesNotInvokeHook
```

### Step 2 — Audit and wire every WAL-Append site

Per
`grep -rn "walWriter.Append\|wal.Writer.*Append\|WAL.Append" internal/`,
the producer sites that need `NoteWrote` calls are:

| package / file | purpose | notes |
|---|---|---|
| `internal/executor/heap_insert.go` (insertOp) | INSERT WAL record | call after Append |
| `internal/executor/heap_update.go` (updateOp) | UPDATE WAL record (regular + HOT) | call after Append |
| `internal/executor/heap_delete.go` (deleteOp) | DELETE WAL record | call after Append |
| `internal/executor/operators_storage.go` (insertOp.Next) | row-level INSERT path | call after Append |
| `internal/executor/lock_rows.go` (lockRowsOp) | tuple-lock xmax stamp | call after Append |
| `internal/access/btree/insert.go` | BTREE_INSERT | indirect — index inserts during INSERT/UPDATE; piggy-back on the parent operator's NoteWrote |
| `internal/access/btree/split.go` | BTREE_SPLIT | indirect — same as above |
| `internal/vacuum/...` | VACUUM records | non-client xact (autovacuum/checkpointer xact). Audit whether autovacuum-pid xacts go through `Manager.finish`; if so, they currently get a commit record we don't need either. |
| `internal/server/database_ddl.go` | CREATE/DROP DATABASE | always read-write |
| `internal/initdb/heap_prune.go` (opportunistic) | HEAP_PRUNE | runs from client backend during reads. **CRITICAL** — read transactions that prune MUST emit a commit. Design A handles this naturally: prune calls NoteWrote. |
| Catalog DDL handlers | CREATE/DROP INDEX, table | always read-write |

Each Append site receives:

```go
// existing
_, endLSN, err := walWriter.Append(payload)
// new (M0093-0002):
if err == nil && ctx != nil && ctx.TxnMgr != nil {
    ctx.TxnMgr.NoteWrote(ctx.Tx.XID)
}
```

Or, where the executor passes a context object, factor a
helper:

```go
func appendWALForTxn(ctx *executor.Context, payload []byte) (lsn LSN, err error) {
    _, endLSN, err := ctx.WAL.Append(payload)
    if err == nil {
        ctx.TxnMgr.NoteWrote(ctx.Tx.XID)
    }
    return endLSN, err
}
```

The exact API shape will be picked during implementation;
the audit-completeness invariant is what matters.

### Step 3 — `Manager.finish` gate

```go
if state.wroteWAL && m.xactMarker != nil {
    if err := m.xactMarker(tx.XID, kind); err != nil { ... }
}
```

Both commit AND abort cases gate on the same flag. A
read-only Rollback also produces no WAL.

### Step 4 — clog update gate

The current `xactMarker` hook also calls
`clog.SetCommitted(xid)` / `clog.SetAborted(xid)`. With
Design A those calls move inside the hook (which is no
longer invoked for read-only txns), so the clog stays
untouched for read-only XIDs.

**Audit:** does any code path read clog for an XID that
was assigned but never marked? If so it would see
`InProgress` indefinitely. Verify:
- mvcc.Snapshot construction uses the in-memory active
  set, NOT clog. ✓
- HOT chain traversal: `mvcc.TupleVisible` consults
  Snapshot, not clog directly. ✓
- Crash recovery: clog `InProgress` after crash means the
  XID was active at crash time and treated as aborted on
  replay. For a read-only XID this is correct (its
  effects on tuples — none — are correctly invisible).
- pg_stat_activity / activity registry: doesn't read clog.

The clog-skip is safe and intentional.

### Step 5 — Tests

- `TestReadOnlySelect_NoWALEmitted` — single SELECT,
  assert WAL LSN unchanged across the txn.
- `TestReadWriteInsert_EmitsCommitRecord` — INSERT, assert
  WAL LSN advances and the commit record is on disk.
- `TestMixedTxn_FirstWriteFlipsFlag` — multi-statement
  txn where statement 1 is SELECT and statement 2 is
  INSERT; assert commit record emitted.
- `TestRollback_ReadOnlyNoAbortRecord` — read-only ROLLBACK
  emits no abort record.
- `TestRollback_AfterWriteEmitsAbortRecord` — write then
  rollback emits an abort record.
- `TestOpportunisticPrune_FromSelectFlipsFlag` — a SELECT
  that triggers HEAP_PRUNE WAL emission must result in a
  commit record (because durability of the prune depends
  on it).
- Crash-recovery integration test: run pgbench-S for a
  few seconds, kill -9 the server, restart, verify
  recovery succeeds with no missing-WAL or torn-WAL
  errors (the absence of read-only commit records means
  fewer WAL bytes to replay, which should be a no-op for
  recovery).

## Acceptance

Per the milestone DoD, but the design-level invariants are:

1. After M0093-0002 lands, every test that exercised
   write paths previously continues to pass — the gate
   on `wroteWAL` is a strict additive enable.
2. New tests above all pass.
3. The 19,684 walwriter flushes / 60 s observed during
   pgbench-S goes to ~0 (modulo background WAL writer's
   periodic wal_writer_delay flush of any pending
   data — which is a no-op when there's nothing in the
   in-memory WAL buffer).

## Risks (mirrored from milestone)

- **R1: Missed NoteWrote → durability bug.** Mitigation:
  the audit table above is the gate; every Append site
  appears, and Step 5's read-write tests catch any miss
  in practice.
- **R2: Logical decoding consumer expects commit record
  for every txn.** Mitigation: read-only txns never
  enqueue change records, so the reorder buffer never
  has anything to flush for them. Verified by inspection;
  add a regression test that exercises a subscription
  with concurrent read-only and read-write traffic.
- **R3: pg_stat_activity expects xact_start /
  xact_finish parity.** Mitigation: that's tracked
  independently of clog; verify no code reads clog for
  pg_stat_activity entries.
- **R4: clog InProgress for a read-only XID.**
  Verified safe above.

## References

- `internal/mvcc/manager.go::Manager.finish` — the unconditional hook invocation site.
- `internal/initdb/open.go::Open` (around line 575) — the
  installed `xactMarker` hook.
- PG `src/backend/access/transam/xact.c::RecordTransactionCommit` —
  the upstream lazy-XID path.
- `bench/pgbench-compare/results/20260511_goopg_select-only_m0092_followup_summary.md` —
  the measurement that surfaced the bottleneck.
