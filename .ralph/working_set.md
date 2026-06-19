(idle — nothing in flight)

Last landed: DU-002 slice 217 (loop #32) — ENUM `vacuum_index_cleanup` storage
parameter round-trips through pg_dump. goopg's FIRST enum reloption.

What happened: PG18 heap reloption (RELOPT_TYPE_ENUM, RELOPT_KIND_HEAP|TOAST,
reloptions.c:519), members auto/on/off/true/false/yes/no/1/0 (case-insensitive;
StdRdOptIndexCleanupValues, reloptions.c:487), default auto. Executor validates the
lowercased value against that set, else 22023. KEY: value stored VERBATIM (trimmed) on
catalog.Table.VacuumIndexCleanup (string) — NO re-canonicalization, so alias `yes`
round-trips as `=yes` (not `=true`/`=on`), matching PG's pg_class.reloptions which keeps
literal text. Set flag guards presence (auto is legal explicit, no sentinel). pg_class
virtual view appends `vacuum_index_cleanup=V` after vacuum_max_eager_freeze_failure_rate;
pg_dump renders `WITH (vacuum_index_cleanup='V')`. Advisory catalog/dump-only.
NB: goopg parser lowercases bareword option values (OFF→off), so the guarded behavior is
absence-of-alias-normalization, not case.

Files: internal/catalog/catalog.go (Table field + render),
internal/executor/operators_ddl.go (extract/validate + persist),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestVacuumIndexCleanupSurfacesInPgClassReloptions + …InvalidRejected),
internal/testport/pgdump_connsetup_test.go (optvic fixture + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 217), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; catalog+executor reloption tests PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: `toast.*` namespace (BIGGER: real pg_dump reads tc.reloptions from the toast table's
pg_class row, but goopg hardcodes reltoastrelid=0 → needs toast-table pg_class modeling).
Or composite types (CREATE TYPE AS; pg_class.reltype hardcoded 0). The simple per-table
scalar reloptions are now largely exhausted — remaining heap reloptions are mostly enum
duplicates of vacuum_index_cleanup or need new on-disk modeling.
