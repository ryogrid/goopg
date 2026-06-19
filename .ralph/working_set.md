(idle — nothing in flight)

Last landed: DU-002 slice 197 (loop #10) — integer storage parameter with a
non-zero minimum (`toast_tuple_target`, 128–8160) round-trips through pg_dump.

What happened: slices 54/195/196 made fillfactor, parallel_workers,
autovacuum_enabled round-trip. toast_tuple_target is the next-most-common heap
reloption and exercises the integer variant whose min is 128 (PG's `128,
TOAST_TUPLE_TARGET_MAIN`=8160 on 8 KB page). Because min is 128, zero
unambiguously means "unset" → reuse fillfactor's plain zero-check (no separate
Set flag), unlike parallel_workers whose 0 is a real value. goopg validated the
lowercase WITH key but never extracted it, so `WITH (toast_tuple_target=256)`
silently dropped it. Fix: extract/bounds-check (128–8160; out-of-range/non-int →
22023) on base-table CREATE path; persist catalog.Table.ToastTupleTarget;
pg_class virtual view appends `toast_tuple_target=N` after autovacuum_enabled;
pg_dump renders `WITH (toast_tuple_target='256')`. Advisory catalog/dump-only;
base-table-only.

Files: internal/catalog/catalog.go (Table.ToastTupleTarget field ~L419 + render
~L2140), internal/executor/operators_ddl.go (extract/parse ~L1013 + persist
~L1090), operators_fillfactor_reloptions_test.go (NEW
TestToastTupleTargetSurfacesInPgClassReloptions + ...OutOfBoundsRejected),
internal/testport/pgdump_connsetup_test.go (optt fixture ~L712 + assertion
~L2390), docs/design/0110-0001-pg-dump-tap-port.md (Slice 197), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; go vet testport clean;
catalog+executor reloption tests PASS; TestPort_PgDumpConnectionSetup PASS;
pgbench pre-commit smoke on commit.

Next (slice 198 candidates): (1) real-typed reloption
`autovacuum_vacuum_scale_factor` (real 0–100, needs float parse + 0-as-valid →
new Set-flag-style path), or `autovacuum_vacuum_threshold` (int 0+, needs Set
flag like parallel_workers). (2) composite types (CREATE TYPE AS) — larger,
pg_class.reltype hardcoded 0 (pg18_user_catalog_rows.go:453). (3) per-column
attfdwoptions (foreign-table only).
