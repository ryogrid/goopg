(idle — nothing in flight)

Loop #23 (this loop) — COMPLETE, committed + pushed (`8107a8de`, on top of
the peer's `3a370819`).

Task: M0119-0004's partition-child-index-reloptions residual (the deferral
row an earlier loop opened): `createPartitionChildIndexes` never copied the
parent's WITH-clause storage parameters (fillfactor/deduplicate_items/
fastupdate/gin_pending_list_limit/pages_per_range/autosummarize) onto an
auto-created partition-child index, so `CREATE INDEX ... WITH
(fillfactor=N)` on a partitioned parent silently dropped the option on
every child.

Files: internal/executor/operators_ddl.go (createPartitionChildIndexes —
copy the six WITH-clause fields onto childIdx alongside the pre-existing
HasPredicate/IncludeColumns copy, then resyncIndexClassHeapRow when heap
sync is available), internal/executor/partition_create_index_recurse_test.go
(new TestCreateIndexRecursesPartitionTreeCarriesReloptions), internal/initdb/
view_ddl_recovery_test.go (new TestPartitionChildIndexReloptionsSurviveRestart,
full Init/Open/DDL/Close/Open restart regression), .ralph/deferral_ledger.md
(flipped the M0119-0004 index-reloptions-follow-up row's status "-" →
"resolved"), docs/design/0110-0001-pg-dump-tap-port.md + README.md (new
"partition-child index reloptions" follow-up section + addendum sentence).

Key symbols: catalog.Index.{Fillfactor,DeduplicateItems,FastUpdate,
GinPendingListLimit,PagesPerRange,AutoSummarize}; executor.resyncIndexClassHeapRow;
executor.(*ddlOp).createPartitionChildIndexes.

Findings: since createPartitionChildIndexes recurses using the same
top-level CreateIndexStmt for multi-level partition trees, this one-site fix
propagates correctly to every descendant level, not just immediate children.
Considered and ruled OUT of scope: `ALTER INDEX parent ATTACH PARTITION
child` needs no equivalent fix — both indexes there come from independent
CREATE INDEX statements, each already carrying its own WITH-clause options.

Gates run: go build ./... clean; go test ./internal/catalog/...
./internal/executor/... ./internal/initdb/... (full packages, -count=1)
PASS; scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33) — two earlier runs in
this loop failed/hung due to a concurrently-running peer ralph loop's own
build+test load (transient resource contention, not a regression from this
change — reproduced clean on retry); make ralph-state-guard auto-repaired
the routine running/completed progress-marker skew (peer loop's own marker
churn), OK; pre-commit pgbench smoke PASS (0 failed, TPC-B ~227 TPS,
simple-update ~245 TPS, select-only ~14.2k TPS).

Concurrency note: peer ralph_loop.sh was live throughout this loop and
completed its own task (M0122-0004 first_value/last_value/nth_value window
functions, committed b9cfc369 + working_set carry 3a370819) while I worked —
none of its files (internal/analyzer/*, internal/planner/planner.go,
internal/executor/operators_window.go, internal/executor/window_compat_test.go)
were touched by me. Committed via explicit `git commit -- <6 files>`
(message before `--`); `git show --stat HEAD` confirmed only those 6 files
changed. Fetched first (origin was a clean ancestor at 3a370819) then pushed
a clean fast-forward (3a370819..8107a8de).

Next step: the M0119-0004 index-reloptions deferral chain is now fully
closed (parent + partition-child, both in-memory and heap-restart paths).
The peer loop's own working_set (now superseded by this write) named its
next candidates as: `ntile`/`cume_dist`/`percent_rank` as window functions,
ROWS/RANGE/GROUPS frame-clause parsing/execution (M0020's largest open
item), M0122-0003's pg_stat_io/track_io_timing remainder, or ledger row
480's comma/LATERAL ctx.OuterRows gap — any of these remain good candidates
for the next loop. **Re-check `git status` first** — a peer loop may have
new WIP by the time the next loop starts.
