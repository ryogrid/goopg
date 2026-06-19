(idle — nothing in flight)

Last landed: DU-002 slice 226 (loop #41) — first `RELOPT_KIND_TOAST` *integer*
reloption (`toast.autovacuum_vacuum_threshold`); three-element TOAST reloptions array.

What happened: `autovacuum_vacuum_threshold` shares RELOPT_KIND_HEAP|RELOPT_KIND_TOAST
(reloptions.c:229, range 0–INT_MAX, default -1), so PG accepts the `toast.` prefix and
stores it (no prefix) on the TOAST relation's reloptions. Added one gather block in
operators_ddl.go after the slice-225 vacuum_truncate arm, reusing the parent-table
integer path (slice 198): strconv.Atoi + 0..2147483647 bounds (non-int/oob → 22023),
appended as `autovacuum_vacuum_threshold=<N>`. catalog UNCHANGED — strings.Join over
ToastReloptions now renders `{autovacuum_enabled=false,vacuum_truncate=false,autovacuum_vacuum_threshold=100}`.
pg_dump re-adds prefix per element → `WITH (toast.autovacuum_enabled='false',
toast.vacuum_truncate='false', toast.autovacuum_vacuum_threshold='100')`.

Files: internal/executor/operators_ddl.go (int gather), internal/testport/pgdump_connsetup_test.go
(optoast fixture carries all 3 options + updated combined-WITH assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 226), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor+parser+catalog PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: remaining RELOPT_KIND_TOAST float/int autovacuum options, each a one-line gather
reusing established int/float paths: toast.autovacuum_vacuum_scale_factor (slice-199
float path), toast.autovacuum_vacuum_cost_delay (REAL) / cost_limit (INT),
toast.autovacuum_freeze_min_age/max_age/table_age (INT), toast.log_autovacuum_min_duration
(INT, allows -1). NOTE: toast.autovacuum_analyze_* is RELOPT_KIND_HEAP ONLY → PG rejects
it; do NOT add. After: composite types.
