# M0142-0003g — index-accelerating goopg's FK constraint validation scan

Status: implemented (2026-09-16 — FK parent-existence checks now probe the
parent's unique index instead of scanning the whole heap).

## Task

Filed by M0142-0003f's root-cause trace: goopg's FK parent-existence check
(`assertParentExists` → `scanRelForFKMatch`, `internal/executor/operators_fk.go`)
scanned the *entire parent heap*, block by block, on every call, instead of
consulting the parent's existing unique B-tree index — O(child rows × parent
scan distance) instead of PG's O(child rows) (`validateForeignKeyConstraint`,
`postgres/src/backend/commands/tablecmds.c:13694`, an indexed RI-trigger probe
per child row). At `partsupp`/`part` scale (child 800k rows, parent 200k rows)
this did not complete in several minutes; `lineitem`'s 6M-row FKs were never
attempted for real. The scan also had no cancellation check, so a mis-sized FK
add could not be aborted short of a server restart.

Fix required: (a) route the parent-existence check through an index probe when
a unique index covers the referenced columns; (b) add a cancellation check to
the scan's outer loop(s).

## Design

`scanRelForFKMatch` (`internal/executor/operators_fk.go`) is the single call
site `assertParentExists` (INSERT-time) and `fullTableFKCheckRel` (ADD
CONSTRAINT validation-time) both go through to answer "does a live parent row
matching these values exist?" It is now a two-line dispatcher:

```go
func scanRelForFKMatch(ctx *Context, tbl *catalog.Table, colNames []string, vals []Datum) (bool, *fkPendingRef, error) {
	if idx := findFKCoveringUniqueIndex(ctx, tbl, colNames); idx != nil {
		if key := fkProbeKeyForIndex(ctx, idx, tbl, colNames, vals); key != nil {
			... open the index, scanIndexForFKMatch ...
		}
	}
	return scanRelForFKMatchSeq(ctx, tbl, colNames, vals)
}
```

- `findFKCoveringUniqueIndex` — the first unique/PK btree index on `tbl` whose
  column set equals `colNames` (order-independent; PG's own FK-creation rule
  guarantees one exists whenever the FK was actually creatable,
  `transformFkeyCheckAttrs`). Skips partial indexes (can't certify absence
  across all rows).
- `fkProbeKeyForIndex` — builds the btree search key via
  `ctx.indexRowProbeKey`, **the same probe-key builder
  `checkUniqueIndexesForInsert` already uses to maintain/probe unique
  indexes** — by placing each value at its column's position in a scratch
  `Row` and delegating. This mattered concretely during development: the
  first version called `encodeIndexKeyFromCols` directly (the "blob" key
  format), which silently mismatches whenever the index actually uses PG's
  real index-tuple format (`pgIndexTupleKey`, selected when
  `ctx.pgIndexKeyDesc(idx)` is non-nil) — every `RangeScan` then reported a
  false "not found" for values that were present. This regressed
  `TestAlterTableAddForeignKeyDanglingRow`/`NotValidThenValidate` (wrong
  DETAIL — reported the first, present, value as missing instead of the
  actually-dangling one), caught immediately by the executor test suite.
  Routing through the shared probe-key builder instead of a second,
  hand-rolled encoder eliminates the whole class of "which key format does
  this index actually use" bugs — see `pgindex_btree.go`'s `indexRowKey` for
  the two formats.
- `scanIndexForFKMatch` — an exact-key `BTree.RangeScan(key, key, ...)` probe
  (mirrors `uniqueCheckWithWait`'s `scanOnce`: pin/lock/read/unlock before
  inspecting the tuple header, which `PageGetHeapTuple` returns as a value
  copy safe to read unlocked).
- `fkPendingOutcome` — the visibility / multixact / key-changing-wait
  classification for a single already-key-matched tuple, extracted **verbatim**
  from the pre-existing `scanRelForFKMatch` body so it is shared, unchanged,
  by both the new indexed path and the renamed unindexed fallback
  (`scanRelForFKMatchSeq`) — this is the `pattern_sibling_paths_must_agree`
  guard: the FK-vs-in-flight-updater wait semantics (FOR KEY SHARE
  compatibility, multixact updater resolution, cross-partition move
  detection upstream in `scanTableForMatchFKWait`) must never diverge between
  the two scan strategies.
- `scanRelForFKMatchSeq` — the old full-heap-scan behavior, unchanged except
  it now calls `fkPendingOutcome` instead of inlining the same logic, and
  checks `ctx.Ctx.Err()` per block so a client disconnect / statement
  cancellation can actually interrupt it (it previously could not —
  `pg_terminate_backend` on a stuck scan returned success but the backend
  kept running, per -0003f). It remains the correctness fallback for FKs with
  no covering unique index found, or a probe key that can't be built (a NULL
  value in a partial-NULL multi-column FK — NULLs are absent from btree
  unique-index entries).

`fullTableFKCheckRel` (the ADD-CONSTRAINT-time outer loop over every child
row) also gained a `ctx.Ctx.Err()` check per block: with the inner parent scan
now O(log parent rows), this outer O(child rows) loop is the dominant residual
cost for a large child table (e.g. `lineitem`'s 6M rows), so it is the one
that most needs to stay cancellable.

## Verification

- `go test ./internal/executor/...` — full package green (12.9s), including
  every FK/unique test (`TestAlterTableAddForeignKeyDanglingRow`,
  `NotValidThenValidate`, `TestFKOmittedRefColumnsResolveToPrimaryKey`,
  `TestUpdateCoercesDateLiteralBeforeFKCheck`, the multixact/wait tests, …).
  These four caught the key-format bug described above on the first attempt.
- Realistic-scale functional + perf check on an isolated throwaway cluster
  (port 5533, binary built to `/tmp/goopg-fk-check`, cleaned up after):
  parent table 200,000 rows (PK), child table 800,000 rows (matching
  `partsupp`/`part`'s exact row counts from -0003f).
  - Clean case (`pid` column fully populated from the parent's key range):
    `ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY` completed in **6.7s**
    (previously did not finish in several minutes at this same scale).
  - Dangling-row case (one child row referencing a nonexistent parent key):
    completed in **9.4s** and raised the correct `23503` with byte-exact
    `DETAIL: Key (pid)=(999999) is not present in table "fkperf_parent".`
- `scripts/tpch-spotcheck.sh` could not be run to completion this loop — see
  "Gate note" in the M0142-0003g fix_plan entry: its private `pg_basebackup`
  clone of the shared `:65433` bench cluster came up with `lineitem` missing,
  most likely because of the backend still stuck mid-DDL on the *pre-fix*
  binary since -0003f (`M0142-0003h`'s open finding). This is environmental,
  not a regression from this change — confirmed a separate, already-failing
  parser test (`TestLockingClauseParity`) reproduces identically with and
  without this diff via `git stash`.

## Follow-up

**M0142-0003i** (filed): resume -0003f now that the blocker is gone — add the
remaining 5 FKs to the shared cluster, persist the DDL so it survives
`--reset`, and re-run Q9's `EXPLAIN`/estimate-audit. That task should also
resolve the abandoned PID 81 backend, since -0003g's own loop found it
corrupts online clones of the cluster (not just an inert leftover).
