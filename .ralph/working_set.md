(idle — nothing in flight)

Last landed: DU-002 slice 210 (loop #24) — integer `autovacuum_multixact_freeze_min_age`
storage parameter round-trips through pg_dump.

What happened: opened the multixact freeze-age INT subfamily (eighth INT autovacuum
reloption). autovacuum_multixact_freeze_min_age is RELOPT_TYPE_INT, RELOPT_KIND_HEAP|TOAST,
default -1 (unset), range 0–1000000000 (reloptions.c:1891/281). 0 is a valid value, so the
Set flag (not a zero check) records presence. Negatives rejected earlier by the parser as a
syntax error, so the only reachable invalid cases are overflow + non-integer. Pure reuse of
the slice-198/207/208/209 int path. Persist catalog.Table.AutovacuumMultixactFreezeMinAge
(int); pg_class virtual view appends `autovacuum_multixact_freeze_min_age=N` after
autovacuum_freeze_table_age; pg_dump renders `WITH (autovacuum_multixact_freeze_min_age='N')`.
Advisory catalog/dump-only; base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumMultixactFreezeMinAge/…Set + render),
internal/executor/operators_ddl.go (extract/parse + persist),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumMultixactFreezeMinAgeSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optamfma fixture + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 210), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; catalog+executor reloption tests PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: remaining multixact freeze trio (all RELOPT_KIND_HEAP|TOAST):
autovacuum_multixact_freeze_max_age (10000–2000000000),
autovacuum_multixact_freeze_table_age (0–2000000000), then
autovacuum_vacuum_cost_limit (1–10000). Then user_catalog_table (bool); then toast.*
namespace; or composite types (CREATE TYPE AS; larger, pg_class.reltype hardcoded 0).
