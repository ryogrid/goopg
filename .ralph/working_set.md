(idle — nothing in flight)

Last landed: DU-002 slice 205 (loop #18) — boolean `vacuum_truncate` storage
parameter round-trips through pg_dump.

What happened: pure reuse of slice 196's (autovacuum_enabled) boolean path.
RELOPT_TYPE_BOOL, RELOPT_KIND_HEAP|TOAST, default true (reloptions.c:152/1915).
Executor parses via parseReloptionBool (PG parse_bool prefix-matching; non-bool →
22023); separate VacuumTruncateSet flag so explicit `vacuum_truncate=false` round-
trips; persist catalog.Table.VacuumTruncate (bool); pg_class virtual view appends
`vacuum_truncate=true|false` after autovacuum_vacuum_insert_threshold; pg_dump
renders WITH (vacuum_truncate='false'). Advisory catalog/dump-only; base-table-only.

Files: internal/catalog/catalog.go (Table.VacuumTruncate/…Set ~L531 + render ~L2284),
internal/executor/operators_ddl.go (extract/parse ~L1207 + persist ~L1279),
operators_fillfactor_reloptions_test.go (NEW
TestVacuumTruncateSurfacesInPgClassReloptions + …InvalidValueRejected),
internal/testport/pgdump_connsetup_test.go (optvt fixture ~L823 + assertion ~L2620),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 205), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; go vet catalog/executor/testport
clean; catalog+executor reloption tests PASS; TestPort_PgDumpConnectionSetup PASS;
pgbench pre-commit smoke on commit.

Next: more reloption families remain. Candidates: (1) int `log_autovacuum_min_duration`
(RELOPT_TYPE_INT, range -1–INT_MAX; reuse slice-198 int path). (2) the `toast.*`
namespace reloptions. (3) composite types (CREATE TYPE AS) — larger,
pg_class.reltype hardcoded 0 (pg18_user_catalog_rows.go:453).
