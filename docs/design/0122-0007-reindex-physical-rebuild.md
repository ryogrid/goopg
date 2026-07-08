# 0122-0007 — REINDEX physical rebuild (plain `REINDEX INDEX` / `TABLE` / `SCHEMA`)

- **Milestone:** M0122-0007 (DDL / admin commands / ctl / GUC config)
- **Status:** accepted (partial — see Deferral)
- **Related:** [0118-0029-reindex-concurrently-wait-for-lockers.md](0118-0029-reindex-concurrently-wait-for-lockers.md),
  [0118-0030-reindex-schema-relation-locking.md](0118-0030-reindex-schema-relation-locking.md),
  [root-0009-btree.md](root-0009-btree.md) (bulk-build mechanism this slice reuses)

## Problem

`REINDEX {INDEX,TABLE,SCHEMA}` (`internal/executor/operators_reindex.go`) has
always validated its target and, for the `CONCURRENTLY`/`SCHEMA` forms,
reproduced PostgreSQL's observable *locking* behaviour (M0118-0008's
reindex-concurrently / reindex-schema isolation specs) — but the physical
rebuild itself was an unconditional no-op: the on-disk btree pages were never
touched. A REINDEX issued to repair a corrupted or stale index (its actual
real-world purpose) silently did nothing while reporting success.

## Fix

Plain (non-`CONCURRENTLY`) `REINDEX INDEX name`, `REINDEX TABLE name`, and
`REINDEX SCHEMA name` now physically rebuild every btree index involved,
reusing CREATE INDEX's own bulk-build path rather than inventing a new one:

- `reindexOp.rebuildIndex(idx, pos)` resolves `idx`'s key columns (mirroring
  `createBTreeIndex`'s column-lookup loop) and its partial-index predicate (via
  `planner.ResolveIndexPredicate(idx.Predicate, tbl)`, the same resolver CREATE
  INDEX uses), then constructs a bare `&ddlOp{ctx: o.ctx}` to call
  `bulkBuildBTree`/`bulkBuildBTreeWithPredicate` — the exact function CREATE
  INDEX calls to populate a brand-new index from a heap scan.
  `btree.BulkCreateWithOptions` (`internal/access/btree/bulkload.go`) already
  `TruncateRelation` + `InvalidateRel`s the target before repacking it (originally
  written to give a **crash-recovered** bulk build a clean slate), which is
  exactly the "drop and recreate from scratch" semantics `REINDEX` needs
  (`reindex.sgml`: "REINDEX is similar to a drop and recreate of the index").
  No new index-building code was written — this slice is entirely about
  reaching the existing mechanism from a *second* caller (an already-live
  index, not a fresh one) with the right locking around it.
- Non-btree access methods (gist/spgist/gin/brin) are catalog-only in goopg —
  CREATE INDEX itself never builds physical storage for them (see
  `execCreateIndex`'s catalog-only branch) — so `rebuildIndex` is a no-op for
  `idx.Method != "btree"`, matching that pre-existing scope boundary exactly.
- `reindexOp.rebuildTableIndexes(tbl, pos)` calls `rebuildIndex` for every
  `catalog.IndexesOnTable(tbl)` entry; used by `REINDEX TABLE` and (2026-07-09
  follow-up) `REINDEX SCHEMA`'s per-table loop.
- **`REINDEX SCHEMA` follow-up (2026-07-09):** the `SCHEMA` branch's
  per-relation loop now calls `rebuildTableIndexes` for each non-concurrent
  table instead of only acquiring/releasing a transient `ShareLock`
  (`schemaTableNamesByOID` — renamed from `schemaRelsByOID` — now returns
  qualified names, re-resolved via `LookupTable` immediately before use
  instead of once up front). This matters because `reindex-schema.spec`
  exercises a table being concurrently dropped *while REINDEX SCHEMA is still
  waiting on an earlier table's lock*: capturing `Table`/`RelFileNode` once up
  front (as the old `schemaRelsByOID` did) would hand a stale pointer to a
  since-dropped relation to the rebuild step; re-resolving by name and
  silently skipping a lookup miss reproduces upstream's tolerate-concurrent-
  drop behaviour instead.

### Locking: a real hold, not the file's existing wait-only pattern

Every other lock helper in `operators_reindex.go`
(`acquireRelLockMaybeTransient`, used by the `SCHEMA` branch and
`waitForRelationLockers`) is deliberately **transient** in autocommit: it
acquires, waits out any conflicting holder, and releases immediately — designed
for "observe the wait, don't hold anything across the statement" cases like
`REINDEX SCHEMA`'s no-op rebuild. A *physical* rebuild is different: it
`TruncateRelation`s a live index's storage, so it must genuinely exclude any
concurrent reader/writer of the same objects for the rebuild's duration, or a
concurrent index scan could be mid-Pin of a page the truncate is about to
discard, and a concurrent DML write to the table could be lost from the
rebuilt index (it's scanned once, not maintained incrementally).

New `reindexOp.acquireReindexLocks(idxRel, tblRel)` acquires — and returns a
closure that releases — two locks directly against the package-level
`tableLockMgr` (bypassing the transient-release wrapper):

- `ShareLock` on the parent table (blocks concurrent writes, allows reads).
- `AccessExclusiveLock` on the index itself (blocks any concurrent read that
  would use it — `acquireScanIndexReadLocksTxn` already AccessShare-locks every
  index of a scanned relation — and any concurrent `DROP INDEX`).

This mirrors `reindex.sgml` exactly: "REINDEX locks out writes but not reads of
the index's parent table. It also takes an ACCESS EXCLUSIVE lock on the
specific index being processed." `rebuildIndex` calls this immediately before
the bulk rebuild and `defer release()`s it, so the exclusion window is scoped
to the physical work, not the whole connection/session. A zero backend
identity (no live connection — embedded/test contexts) or a system-catalog OID
skips locking entirely, matching every sibling helper's behaviour.

Backend identity selection mirrors `tryAcquireMaintenanceLock`: the
transaction's `TxnLockBackendID` when inside an explicit block, else the
per-statement `BackendID`. Lock-wait failures (deadlock / statement
cancellation) map onto the same `ExecError` codes
(`40P01`/cancellation/`XX000`) `acquireRelLockMaybeTransient` uses, via the new
`reindexLockWaitError` helper.

### What stays a no-op (deliberately, this slice)

- **`REINDEX INDEX/TABLE ... CONCURRENTLY`.** Real PostgreSQL builds a second,
  shadow index concurrently and atomically swaps it in — goopg has no such
  build-then-swap machinery (a bare `TruncateRelation` on the live index would
  make it unusable mid-rebuild, defeating the entire point of `CONCURRENTLY`
  not blocking access). The existing wait-for-lockers behaviour
  (`waitForRelationLockers`, M0118-0008) is unchanged; only the trailing
  physical rebuild remains a no-op, exactly as before this slice.
- **`REINDEX SCHEMA CONCURRENTLY`.** Its per-relation loop still only calls
  `waitForRelationLockers` — the same behaviour as before this follow-up; it
  does not rebuild. Needs the same shadow-index build-then-swap machinery as
  the other `CONCURRENTLY` forms above.
- **`REINDEX DATABASE` / `REINDEX SYSTEM`.** Unchanged catalog-only no-op (no
  `case` in the `ObjectType` switch at all); `reindexdb`'s TAP-ported tests
  (`TestPort_Scripts090Reindexdb`/`091ReindexdbAll`) only ever issue
  `REINDEX DATABASE`, so they are unaffected by this slice.
- **`REINDEX ... pg_toast.<name>`.** Still routed through the dedicated
  TOAST-relation branch above the `ObjectType` switch (M0118-0008's
  reindex-concurrently-toast epic) — a synthetic catalog-only relation with no
  physical storage, unaffected.

## Tests

- `internal/executor/reindex_physical_rebuild_test.go`:
  - `TestReindexIndexPhysicallyRebuilds` — truncates a live index's storage to
    0 blocks (simulating on-disk corruption), runs `REINDEX INDEX`, and scans
    the rebuilt btree directly (`btree.Open` + `RangeScan`, bypassing SQL) to
    confirm all 5 rows are back. Confirmed non-vacuous via `git stash` on
    `operators_reindex.go` alone — fails with `short read at block` pre-fix.
  - `TestReindexTablePhysicallyRebuildsAllIndexes` — `REINDEX TABLE` rebuilds a
    truncated btree index and silently leaves a sibling `gist` index alone (no
    error). Confirmed non-vacuous the same way.
  - `TestReindexIndexBlocksBehindConcurrentIndexReader` — a second backend
    holding `AccessShareLock` on the index blocks `REINDEX INDEX` until it
    releases (goroutine + channel, mirrors `lock_integration_test.go`'s style).
    Confirmed non-vacuous: fails (`REINDEX INDEX completed while a concurrent
    ... reader still held the index`) pre-fix, since the pre-fix code never
    acquired any lock for the `INDEX` case at all.
  - `TestReindexSchemaPhysicallyRebuildsAllTables` (2026-07-09 follow-up) — two
    tables in one schema, each with a truncated btree index; `REINDEX SCHEMA`
    must rebuild both. Confirmed non-vacuous via `git stash` on
    `operators_reindex.go` alone — fails with `short read at block` pre-fix.
- Live-verified against the real `cmd/goopg` binary: created a table + btree
  index, confirmed `EXPLAIN` chose an Index Scan, stopped the server, zero-byte
  truncated the index's on-disk relfilenode file directly (`base/<db>/<oid>`),
  restarted — the query now failed with `short read at block`. `REINDEX INDEX
  <name>` reported `OK`, the same query then returned the correct row and
  `EXPLAIN` still chose the Index Scan; `REINDEX TABLE <name>` afterward left
  the data intact (`SELECT count(*)` unchanged).
- Gates: `go build ./...` / `go vet ./internal/executor/...` clean; `go test
  ./internal/executor/... ./internal/access/btree/... ./internal/catalog/...
  ./internal/planner/...` PASS; `go test ./internal/testport/... -run
  'TestPort_IsolationReindex|TestPort_IsolationMultipleCic'` PASS (all 4 specs,
  no regression — `TestPort_IsolationReindexSchema` in particular still passes
  after the 2026-07-09 follow-up wired physical rebuild into the `SCHEMA`
  branch, confirming the concurrent-drop-tolerance behaviour survived);
  `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
  `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0 failed
  txns, all 3 workloads).

## Deferral

`REINDEX ... CONCURRENTLY`'s physical rebuild (all three object types —
`INDEX`/`TABLE`/`SCHEMA`; needs a genuine shadow-index build-then-swap, a
materially larger capability) remains open — see `.ralph/deferral_ledger.md`.
Also carried forward unchanged from the parent M0122-0007 bucket: `CREATE`/
`DROP DATABASE` full DDL, tablespace physical relocation.
