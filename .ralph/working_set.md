(idle — nothing in flight)

Last landed: DU-002 slice 203 (loop #16) — second INTEGER autovacuum storage
parameter (`autovacuum_analyze_threshold`, range 0–INT_MAX) round-trips through pg_dump.

What happened: pure reuse of slice 198's integer path. autovacuum_analyze_threshold is
RELOPT_TYPE_INT (reloptions.c:254/1881: default -1, range 0–INT_MAX). No parser change.
Mechanism: executor strconv.Atoi + `0 ≤ N ≤ 2147483647` bounds-check (overflow/non-int
→ 22023; negatives are a parser syntax error); separate AutovacuumAnalyzeThresholdSet
flag so explicit 0 round-trips; persist catalog.Table.AutovacuumAnalyzeThreshold (int);
pg_class virtual view appends `autovacuum_analyze_threshold=N` after
autovacuum_vacuum_cost_delay; pg_dump renders WITH (autovacuum_analyze_threshold='50').
Advisory catalog/dump-only; base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumAnalyzeThreshold/…Set ~L501
+ render ~L2246), internal/executor/operators_ddl.go (extract/parse ~L1158 + persist
~L1226), operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumAnalyzeThresholdSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optaat fixture ~L795 + assertion ~L2564),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 203), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; go vet catalog/executor/testport
clean; catalog+executor reloption tests PASS; TestPort_PgDumpConnectionSetup PASS;
pgbench pre-commit smoke on commit.

Next (slice 204 candidates): (1) int autovacuum_vacuum_insert_threshold
(RELOPT_TYPE_INT, same Set-flag int path as slice 198/203). (2) composite types
(CREATE TYPE AS) — larger, pg_class.reltype hardcoded 0 (pg18_user_catalog_rows.go:453).
