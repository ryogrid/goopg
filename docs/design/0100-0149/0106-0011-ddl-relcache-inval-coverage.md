# 0106-0011 — DDL coverage for `RelcacheInvalPending`

Status: accepted (2026-05-20)
Milestone: M0106-0011
Related: `docs/design/0106-0011-rollback-catalog-rows-clog-filter.md`,
`docs/design/0106-0011-crash-mid-tx-clog-implicit-abort.md`

## Context

M0106-0010 batched-31 wired the commit-time xact-marker hook
(`internal/initdb/open.go::startMVCCTxnMarkerLogger`) to consult the
process-wide `mvcc.Manager.relcacheInvalPending` flag. When set, the hook
emits a `RecordKindXactCommitInval` WAL record and unlinks +
regenerates both `global/pg_internal.init` and
`base/<dboid>/pg_internal.init` so the next backend (or a PG18 standby
reconnecting via `pg_basebackup`) reloads fresh relcache descriptors.

The flag is set inside `syncTableToCatalogHeap` (CREATE TABLE) and the
VACUUM nailed-catalog path. Other DDL paths that mutate the catalog —
DROP TABLE, ALTER TABLE ADD COLUMN, CREATE INDEX, DROP INDEX — did not
set it. A PG18 standby reconnecting after these operations would keep a
stale relcache: the dropped relation would still resolve, ADD COLUMN
would report a pre-ALTER attribute count, and CREATE INDEX would be
invisible on the parent table.

## Change

`internal/executor/operators_ddl.go`:

- `dropTableByRef` flags `SetRelcacheInvalPending()` after a successful
  `Catalog.DropTable`. Covers both `DROP TABLE` and the cascading
  partition / inheritance drops.
- `execAlterTableAddColumn` flags after a successful `Catalog.AddColumn`.
  The error paths (`already exists`, `XX000`) deliberately do not flag —
  a rejected ALTER must not emit a spurious commit-inval record.
- `syncIndexToCatalogHeap` (CREATE INDEX, ALTER TABLE ADD PRIMARY KEY)
  flags after the pg_class row is appended. The site mirrors the
  existing flag inside `syncTableToCatalogHeap` and is gated by the
  same `catalogHeapSyncAvailable(ctx)` predicate at the call site, so
  no-pool / virtual-pg_class fixtures stay flag-free.
- `execDropIndex` flags once at the end if at least one
  `Catalog.DropIndex` actually mutated state. The `IF EXISTS` miss
  path is left unflagged — without a mutation there is no inval to
  signal.

The new flag-set sites use the same `ctx.TxnMgr != nil` guard as the
existing CREATE TABLE site; the flag is process-wide (`atomic.Bool` on
the MVCC manager) and is drained by `TakeRelcacheInvalPending` at the
next commit, so spurious flagging in a transaction that later rolls
back is acceptable but wasteful — that is why the
`DROP INDEX IF EXISTS no_such_idx` path stays no-op.

## Why not move the existing flag-set out of the
   `catalogHeapSyncAvailable` branch?

CREATE TABLE / CREATE INDEX flag inside their `syncTable…` /
`syncIndex…` helpers, which only run when the catalog has been
bootstrapped as physical relations (full goopg server fixtures). The
in-memory `executor` unit-test fixture leaves pg_class / pg_attribute
virtual and never reaches the sync helpers; flagging would then emit
inval records against a relcache the test never built. The DROP /
ALTER / DROP-INDEX sites added here flag unconditionally because they
mutate the in-memory catalog regardless of whether the catalog heap
sync is wired — the on-disk catalog heap mutation gap is tracked
separately under the M0106-0011 follow-up "DDL must persist pg_class /
pg_attribute deletes / updates to heap".

## Tests

`internal/executor/operators_ddl_relcache_inval_test.go`:
`TestDDLPathsFlagRelcacheInvalPending` runs four subtests against
`newDDLFixture` — DROP TABLE, ALTER TABLE ADD COLUMN, DROP INDEX, and
the `DROP INDEX IF EXISTS` miss path — and asserts the flag is set (or
not, for the miss path). Each subtest drains the flag with
`TakeRelcacheInvalPending` between operations so assertions are
independent of any earlier DDL setup.

## Follow-up: persist catalog heap mutations (M0106-0011 follow-up a)

`internal/executor/operators_ddl.go` (loop 33):

- `deleteCatalogRowsForOID(ctx, dbOid, relOID, xmax)` stamps xmax on
  the on-disk pg_class / pg_attribute rows for the dropped relation so
  `loadUserTablesFromHeap` (which does an xmax==0 filter) does not
  re-load the dropped relation after restart.
- `catalogDBOids(ctx)` returns both `DefaultDBOid` (1) and the catalog's
  actual `DBOID()` (5, the "postgres" database), since
  `syncTableToCatalogHeap` writes to DefaultDBOid=1 and mirrors to
  DBOid=5, while `loadUserTablesFromHeap` reads from `cat.DBOID()=5`.
- `dropTableByRef` and `execDropIndex` call `MaterializeWriterXID` before
  stamping; DROP TABLE/INDEX never call `writeHeapRowReturningPG` so the
  transaction XID remains `InvalidTransactionID (0)` without explicit
  materialization.

`internal/storage/bufpool.go` (loop 33):

- `MarkDirtyForceFPI` emits a fresh full-page image of the post-stamp
  page, overriding any stale FPI from earlier in the same checkpoint
  epoch. Required for the DBOid=5 mirror case where the FPI captured
  during CREATE TABLE did not yet contain the row added by CREATE INDEX;
  without a fresh FPI, WAL replay would restore the pre-row state and
  the index slot would be invalid.

Root-cause chain for the table test failure (M0106-0011 follow-up a):

1. **Format mismatch**: `deleteCatalogRowsForOID` used only
   `DecodePGClassRow` (native format); `syncTableToCatalogHeap` writes
   PG18-physical rows. Fix: try both decoders (same pattern as
   `loadUserTablesFromHeap`).
2. **XID not materialized**: DROP TABLE/INDEX never assign a real XID
   (no `writeHeapRowReturningPG`), so `ctx.Tx.XID == 0` and the stamp
   was skipped. Fix: call `MaterializeWriterXID()` before the stamp.
3. **DBOid mismatch**: `loadUserTablesFromHeap` reads from `cat.DBOID()`
   (5), but `deleteCatalogRowsForOID` stamped only `DefaultDBOid` (1).
   Fix: `catalogDBOids()` helper stamps both.
4. **WAL replay FPI override**: using `MarkDirtyLogicalChange` (WAL
   heap-delete) caused the stale DBOid=5 FPI (captured pre-index-row) to
   be replayed first, making the index slot invalid. Fix:
   `MarkDirtyForceFPI` emits a post-stamp FPI that overrides the stale one.

`internal/executor/operators_tx.go` (loop 33):

- `rollbackDDLCreate` also calls `deleteCatalogRowsForOID` (for the
  ROLLBACK-of-CREATE path); updated to pass both DBOids via
  `catalogDBOids()`.

## Tests

`internal/executor/operators_ddl_relcache_inval_test.go`:
`TestDDLPathsFlagRelcacheInvalPending` runs four subtests against
`newDDLFixture` — DROP TABLE, ALTER TABLE ADD COLUMN, DROP INDEX, and
the `DROP INDEX IF EXISTS` miss path — and asserts the flag is set (or
not, for the miss path). Each subtest drains the flag with
`TakeRelcacheInvalPending` between operations so assertions are
independent of any earlier DDL setup.

`internal/initdb/transactional_ddl_test.go` (loop 33):
- `TestDroppedTableNotVisibleAfterRestart` — committed DROP TABLE must
  stamp xmax on pg_class / pg_attribute heap rows so the relation does
  not reappear after re-Open.
- `TestDroppedIndexNotVisibleAfterRestart` — committed DROP INDEX must
  stamp xmax on the index's pg_class row for the same reason.

## Verification

- `go test -count=1 -run TestDDLPathsFlagRelcacheInvalPending
  ./internal/executor/` — PASS 0.01s.
- `go test -count=1 -run "TestDropped.*NotVisibleAfterRestart|TestRolledback.*|TestCrashMid.*"
  ./internal/initdb/` — all PASS.
- `go test -count=1 ./internal/executor/ ./internal/storage/` — PASS.
- `go test -count=1 ./internal/mvcc/ ./internal/catalog/
  ./internal/server/` — PASS.
- `go test -count=1 ./internal/initdb/` — 15 pre-existing baseline
  failures unchanged (matches loop 31 baseline note).
- `go test -count=1 ./internal/wal/` — 2 pre-existing baseline
  failures unchanged.

## Open follow-ups (still under M0106-0011)

- DROP TABLE / ALTER TABLE ADD COLUMN do not yet write the
  corresponding pg_class / pg_attribute heap mutations (only the
  in-memory catalog is updated). The relcache-inval flag triggers the
  init-file refresh but the heap rows themselves remain stale on disk
  — a follow-up loop must extend `dropTableByRef` and
  `execAlterTableAddColumn` with `deleteHeapRowCanonical` /
  `writeHeapRowCanonical` against pg_class / pg_attribute and update
  the matching nailed-index entries.
- Checkpoint / shutdown init-file refresh: the current refresh
  triggers only on commit-inval. A long-lived process with no DDL
  but lots of catcache churn (e.g. VACUUM that runs after a session
  with relcache inval already drained) leaves stale init files until
  the next DDL or restart. Tracked as the next M0106-0011 slice.
