(idle — nothing in flight)

Last landed: DU-002 slice 212 (loop #26) — integer `autovacuum_multixact_freeze_table_age`
storage parameter round-trips through pg_dump. Completes the multixact freeze-age subfamily
(min/max/table-age).

What happened: tenth INT autovacuum reloption. autovacuum_multixact_freeze_table_age is
RELOPT_TYPE_INT, RELOPT_KIND_HEAP|TOAST, default -1 (unset), range 0–2000000000
(reloptions.c:1895/316). 0 is a valid explicit value (min-age pattern); the Set flag guards
presence. Since the WITH-clause parser refuses negative option values, only overflow
(2000000001) and non-integer are reachable invalid cases (NOT -1 — that fails at parse, so it
was dropped from the OutOfBounds test). Pure reuse of the slice-198/207–211 int path. Persist
catalog.Table.AutovacuumMultixactFreezeTableAge (int); pg_class virtual view appends
`autovacuum_multixact_freeze_table_age=N` after autovacuum_multixact_freeze_max_age; pg_dump
renders `WITH (autovacuum_multixact_freeze_table_age='N')`. Advisory catalog/dump-only;
base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumMultixactFreezeTableAge/…Set + render),
internal/executor/operators_ddl.go (extract/parse + persist),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumMultixactFreezeTableAgeSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optamftaa fixture + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 212), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; catalog+executor reloption tests PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: autovacuum_vacuum_cost_limit (RELOPT_TYPE_INT, 1–10000); then user_catalog_table (bool);
then toast.* namespace; or composite types (CREATE TYPE AS; larger, pg_class.reltype hardcoded 0).
