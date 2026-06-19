(idle — nothing in flight)

Last landed: DU-002 slice 211 (loop #25) — integer `autovacuum_multixact_freeze_max_age`
storage parameter round-trips through pg_dump.

What happened: continued the multixact freeze-age INT subfamily (ninth INT autovacuum
reloption). autovacuum_multixact_freeze_max_age is RELOPT_TYPE_INT, RELOPT_KIND_HEAP|TOAST,
default -1 (unset), range 10000–2000000000 (reloptions.c:1893/299). Unlike the min/table-age
options the lower bound is 10000 (not 0), so a below-min positive value (9999) is a reachable
invalid case (tested) alongside overflow + non-integer; the Set flag still guards presence.
Pure reuse of the slice-198/207/208/209/210 int path. Persist
catalog.Table.AutovacuumMultixactFreezeMaxAge (int); pg_class virtual view appends
`autovacuum_multixact_freeze_max_age=N` after autovacuum_multixact_freeze_min_age; pg_dump
renders `WITH (autovacuum_multixact_freeze_max_age='N')`. Advisory catalog/dump-only;
base-table-only.

Files: internal/catalog/catalog.go (Table.AutovacuumMultixactFreezeMaxAge/…Set + render),
internal/executor/operators_ddl.go (extract/parse + persist),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestAutovacuumMultixactFreezeMaxAgeSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optamfmaxa fixture + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 211), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; catalog+executor reloption tests PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: remaining multixact freeze-age member autovacuum_multixact_freeze_table_age
(RELOPT_TYPE_INT, 0–2000000000, reloptions.c:316/1895), then
autovacuum_vacuum_cost_limit (1–10000); then user_catalog_table (bool); then toast.*
namespace; or composite types (CREATE TYPE AS; larger, pg_class.reltype hardcoded 0).
