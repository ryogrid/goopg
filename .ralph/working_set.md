(idle — nothing in flight)

Loop #24 (this loop) — COMPLETE, committed + pushed (`2cacac14`, on top of
the peer's `d60bba27`).

Task: M0119-0004's index-reloptions residual (fillfactor/deduplicate_items/
fastupdate/gin_pending_list_limit/pages_per_range/autosummarize now survive
a restart for indexes, mirroring the table/view fix from an earlier loop).

While root-causing why the fix didn't take effect via a real
Init/Open/DDL/Close/Open repro, also found and fixed two deeper
pre-existing bugs the reloptions gap had been masking:
1. `mirrorTouchedCatalogsToPostgresDB` (internal/executor/
   sys_catalog_postgres_db_mirror.go) never mirrored pg_index (2610) to
   PostgresDBOid — `loadUserIndexesFromHeap`'s heap-scan recovery had
   *never* found any user index's pg_index row in real deployments
   (cat.DBOID() is almost always 5/"postgres" per `detectCatalogDBOID`),
   silently falling back to WAL replay for every index, always. Added
   `catalog.IndexRelationId` to `mirroredOIDs`.
2. Fixing (1) unmasked a dual catalog-key registration bug:
   `RegisterIndexDuringRecovery`/`UnregisterIndexDuringRecovery`
   (internal/catalog/catalog.go) used a bare `key(...)` map probe instead
   of the "" vs "public." collision-aware `lookupIndexLocked` resolution
   reads and `RenameIndexDuringRecovery` already use — broke the
   pre-existing `TestRenameIndexSurvivesRestartViaWAL` the moment
   heap-scan recovery started actually running (heap-scan registers under
   the resolved schema, e.g. "public.foo"; WAL-replay registers under the
   raw unqualified DDL schema, "foo" — two objects for one index). Fixed
   by switching both functions to `lookupIndexLocked`.

Files: internal/catalog/catalog.go (BuildIndexReloptions/
IndexReloptionsElements/ApplyIndexReloptions, mirroring the table/view
builders; RegisterIndexDuringRecovery/UnregisterIndexDuringRecovery
lookupIndexLocked fix), internal/executor/pg18_user_catalog_rows.go
(buildUserPGClassRowForIndex encodes real reloptions), internal/executor/
operators_ddl.go (new resyncIndexClassHeapRow + a resyncIndexHeapRow call
right after CREATE INDEX's WITH-clause field-setting block — that block
previously only mutated the live in-memory idx, never the heap row already
written by syncIndexToCatalogHeap), internal/executor/
sys_catalog_postgres_db_mirror.go (pg_index mirror fix), internal/initdb/
open.go (loadUserIndexesFromHeap decodes+applies reloptions).

Tests: TestBuildUserPGClassRowForIndexReloptionsSurvivesHeapEncode
(executor), TestIndexReloptionsSurviveRestart (initdb, full
Init/Open/DDL/Close/Open restart regression); re-verified
TestRenameIndexSurvivesRestartViaWAL / TestCreateIndexRecoveredOIDDoesNotCollide
still green after the dual-registration fix.

Gates run: go build ./... clean; go test ./internal/catalog/...
./internal/executor/... ./internal/initdb/... (full packages, -count=1)
PASS; go test ./internal/server/... PASS; scripts/tpch-spotcheck.sh PASS
(Q12=2/Q13=33); pre-commit pgbench smoke PASS (0 failed, TPC-B ~213 TPS,
simple-update ~174 TPS, select-only ~13.6k TPS); make ralph-state-guard
auto-repaired the routine running/completed progress-marker skew, OK.

Deferral ledger (M0119-0004 index-reloptions follow-up row): landed both
bugs above; ONE gap left open (bounded scope, not fixed this loop):
`createPartitionChildIndexes` never copies Fillfactor/DeduplicateItems/
FastUpdate/GinPendingListLimit/PagesPerRange/AutoSummarize onto a
partition-child index at all (only HasPredicate/IncludeColumns/expression
strings are carried over), so `CREATE INDEX ... WITH (fillfactor=N)` on a
partitioned parent silently drops the storage parameter on every child.
Resume point: internal/executor/operators_ddl.go's
createPartitionChildIndexes — copy the WITH-clause fields the same way
HasPredicate/IncludeColumns are already copied, then call the new
resyncIndexClassHeapRow if heap sync is available.

Concurrency note: peer ralph_loop.sh was live throughout this loop, writing
docs/design/0020-0001-window-parser-and-ast.md, internal/analyzer/
{analyzer.go,analyzer_test.go}, internal/executor/
{operators_window.go,window_compat_test.go}, internal/planner/planner.go,
unimplemented_feat.json, .ralph/{fix_plan.md,progress.json} — none of
those touched. Committed via explicit `git commit -- <10 files>` (message
before `--`); `git show --stat HEAD` confirmed only those 10 files changed.
Fetched first (origin was a clean ancestor) then pushed a clean
fast-forward (d60bba27..2cacac14).

Next step: pick up the partition-child-index-reloptions gap above, or
resume M0122-0004's window-frames/GROUPING SETS backlog (peer is mid-flight
on named/aggregate windows — re-check `git status` before touching
internal/analyzer, internal/planner/planner.go, or
internal/executor/operators_window.go), or M0122-0003's
pg_stat_io/track_io_timing remainder, or the next M0119 pg_dump
catalog-view parity slice. **Re-check `git status` first** — the peer loop
may have new WIP by the time the next loop starts.
