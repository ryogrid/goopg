Loop #8 (this run): M0118-0008 — partition-drop-index-locking **PROMOTED** (design
0118-0074). Closed the LAST blocker (4 of 4): transactional-DDL cross-session
catalog visibility. COMMIT + push pending at loop end (after regress suite confirms).

## What landed (PROMOTION, not just an enabler)
A non-CONCURRENTLY `DROP INDEX` issued INSIDE an explicit transaction now DEFERS
its catalog/relfile/WAL/pg_class-xmax removal to COMMIT, so the dropped index
stays in the shared catalog (pg_class row visible to s3) and s2 is shown holding
its AccessExclusiveLock until s2commit → PG's 6 rows now match (was 5).

- session.go: `PendingIndexDrop` type + `BasicSession.pendingIndexDrops` +
  `AddPendingIndexDrop`/`TakePendingIndexDrops`/`CancelPendingIndexDropsToDepth`;
  `EndExplicitTransaction` nils the slice (safety net for every ROLLBACK path).
- operators_ddl.go: `execDropIndex` computes `deferSess` (!Concurrent &&
  InExplicitTransaction); records the drop + `continue`s instead of removing;
  new `ApplyPendingIndexDrops(ctx, sess)` does the real removal (mirrors the
  immediate path) — called BEFORE TxnMgr.Commit.
- operators_tx.go: `execCommit` calls ApplyPendingIndexDrops; ROLLBACK TO
  SAVEPOINT calls CancelPendingIndexDropsToDepth.
- dispatch.go: server simple-query TxCommit path calls ApplyPendingIndexDrops
  before TxnMgr.Commit (this is the path the isolation runner uses).
- isolation_port_test.go: TestPort_IsolationPartitionDropIndexLocking (strict).
- docs/test-port CSV (D-002 rationale sentence) + regenerated .md.
- docs/design/0118-0074-*.md + README index.

Key symbols: PendingIndexDrop, ApplyPendingIndexDrops, execDropIndex,
BasicSession.{pendingIndexDrops,EndExplicitTransaction}, CancelPendingIndexDropsToDepth.

## Known limitation (NOT a gap for this spec)
The deferral keeps the index visible to the dropping session too (shared catalog,
not per-session MVCC-filtered). This spec never re-queries it from s2, so output
is byte-identical. Full same-session invisibility = the MVCC-catalog milestone.

## M0118-0008 hard tail (remaining, all Effort-L)
- alter-table-4 + partition-concurrent-attach: need FULL per-session MVCC catalog
  visibility (dropping/altering session must see its own uncommitted DDL effects).
  THIS is the next milestone to start.
- reindex-concurrently-toast: real TOAST relations (reltoastrelid=0; text inline).
- WHERE CURRENT OF positioned UPDATE/DELETE: parsed (CurrentOf), no executor site.

Next step: start the per-session MVCC catalog visibility milestone (shared by
alter-table-4 + partition-concurrent-attach), OR pivot to reindex-concurrently-toast.

Gates run: go build ./... clean; TestPort_IsolationPartitionDropIndexLocking
strict PASS (both perms); DropIndexConcurrently1/ReindexConcurrently/ReindexSchema/
MultipleCic PASS; go test ./internal/executor/ + -race (DROP INDEX/txn/savepoint/
commit) PASS; TestPort_RegressSuite running (confirm before commit); pgbench
smoke = pre-commit hook.
