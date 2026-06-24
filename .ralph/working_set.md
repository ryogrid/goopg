Loop #7 (this run): M0118-0008 — pg_stat_activity retains last query on idle
(design 0118-0073). Closes "blocker 3" of 4 for partition-drop-index-locking.
NOT a promotion. COMMIT + push pending at loop end.

## What landed (enabler)
Two surgical halves so s3getlocks' `s.query` column matches PG byte-for-byte:
1. ENGINE: internal/activity/registry.go UpdateState — removed the
   `else if state=="idle" { c.Query.Store(nil) }` branch. Active query text now
   persists through idle (PG: query shows the last executed query in all
   non-active states; never-run backend still NULL; QueryStart untouched).
2. RUNNER FIDELITY: internal/testport/framework/isolation_runner.go execStep —
   for a SINGLE-statement step, send the trimmed verbatim body (keeps trailing
   ';') instead of the ';'-stripped splitSQLStatements result. splitSQLStatements
   + its unit test unchanged; multi-statement steps keep the split form.

Files: internal/activity/registry.go, internal/activity/activity_test.go (new
TestUpdateStateRetainsQueryOnIdle), internal/testport/framework/isolation_runner.go,
docs/design/0118-0073-*.md + README index.

Key symbols: ActivityRegistry.UpdateState, coldActivity.Query, execStep,
splitSQLStatements (unchanged).

## Probe result (2026-06-24)
Throwaway probe of partition-drop-index-locking.spec: the ENTIRE first
s3getlocks snapshot now matches PG (idle SELECT...; row + active DROP INDEX...;
rows, all with ';'). Diff now starts at blocker 4 only.

## partition-drop-index-locking remaining blocker (resume point)
4. **Transactional-DDL cross-session catalog visibility** (MILESTONE-SIZED,
   shared with alter-table-4 / partition-concurrent-attach): 2nd s3getlocks must
   still show the dropped index's pg_class row + locks until s2commit. goopg
   removes the index from the shared in-memory catalog synchronously at DROP
   INDEX, so the pg_locks JOIN pg_class loses the row → 5 vs 6 rows. Needs MVCC
   catalog visibility (uncommitted DDL invisible cross-session until commit).

Next step: blocker 4 is the only remaining blocker but is milestone-sized (MVCC
catalog subsystem). Either start that milestone, or pivot to another M0118-0008
tail spec. See hard-tail list below.

## M0118-0008 hard tail (all Effort-L, deferred)
- partition-drop-index-locking: blocker 4 only (MVCC catalog visibility).
- alter-table-4 + partition-concurrent-attach: same MVCC catalog visibility.
- reindex-concurrently-toast: real TOAST relations (reltoastrelid=0; text inline).
- WHERE CURRENT OF positioned UPDATE/DELETE: parsed (CurrentOf), no executor site.

Gates run: go build ./... clean; TestUpdateStateRetainsQueryOnIdle PASS;
go test ./internal/activity/ + -race PASS; go test ./internal/testport/framework/
PASS; strict isolation no-regression batch (LockCommittedUpdate/
DropIndexConcurrently1/CreateTrigger/InheritTemp/TruncateConflict/
ReindexConcurrently/MultipleCic) all PASS; pgbench smoke = pre-commit hook.
