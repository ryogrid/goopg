(idle — nothing in flight)

Last landed: DU-002 slice 208 (loop #22) — integer `autovacuum_freeze_max_age`
storage parameter round-trips through pg_dump.

What happened: continued the freeze-age INT subfamily. autovacuum_freeze_max_age is
RELOPT_TYPE_INT, RELOPT_KIND_HEAP|TOAST, default -1 (unset), range 100000–2000000000
(reloptions.c:1887/290). Range MIN is 100000, so explicit -1 rejected as out-of-range;
Set flag records presence. Pure reuse of the slice-198/207 int path. Persist
catalog.Table.AutovacuumFreezeMaxAge (int); pg_class virtual view appends
`autovacuum_freeze_max_age=N` after autovacuum_freeze_min_age; pg_dump renders
`WITH (autovacuum_freeze_max_age='N')`. Advisory catalog/dump-only; base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumFreezeMaxAge/…Set + render),
internal/executor/operators_ddl.go (extract/parse + persist),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumFreezeMaxAgeSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optafmx fixture + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 208), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; catalog+executor reloption tests PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: remaining freeze-age INT reloptions (all RELOPT_KIND_HEAP|TOAST):
autovacuum_freeze_table_age (0–2000000000), autovacuum_multixact_freeze_min_age
(0–1000000000), autovacuum_multixact_freeze_max_age (10000–2000000000),
autovacuum_multixact_freeze_table_age (0–2000000000), autovacuum_vacuum_cost_limit
(1–10000). Then user_catalog_table (bool); then toast.* namespace; or composite
types (CREATE TYPE AS; larger, pg_class.reltype hardcoded 0).
