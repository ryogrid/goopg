Task: M0122-0006 follow-up — "index properties beyond ordering" (predicate/
INCLUDE/opclass/collation/fillfactor/dedup/NULLS NOT DISTINCT survive a
genuine crash restart). COMPLETE and committed this loop.

Files: internal/wal/recovery.go (CreateIndexPayload gained 8 new fields;
new optional self-describing trailing "extension" block —
hasCreateIndexExtension/encodeCreateIndexExtension/
decodeCreateIndexExtension — appended after the existing ColDescending/
ColNullsFirst blocks, omitted entirely for a plain index so old WAL/tests
are byte-unaffected), internal/wal/index_ddl_test.go (4 new tests),
internal/executor/operators_ddl.go (new btreeIndexProps struct;
createBTreeIndex takes `props ...*btreeIndexProps` instead of the old bare
`predExpr ...planner.Expr`, sets all 8 fields on idx BEFORE WAL emission;
only 2 of 16 call sites changed: execCreateIndex's direct statement path
and createPartitionChildIndexes — the other 14 just omit the new optional
arg, as before), internal/catalog/catalog.go (RegisterIndexDuringRecovery
gained the same 8 params), internal/initdb/index_ddl_recovery.go (interface
+ pure-WAL call site thread the decoded payload through),
internal/initdb/open.go (heap-path call site passes zero values for the 8
new params — heap decode doesn't have them yet, documented residual, not a
regression), internal/initdb/index_ddl_recovery_test.go (new true-crash
test), docs/design/0122-0006-index-column-order-restart-persistence.md
(new "Follow-up (2026-07-08)" section), docs/design/README.md (0122-0006
row appended), .ralph/deferral_ledger.md (prior 2026-07-08 row flipped to
resolved; new row appended for the heap-path residual), .ralph/fix_plan.md
(M0122-0006 bullet dated update).

Key symbols: createBTreeIndex (operators_ddl.go) — sets
idx.HasPredicate/PredicateString/IncludeColumns/ColOpClasses/ColCollations/
Fillfactor/DeduplicateItems/NullsNotDistinct from `xp *btreeIndexProps`
immediately after `idx.ColDescending = colDescending`, before the
wal.EncodeCreateIndex block. wal.DecodeCreateIndex's dispatch is now
3-generation: remaining==0 (pre-M0122-0006), remaining==2*numCols
(M0122-0006, order blocks only), remaining>2*numCols (this follow-up,
order blocks + extension). RegisterIndexDuringRecovery
(internal/catalog/catalog.go:5243ish) sets the 8 new fields directly on
the Index struct literal.

Findings: idx.Predicate (the parsed parser.Expr AST, as opposed to
PredicateString) is deliberately NOT threaded through WAL/recovery —
confirmed via grep that nothing reads idx.Predicate again after CREATE
INDEX finishes (build-time row filter runs once; pg_get_indexdef/pg_dump
render from PredicateString only), so it stays nil after a WAL-only
crash-restart recovery — harmless, no observable behavior difference.
Confirmed non-vacuous via a temporary revert (reverted the WAL-payload
literal to omit the new fields while leaving idx.* correctly set
in-memory — exact old-bug shape) — all 8 new assertions failed as
expected, then restored. Live end-to-end verified against the real
cmd/goopg binary with an actual kill -9 (no checkpoint in between):
`CREATE UNIQUE INDEX ext_idx ON ext (a, b COLLATE "C" text_pattern_ops)
INCLUDE (c) WITH (fillfactor=70, deduplicate_items=off) NULLS NOT
DISTINCT WHERE (a > 0)` — pg_get_indexdef and pg_index columns all
correct post-restart.

Sibling gap (NOT fixed, new ledger row appended): the heap-recovery driver
(loadUserIndexesFromHeap, internal/initdb/open.go) still can't restore ANY
of these 8 fields after a CHECKPOINTED restart — catalog.PGIndexRow/
DecodePGIndexPhysicalRow (codec.go) never decode indclass/indcollation/
indexprs/indpred content, no indnullsnotdistinct heap field exists, and
buildUserPGIndexRow (pg18_user_catalog_rows.go) always writes those as
zero/NULL regardless of idx's real values. Fillfactor/DeduplicateItems ARE
already restored on checkpointed restart, but via the separate
ApplyIndexReloptions/pg_class.reloptions mechanism, not pg_index. Full
resume point (write-side buildUserPGIndexRow changes, read-side
DecodePGIndexPhysicalRow changes, expr-as-SQL-text reuse of
column_defaults_recovery.go's parser.ParseExpr pattern for indexprs/
indpred) is in the ledger row appended today.

Next step: pick the next task. M-NIGHTLY is clean (all 8 items from run
20260707-000712 checked/resolved per fix_plan.md; ci/logs/action-items.md
unchanged since). Candidates: (a) the heap-path residual above (checkpointed
restart still loses predicate/opclass/collation/INCLUDE/NULLS-NOT-DISTINCT)
— well-scoped, exact resume point in the ledger row, would need a NEW test
using a graceful rt1.Close() (checkpointed) instead of the true-crash
pattern; (b) M0122-0006's remaining items (pg_tablespace visibility, fuller
pg_index heap persistence framing); (c) M0122-0007/0008 quick wins
(DDL/admin commands, auth/roles — SASLprep/channel binding/
scram_iterations still open in 0122-0008); (d) resume M0119-0004/0005/0006/
0007 per the Current Priority banner (M0119-0004's per-database
catalog-isolation gap, M0119-0006's opclass/pg_amproc-UPDATE-path gap
documented in the 2026-07-07 M0122-0001 ledger row, M0119-0006's
005_opclass_damage.pl two-part gap: pg_amproc Virtual-UPDATE path +
internal/access/btree has zero opclass/comparator dispatch).

Gates run: go build ./... clean, go vet ./... clean. go test
./internal/wal/... ./internal/catalog/... ./internal/executor/...
./internal/initdb/... ./internal/planner/... ./internal/analyzer/...
./internal/parser/... ./internal/server/... PASS. go test -short ./...
(excluding tpch/testport) PASS, full suite (48 packages). scripts/
tpch-spotcheck.sh PASS (Q12=2/Q13=33). RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh PASS (0 failed, all 3 workloads). Live
e2e: real cmd/goopg binary, actual kill -9 crash, verified
pg_get_indexdef + pg_index (indoption/indnullsnotdistinct/indclass/
indcollation) all correct post-restart. make ralph-state-guard: 2 benign
issues auto-repaired (identical pattern to every prior loop —
status/progress running-vs-completed reconciliation).

In-flight: none. All manual verification servers/processes/temp files
killed and cleaned up (/tmp/goopg-verify*).
