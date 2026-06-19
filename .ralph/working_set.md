(idle — nothing in flight)

Last landed: DU-002 slice 213 (loop #27) — integer `autovacuum_vacuum_cost_limit`
storage parameter round-trips through pg_dump. Eleventh INT autovacuum reloption.

What happened: autovacuum_vacuum_cost_limit is RELOPT_TYPE_INT, RELOPT_KIND_HEAP|TOAST,
default -1 (unset), range 1–10000 (reloptions.c:1883/268). Unlike the freeze-age options
the lower bound is 1, so 0 is below range and rejected — 0, overflow (10001) and
non-integer are the reachable invalid cases. The Set flag guards presence. Pure reuse of
the slice-198/207–212 int path. Persist catalog.Table.AutovacuumVacuumCostLimit (int);
pg_class virtual view appends `autovacuum_vacuum_cost_limit=N` after
autovacuum_multixact_freeze_table_age; pg_dump renders
`WITH (autovacuum_vacuum_cost_limit='N')`. Advisory catalog/dump-only; base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumVacuumCostLimit/…Set + render),
internal/executor/operators_ddl.go (extract/parse + persist),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumVacuumCostLimitSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optavcl fixture + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 213), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; catalog+executor reloption tests PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: user_catalog_table (bool); then toast.* namespace; or composite types
(CREATE TYPE AS; larger, pg_class.reltype hardcoded 0).
