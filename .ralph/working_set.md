(idle — nothing in flight)

Last landed: DU-002 slice 209 (loop #23) — integer `autovacuum_freeze_table_age`
storage parameter round-trips through pg_dump.

What happened: continued the freeze-age INT subfamily (seventh INT autovacuum
reloption). autovacuum_freeze_table_age is RELOPT_TYPE_INT, RELOPT_KIND_HEAP|TOAST,
default -1 (unset), range 0–2000000000 (reloptions.c:1889/312). 0 is a valid value,
so the Set flag (not a zero check) records presence. Negatives rejected earlier by
the parser as a syntax error, so the only reachable invalid cases are overflow +
non-integer. Pure reuse of the slice-198/207/208 int path. Persist
catalog.Table.AutovacuumFreezeTableAge (int); pg_class virtual view appends
`autovacuum_freeze_table_age=N` after autovacuum_freeze_max_age; pg_dump renders
`WITH (autovacuum_freeze_table_age='N')`. Advisory catalog/dump-only; base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumFreezeTableAge/…Set + render),
internal/executor/operators_ddl.go (extract/parse + persist),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumFreezeTableAgeSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optafta fixture + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 209), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; catalog+executor reloption tests PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: remaining freeze INT reloptions (all RELOPT_KIND_HEAP|TOAST): the multixact
trio autovacuum_multixact_freeze_min_age (0–1000000000),
autovacuum_multixact_freeze_max_age (10000–2000000000),
autovacuum_multixact_freeze_table_age (0–2000000000), then
autovacuum_vacuum_cost_limit (1–10000). Then user_catalog_table (bool); then toast.*
namespace; or composite types (CREATE TYPE AS; larger, pg_class.reltype hardcoded 0).
