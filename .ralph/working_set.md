(idle — nothing in flight)

Last landed: DU-002 slice 207 (loop #21) — integer `autovacuum_freeze_min_age`
storage parameter round-trips through pg_dump.

What happened: loop-#20 working_set's "Next: toast_tuple_target" was STALE —
toast_tuple_target already landed in slice 197 (caught by a DuplicateDecl on the
catalog field). Pivoted to the freeze-age subfamily. autovacuum_freeze_min_age is
RELOPT_TYPE_INT, RELOPT_KIND_HEAP|TOAST, default -1 (unset), range 0–1000000000
(reloptions.c:1885/272). Note: range MIN is 0, so explicit -1 is rejected as
out-of-range; but 0 is valid so still uses a separate Set flag. Pure reuse of the
slice-198 int path. Persist catalog.Table.AutovacuumFreezeMinAge (int); pg_class
virtual view appends `autovacuum_freeze_min_age=N` after log_autovacuum_min_duration;
pg_dump renders `WITH (autovacuum_freeze_min_age='N')`. Advisory catalog/dump-only;
base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumFreezeMinAge/…Set + render),
internal/executor/operators_ddl.go (extract/parse + persist),
operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumFreezeMinAgeSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optafma fixture + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 207), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; go vet catalog/executor clean;
catalog+executor reloption tests PASS; TestPort_PgDumpConnectionSetup PASS;
pgbench pre-commit smoke on commit.

Next: more freeze-age INT reloptions remain (all RELOPT_KIND_HEAP|TOAST,
reloptions.c:~263–315): autovacuum_freeze_max_age (range 100000–2000000000),
autovacuum_freeze_table_age (0–2000000000), autovacuum_multixact_freeze_min_age
(0–1000000000), autovacuum_multixact_freeze_max_age (10000–2000000000),
autovacuum_multixact_freeze_table_age (0–2000000000), autovacuum_vacuum_cost_limit
(1–10000). Then user_catalog_table (bool); then toast.* namespace; or composite
types (CREATE TYPE AS; larger, pg_class.reltype hardcoded 0).
