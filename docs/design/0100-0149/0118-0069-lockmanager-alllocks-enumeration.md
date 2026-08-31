# 0118-0069 — `LockManager.AllLocks()` enumeration (M0118-0008 `partition-drop-index-locking` enabler)

**Status:** accepted
**Type:** Foundation enabler, **NOT a promotion** — zero behavioural change.
**Spec target:** `partition-drop-index-locking` (M0118-0008 hard tail).

## Problem

`partition-drop-index-locking`'s keystone step `s3getlocks` reads the
`pg_locks` view, joining it to `pg_class` (by `relation`) and
`pg_stat_activity` (by `pid`), to assert that `DROP INDEX` takes
`AccessExclusiveLock` top-down on the partition tree (some granted, one
waiting) while a concurrent `SELECT` holds `AccessShareLock` on the leaf
table and its indexes.

Today goopg's `pg_locks` (`internal/executor/relation_locks.go`,
`catalog.RelationLockRowsFunc`) surfaces **only** the explicit `LOCK TABLE`
registry `globalRelLockMgr` — a display-only slice with `pid` and `granted`
hard-coded to `0`/`t`. The real heavyweight locks that
`DROP INDEX`/`SELECT`/`acquireDDLLockTxn` take live in the executor's
`tableLockMgr` (`internal/lockmgr`, the `lockmgr.LockManager`), which has no
read-side enumeration API. So `s3getlocks` returns `(0 rows)` — the spec's
current first divergence after enablers 0118-0067 (`DROP INDEX` locks the
partition tree) and 0118-0068 (`CREATE INDEX` recurses the partition tree).

Upstream backs `pg_locks` with `GetLockStatusData()`
(`postgres/src/backend/storage/lmgr/lock.c`), which walks the lock table
and emits one `LockInstanceData` per held/awaited `(backend, mode)`.

## What landed

`LockManager.AllLocks() []LockHolding` — the read-side analog of
`GetLockStatusData()`. It walks `lm.states` under the manager lock and
returns one `LockHolding{Tag, Backend, Mode, Granted}` per `(backend, mode)`:

- **Holders** → `Granted=true`. A backend's holder `Mask` is expanded into
  one entry per set mode bit, so a backend holding several self-compatible
  modes on a tag (e.g. the nested-statement `RowExclusive` + `AccessShare`
  case) yields several rows — matching upstream's one-`LOCK`-per-mode
  accounting in `pg_locks`.
- **Waiters** → `Granted=false`, one per queued `*Waiter`.

Tuple-level tags (`Block`/`Offset` non-zero) are included; a caller that
wants only relation locks filters on `Tag.Block==0 && Tag.Offset==0`. The
method is read-only (takes the lock, copies out, holds nothing); ordering is
unspecified (map iteration) so callers sort.

### Why this is the bounded, zero-risk slice

`AllLocks()` is a **pure addition**: no existing call site references it, so
behaviour is byte-identical and there is no hot-path or regression risk. It
is the single primitive named as "blocker 1" for this spec. Landing it
alone (rather than the full live `pg_locks` bridge) is deliberate — the
bridge is genuinely multi-subsystem and **cannot promote the spec by
itself** (see below), so shipping a tested, unwired primitive keeps the tree
clean and the spec's observable behaviour unchanged while making the next
loop mechanical.

## Tests

`internal/lockmgr/alllocks_test.go`:

- `TestAllLocksEmpty` — fresh manager enumerates nothing.
- `TestAllLocksGrantedHolders` — holders across multiple tags/backends
  surface as `Granted=true`, one row each.
- `TestAllLocksMultiModeMaskExpanded` — a backend holding two
  self-compatible modes on one tag yields one entry per mode bit.
- `TestAllLocksWaiterIsNotGranted` — a backend parked in the wait queue
  surfaces as `Granted=false` alongside the granted holder.
- `TestAllLocksIncludesTupleTags` — tuple-level tags are enumerated;
  relation-only callers filter on `Block==0 && Offset==0`.

Gates: `go test -race ./internal/lockmgr/` PASS; `go build ./...` clean;
pgbench TPC-B smoke = pre-commit hook.

## Remaining work to promote `partition-drop-index-locking` (resume points)

`AllLocks()` is necessary but **not sufficient**. Full promotion needs:

1. **Live `pg_locks`→`tableLockMgr` bridge** (the next loop). Wire
   `AllLocks()` into `RelationLockRowsFunc` ALONGSIDE / replacing
   `globalRelLockMgr`, emitting one row per `LockHolding` with:
   `locktype=relation`, `relation=Tag.Rel`, `mode=Mode.String()` (already
   the pg_locks spelling, e.g. `AccessExclusiveLock`), `granted=Granted`,
   and `pid=` the holder's backend pid. Requires a
   **`BackendID`→pid registry** (map `TxnLockBackendID` → the session pid
   `pg_stat_activity` reports) so the `l.pid = s.pid` join resolves.
   **Dedup hazard:** `LOCK TABLE` records in BOTH `globalRelLockMgr` AND
   `tableLockMgr` (`operators_ddl.go`), so a naive add double-counts s1's
   `AccessShare`; the bridge must unify on one source. Regression surface is
   small — only two ported specs read `pg_locks` and
   `insert-conflict-specconflict` filters to
   `locktype IN ('spectoken','transactionid')`, so the relation-lock bridge
   is observable **only** by `partition-drop-index-locking` itself.

2. **`SELECT` takes `AccessShare` on the leaf's indexes**, not just the
   table — the spec's `s1select` rows include
   `…_subpart_child_id_idx{,1}`. `acquireScanReadLockTxn` (0118-0018) locks
   the table only.

3. **Transactional-DDL cross-session catalog visibility** (milestone-sized).
   The *second* `s3getlocks` (after `s2drop` completes but BEFORE `s2commit`)
   must still show the dropped index's `pg_class` row — the join
   `l.relation = c.oid` requires the not-yet-committed `DROP INDEX` to be
   invisible to s3's snapshot. goopg removes the index from the shared
   in-memory catalog synchronously when the drop completes, so s3 would lose
   the row. This is the same transactional-DDL visibility subsystem
   `alter-table-4` / `partition-concurrent-attach` need.

The deferral ledger carries the full map; `partition-drop-index-locking`
stays `defer`.
