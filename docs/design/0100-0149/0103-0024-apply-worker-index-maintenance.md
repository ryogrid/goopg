# 0103-0024 — Apply-worker index maintenance for fresh-session visibility

Status: ACCEPTED 2026-05-14 (M0103-0007 rung 1)

## Context

M0103-0006 noted as a caveat that goopg's apply-worker writes were
not visible to a fresh `database/sql` session in the same cluster:
the disk file under `base/1/<oid>` carried the applied tuple, but
`SELECT count(*) FROM public.t WHERE id = 1` from a new connection
returned 0. The caveat hand-waved the cause as "the apply worker
writes outside the dispatcher's MVCC view" and deferred the work to
M0103-0007's Scenario A invariants.

The actual root cause is narrower and unrelated to MVCC visibility:
the apply worker called `writeHeapRow` only, with no index
maintenance. Every dispatcher-side INSERT goes through
`maintainUniqueIndexesForInsert` (landed in M0100-0005) which writes
into the primary-key and any other unique btree indexes. The apply
worker skipped that step. As a result:

- SeqScan saw the tuple correctly. `SELECT id, v FROM public.t`
  returned `[[1 hello]]`.
- The MVCC layer was correct. The xmin/xmax stamps were valid;
  `id::text = '1'` and `id = 1::int4` both returned a count of 1
  because the planner picked a SeqScan path for them.
- Bare equality predicates on the indexed column (`WHERE id = 1`)
  fell back to an IndexScan plan, which probed the PK btree, found
  no matching key, and returned 0 rows.

A new dispatcher INSERT into the same table (`INSERT INTO public.t
VALUES (2, 'world')`) was matched correctly by `WHERE id = 2`,
confirming the predicate evaluator and IndexScan operator both work
— the missing piece was strictly the index-side insert.

The dispatcher's UPDATE/DELETE paths face the same divergence:
`applyDeleteByKey` removes the heap tuple but never deletes the
index entry, and `applyUpdateByKey` was a `applyDeleteByKey` +
`writeHeapRow` pair without index re-insertion. The fix here closes
the INSERT and the UPDATE-new-tuple sides; UPDATE-old-tuple /
DELETE index deletion is left as a follow-up (btree's tombstone
mechanics are out of M0103-0007 rung-1 scope and require their own
design). Goopg's index probes already tolerate orphaned index
entries — the IndexScan operator re-fetches the heap tuple and
re-checks visibility, so a stale index entry yields zero rows but
no corruption.

## Decision

Pipe the freshly-written tuple's `storage.ItemPointer` through to
`maintainUniqueIndexesForInsert` from inside `ApplyWorker.applyInsert`
and from inside `applyUpdateByKey`. Both paths now mirror the
dispatcher's INSERT semantics for unique/primary indexes.

### Surface

- `internal/executor/applyworker.go::applyInsert`:
  - `writeHeapRow(ctx, rel, cols, row)` → `writeHeapRowReturning(...)`,
    capturing the new `ptr`.
  - Follow-up call `maintainUniqueIndexesForInsert(ctx, r.local,
    r.local.Columns, row, ptr)`. The function tolerates a nil/empty
    catalog and silently skips relations with no eligible indexes,
    so the change is backward-compatible with tests that don't
    create a PK.

- `internal/executor/applyworker.go::applyUpdateByKey`:
  - Signature gains `*catalog.Table` so the helper can resolve
    `IndexesOnTable`.
  - `writeHeapRow` → `writeHeapRowReturning`; if `tbl != nil` the
    same `maintainUniqueIndexesForInsert` follow-up runs.

- `internal/executor/applyworker.go::applyUpdate`: passes
  `r.local` through to the renamed helper.

No public-API changes outside the executor package. `ApplyWorker`
already carries `cat` (catalog.Catalog), `pool`, and `txnMgr`, so the
context built in each apply path satisfies
`maintainUniqueIndexesForInsert`'s preconditions.

## Verification

- `TestPubSubClusterSmokePGToGoopgFreshSessionVisibility` (new) in
  `internal/testutil/pubsubcluster/cluster_test.go`: drives the full
  upstream-PG + goopg-subscriber harness, applies one INSERT, then
  asserts `count(*) WHERE id = 1` returns 1 via `WaitForRow`
  (fresh `database/sql` connection per probe). Before the fix:
  fails at the 10 s deadline because the IndexScan returns 0 rows.
  After the fix: passes in ≈ 2 s.

- `TestApplyWorkerInsertsRowFromPgoutputStream` (existing) still
  passes — confirms the heap-side write path is unchanged.

Focused regression: `./internal/executor/ ./internal/server/
./internal/wal/ ./internal/catalog/ ./internal/storage/
./internal/testutil/pubsubcluster/` all green with `-race`.

## Follow-up

- UPDATE old-tuple / DELETE index-entry deletion: orphaned index
  entries are tolerated today (IndexScan re-fetches and re-checks
  visibility), so the next rung in M0103-0007 only needs to close
  this if a Scenario A test surfaces a false-positive index hit.
  Documented in `.ralph/fix_plan.md` under M0103-0007.
- Non-unique secondary indexes: not yet maintained on the
  apply-worker side; same rationale as the above tolerance applies
  until a Scenario A failover test exposes a real divergence.
