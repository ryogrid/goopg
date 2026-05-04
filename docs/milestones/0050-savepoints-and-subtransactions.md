# Milestone 0050 — Savepoints and subtransactions

**Status:** planned
**Depends on:** root-0007 (MVCC + snapshot manager — subxact XIDs participate in visibility), Milestone 0030 (catalog persistence — subxact assignment recorded in pg_subtrans equivalent), Milestone 0019 (autovacuum — subxact XIDs interact with the freezing path planned in M0046-0005).
**Drives:** Implement nested-transaction semantics. Specifically: SQL `SAVEPOINT name` / `ROLLBACK TO SAVEPOINT name` / `RELEASE SAVEPOINT name` / `ROLLBACK [TO ...]`; the underlying subtransaction stack on `TransactionState`; subxact XID allocation; subxact-aware snapshot visibility. This is also a hard prerequisite for PL/pgSQL `BEGIN ... EXCEPTION WHEN ... END` blocks.

## 1. Context

`docs/reference/ref-022-session-management.md` lists savepoints / subtransactions as a Tier-1 gap. The user-facing surface is small (four SQL verbs) but the runtime impact is broad:

- Every commit / abort path has to know about the subxact stack: an inner `RELEASE` is a *no-op* against on-disk state (changes promote up to the parent), an inner `ROLLBACK TO SAVEPOINT` undoes only the inner range.
- The snapshot manager's `XidInProgress` test must understand that an inner subxact is in-progress iff its top-level xact is in-progress *and* it has not been individually rolled back.
- The WAL must record subxact assignments (`XLOG_XACT_ASSIGNMENT` upstream) so recovery can rebuild the subxact-to-parent mapping.
- PL/pgSQL `EXCEPTION` blocks (M0051 / planned) are implemented internally as savepoints; without this milestone PL/pgSQL exception handling cannot land.

This is a foundational feature, not a perf optimisation. Once it is in place, REF-021's "no savepoints" entry, the PL/pgSQL exception-block gap, and a class of psql-level user errors (`ERROR: ... ROLLBACK in transaction block`) all clear simultaneously.

## 2. Required Design Docs

1. `docs/design/0050-0001-subxact-stack-and-state-machine.md` — extend `TransactionState` to carry a stack of `SubTransactionState` (id, parent, snapshot, status). State transitions for `SAVEPOINT` (push), `RELEASE` (collapse-up), `ROLLBACK TO SAVEPOINT` (rewind + push fresh).
2. `docs/design/0050-0002-subxact-xid-and-visibility.md` — subxact XID allocation (separate counter or sub-range of the top-level XID). Snapshot manager's `XidInProgress` checks the subxact-to-parent map; pg_subtrans-equivalent in-memory cache. Visibility test extended: a row written by an aborted subxact is invisible even when the top-level xact commits.
3. `docs/design/0050-0003-subxact-wal-and-recovery.md` — new WAL records: `XLogXactAssignment`, `XLogXactRollbackTo` (subxact-only). Recovery rebuilds subxact-to-parent via assignment records and applies abort to the right range. Idempotent w.r.t. repeated replay.
4. `docs/design/0050-0004-savepoint-sql-surface-and-error-recovery.md` — parser & executor for `SAVEPOINT` / `RELEASE` / `ROLLBACK TO SAVEPOINT`. Implicit-savepoint behaviour for psql (`\set ON_ERROR_ROLLBACK`-friendly): when a statement errors inside a subxact-wrapped block the session stays usable. Error path enrichment so a botched DML inside a savepoint aborts only that savepoint, not the outer xact.

`0001`+`0002` land first as a connected pair; `0003` rides on a stable in-memory model; `0004` is the visible SQL surface and lands last.

## 3. Definition of Done

### 3.1 Subxact stack
- `TransactionState` owns a slice-backed `SubTransactionState` stack with O(1) push / collapse / rewind.
- `SAVEPOINT` allocates a fresh subxact id, pushes a stack entry, and clones the necessary parts of the snapshot.
- `RELEASE SAVEPOINT name` collapses entries `[name..top]` into the parent (writes/locks/visibility metadata promoted up).
- `ROLLBACK TO SAVEPOINT name` discards entries `[name..top]`, marks them aborted, then pushes a fresh entry with the same name (mirrors upstream).

### 3.2 Subxact visibility
- Snapshot manager's `XidInProgress(xid)` resolves `xid → top-level xid` via the in-memory subtrans cache.
- A row written by an aborted subxact is invisible to all other snapshots — even after the top-level commit.
- Regression test: `BEGIN; INSERT a; SAVEPOINT s; INSERT b; ROLLBACK TO s; COMMIT;` — only `a` survives; concurrent snapshot agrees.

### 3.3 WAL & recovery
- New WAL records carry the subxact assignment and rollback-to events.
- After crash + replay, the subxact-to-parent map is reconstructed and the visibility tests above still hold.
- Regression test: `restart_after_retention_test.go`-style harness with a subxact pattern survives kill+restart.

### 3.4 SQL surface
- `SAVEPOINT s`, `RELEASE SAVEPOINT s`, `ROLLBACK TO SAVEPOINT s`, and the bare `ROLLBACK TO s` shorthand all parse and execute.
- Outside an explicit transaction, savepoint commands return SQLSTATE 25P01 (`no_active_sql_transaction`).
- psql `\set ON_ERROR_ROLLBACK on` turns each statement into an implicit savepoint and continues after errors.

### 3.5 No regression
- `make ralph-state-guard` green every loop.
- `TestRunTPCHQueriesAgainstSyntheticData` 22/22 unchanged.
- All existing transaction / MVCC / WAL recovery tests still green.

## 4. Out of scope

- Two-phase commit (`PREPARE TRANSACTION` / `COMMIT PREPARED`). Distinct feature; tracked separately.
- Cross-server distributed savepoints.
- PL/pgSQL `EXCEPTION` block runtime — that consumes savepoints and lands in M0051.
- Persistent pg_subtrans on disk; in-memory cache is sufficient for v0 (upstream's pg_subtrans SLRU is an optimisation).

## 5. Reference

- `postgres/src/backend/access/transam/xact.c` — `BeginInternalSubTransaction`, `RollbackToSavepoint`, `ReleaseSavepoint`.
- `postgres/src/backend/access/transam/subtrans.c` — pg_subtrans SLRU (parent map).
- `postgres/src/backend/utils/time/snapmgr.c` — snapshot fields touched by subxact.
- `docs/reference/ref-022-session-management.md` — gap inventory.
- `docs/design/root-0007-mvcc-and-snapshots.md`, `root-0008-wal-and-recovery.md`.
