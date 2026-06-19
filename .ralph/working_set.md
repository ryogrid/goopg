(idle — nothing in flight)

Last landed: DU-002 slice 215 (loop #30) — integer `autovacuum_vacuum_max_threshold`
storage parameter round-trips through pg_dump.

What happened: PG18 heap reloption (RELOPT_TYPE_INT, RELOPT_KIND_HEAP|TOAST,
reloptions.c:236), range -1–INT_MAX, default -2 (=unset). Reuses slice-204
autovacuum_vacuum_insert_threshold integer path. Set flag guards presence (-1/0
valid; parser rejects bare negative as syntax error so 0 is reachable boundary).
Executor strconv.Atoi + bounds-check -1≤N≤INT_MAX → 22023 on bad. Persist
catalog.Table.AutovacuumVacuumMaxThreshold; pg_class virtual view appends
`autovacuum_vacuum_max_threshold=N` after user_catalog_table; pg_dump renders
`WITH (autovacuum_vacuum_max_threshold='N')`. Advisory catalog/dump-only.

Files: internal/catalog/catalog.go (Table field + render),
internal/executor/operators_ddl.go (extract/bounds-check + persist),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumVacuumMaxThresholdSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optavmt fixture + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 215), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; catalog+executor reloption tests PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: remaining heap reloptions — `vacuum_index_cleanup` (enum auto/on/off; NEW path,
RELOPT_TYPE_ENUM) and `vacuum_max_eager_freeze_failure_rate` (REAL, PG18). Then
`toast.*` namespace (BIGGER: real pg_dump reads tc.reloptions from the toast table's
pg_class row, but goopg hardcodes reltoastrelid=0 → needs toast-table pg_class
modeling). Or composite types (CREATE TYPE AS; pg_class.reltype hardcoded 0).
