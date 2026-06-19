(idle — nothing in flight)

Last landed: DU-002 slice 204 (loop #17) — third INTEGER autovacuum storage
parameter (`autovacuum_vacuum_insert_threshold`, range -1–INT_MAX, default -2)
round-trips through pg_dump.

What happened: pure reuse of slice 198/203's integer path. RELOPT_TYPE_INT
(reloptions.c:245/1879). Executor strconv.Atoi + `-1 ≤ N ≤ 2147483647` bounds-check
(overflow/non-int → 22023; a bare negative is a parser syntax error, so -1 floor is
reachable only via quoted '-1' reload); separate AutovacuumVacuumInsertThresholdSet
flag so explicit 0 round-trips; persist catalog.Table.AutovacuumVacuumInsertThreshold
(int); pg_class virtual view appends `autovacuum_vacuum_insert_threshold=N` after
autovacuum_analyze_threshold; pg_dump renders WITH (autovacuum_vacuum_insert_threshold='1000').
Advisory catalog/dump-only; base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumVacuumInsertThreshold/…Set ~L516
+ render ~L2266), internal/executor/operators_ddl.go (extract/parse ~L1182 + persist
~L1251), operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumVacuumInsertThresholdSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optavit fixture ~L810 + assertion ~L2592),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 204), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; go vet catalog/executor/testport
clean; catalog+executor reloption tests PASS; TestPort_PgDumpConnectionSetup PASS;
pgbench pre-commit smoke on commit.

Next: INT autovacuum-namespace reloptions are now exhausted (vacuum_threshold,
analyze_threshold, vacuum_insert_threshold all landed; scale_factors are REAL).
Slice 205 candidates: (1) bool `vacuum_truncate` reloption (RELOPT_TYPE_BOOL, like
slice 196 autovacuum_enabled). (2) composite types (CREATE TYPE AS) — larger,
pg_class.reltype hardcoded 0 (pg18_user_catalog_rows.go:453).
