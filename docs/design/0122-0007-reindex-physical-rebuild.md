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

## `REINDEX ... CONCURRENTLY` physical rebuild: shadow-file build-then-swap (2026-07-09 follow-up)

`REINDEX INDEX/TABLE/SCHEMA CONCURRENTLY` now also physically rebuilds —
previously only the plain form did (see "What stays a no-op" above, now
superseded for these three object types; `REINDEX DATABASE`/`SYSTEM` and the
synthetic `pg_toast.*` relation form remain catalog-only no-ops, unchanged).

Real PostgreSQL rebuilds a `CONCURRENTLY` index via a genuinely different
mechanism than the plain form: it creates a second, shadow index catalog
entry, builds it via `CREATE INDEX CONCURRENTLY`'s own multi-phase
build+validate+build protocol, swaps the two indexes' relfilenodes
(`index_concurrently_swap`, `indexcmds.c`), then drops the now-superseded old
one. goopg's simpler equivalent, reusing pieces already on hand:

- **Build.** `buildIndexShadow` (`operators_reindex.go`) mints a fresh,
  catalog-invisible `RelFileNode` — same tablespace/database identity as the
  live index, but a brand-new `RelOid` via `Catalog.AllocOID()` (the same
  "synthetic OID, no catalog entry" pattern named CHECK constraints already
  use) — and runs the *exact* `bulkBuildBTree`/`bulkBuildBTreeWithPredicate`
  path plain `REINDEX` uses, targeting that RelFileNode instead of the live
  index's own. Because it is a different file, the live index keeps serving
  every concurrent reader/writer throughout the build — nothing needs to
  block for this to be correct. `Pool.FlushRel` durably flushes the shadow
  file before it is considered swap-eligible.
- **Wait.** Exactly the pre-existing `waitForRelationLockers` call this file
  already made for `CONCURRENTLY` before this follow-up — unchanged in
  position, target relation, or semantics, so `reindex-concurrently.spec`'s
  session-interleaving assertions (which key off this wait's exact timing)
  are unaffected. `TABLE`/`SCHEMA` still make exactly ONE such call per
  table (`rebuildTableIndexesConcurrently` builds every index's shadow
  first, waits once, then swaps each), not one per index.
- **Swap.** `swapRelationPhysicalFile` (`operators_ddl.go`) atomically
  replaces the live index's on-disk file with the shadow file via
  `os.Rename` (same filesystem ⇒ atomic — a crash on either side of the
  rename leaves either the pre- or the fully-rebuilt file, never a torn
  one), after `InvalidateRel`+`CloseRelation` on both identities so no stale
  buffer-pool cache entry survives the swap. The index's OID/RelFileNode
  identity is **never changed** — only its bytes are — so, unlike ALTER
  ...  SET TABLESPACE's relocation (which moves a relation to a genuinely
  new path the catalog must learn), this needs **no catalog mutation and no
  WAL record**: the renamed file already **is** the durable state once the
  rename returns. The swap is guarded by the exact same `acquireReindexLocks`
  hold (ShareLock on the table + AccessExclusiveLock on the index) plain
  `REINDEX INDEX` already takes for its *entire* rebuild — here taken only
  around the brief swap itself, which is the entire point of `CONCURRENTLY`
  not blocking for the (potentially long) build.
- **Cleanup.** `removeShadowRelationFile` best-effort removes an
  already-built shadow file that turns out not to be needed (a sibling
  index's build failed, or the post-build wait itself errored) —
  non-fatal, mirroring `relocateRelationPhysicalFileCleanupOld`'s "harmless
  orphaned bytes, never lost data" contract.

**Deliberately not implemented — same simplification `CREATE INDEX
CONCURRENTLY` already makes** (that code's own single build + single
start-time-snapshot wait, no second validation scan): a write that lands on
the table *while the shadow build's heap scan is in flight* is not
guaranteed to appear in the rebuilt index. Real PostgreSQL closes this gap
with a second, incremental validation scan after the first `WaitForLockers`
(`validate_index`); goopg does not implement that for either `CONCURRENTLY`
form. In practice this only matters for a write racing the exact
milliseconds of the (usually short) heap scan — the far more common case,
repairing a corrupted or stale index with no concurrent writers, rebuilds
correctly and was live-verified end to end (corrupt `base/<db>/<oid>` file →
`short read at block` → `REINDEX INDEX CONCURRENTLY` → same relfilenode,
query works, survives a restart with no orphaned shadow file left behind).

Tests: `TestReindexIndexConcurrentlyPhysicallyRebuilds`,
`TestReindexTableConcurrentlyPhysicallyRebuildsAllIndexes`,
`TestReindexIndexConcurrentlyDoesNotBlockConcurrentIndexReader`
(`internal/executor/reindex_physical_rebuild_test.go`). Gates: `go build
./...` clean; `go test ./internal/executor/... ./internal/access/btree/...
./internal/catalog/... ./internal/planner/...` PASS; `go test
./internal/testport/ -run
'TestPort_IsolationReindex|TestPort_IsolationMultipleCic'` PASS (all 4
specs, no regression — confirms the wait-call timing this follow-up was
careful to preserve actually held); `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh`
PASS (0 failed txns, all 3 pgbench workloads).

## Deferral

`REINDEX SCHEMA CONCURRENTLY`'s per-table wait/swap loop does not re-validate
that the CURRENT table (the one whose shadows were just built) still exists
between the wait and the swap — only the pre-existing "next table in the
loop" concurrent-drop tolerance is covered (a table dropped while an
*earlier* table's wait is in flight). A `DROP TABLE` racing the exact
in-flight table's own build/wait/swap window is untested and unhandled,
mirroring a gap that already existed for the wait phase alone before this
follow-up. `REINDEX ... CONCURRENTLY`'s single-scan-without-revalidation
limitation (previous paragraph) is the other open item from this slice. Also
carried forward unchanged from the parent M0122-0007 bucket: `CREATE`/`DROP
DATABASE` full DDL, tablespace physical relocation.
