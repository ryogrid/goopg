(idle — nothing in flight)

Last landed: DU-002 slice 216 (loop #31) — REAL `vacuum_max_eager_freeze_failure_rate`
storage parameter round-trips through pg_dump.

What happened: PG18 heap reloption (RELOPT_TYPE_REAL, RELOPT_KIND_HEAP|TOAST,
reloptions.c:431), range 0.0–1.0 (NARROWER than the 0.0–100.0 scale-factor reloptions),
default -1 (=unset). Reuses slice-199 autovacuum_vacuum_scale_factor float path. Set flag
guards presence (0.0 valid; parser rejects bare negative as syntax error so 0.0 is the
reachable boundary). Executor strconv.ParseFloat + `!(F>=0 && F<=1)` bounds-check (rejects
NaN/±Inf) → 22023. Persist catalog.Table.VacuumMaxEagerFreezeFailureRate; pg_class virtual
view appends `vacuum_max_eager_freeze_failure_rate=F` after autovacuum_vacuum_max_threshold;
pg_dump renders `WITH (vacuum_max_eager_freeze_failure_rate='F')`. Advisory catalog/dump-only.

Files: internal/catalog/catalog.go (Table field + render),
internal/executor/operators_ddl.go (extract/bounds-check + persist),
internal/executor/operators_fillfactor_reloptions_test.go (NEW
TestVacuumMaxEagerFreezeFailureRateSurfacesInPgClassReloptions + …OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optvefr fixture + assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 216), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; catalog+executor reloption tests PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: remaining heap reloption `vacuum_index_cleanup` (enum auto/on/off; NEW path,
RELOPT_TYPE_ENUM — first enum reloption, needs new parse+render machinery). Then
`toast.*` namespace (BIGGER: real pg_dump reads tc.reloptions from the toast table's
pg_class row, but goopg hardcodes reltoastrelid=0 → needs toast-table pg_class modeling).
Or composite types (CREATE TYPE AS; pg_class.reltype hardcoded 0).
