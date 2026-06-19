(idle — nothing in flight)

Last landed: DU-002 slice 227 (loop #42) — first `RELOPT_KIND_TOAST` *real*
reloption (`toast.autovacuum_vacuum_scale_factor`); four-element TOAST reloptions array.

What happened: `autovacuum_vacuum_scale_factor` is RELOPT_TYPE_REAL, shares
RELOPT_KIND_HEAP|RELOPT_KIND_TOAST (reloptions.c:404, range 0.0–100.0, default -1), so PG
accepts the `toast.` prefix and stores it (no prefix) on the TOAST relation's reloptions.
Added one gather block in operators_ddl.go after the slice-226 autovacuum_vacuum_threshold
arm, reusing the parent-table float path (slice 199): strconv.ParseFloat + !(f>=0&&f<=100)
(rejects NaN/±Inf; non-float/oob → 22023), appended as `autovacuum_vacuum_scale_factor=<F>`
via FormatFloat(f,'g',-1,64). catalog UNCHANGED — strings.Join over ToastReloptions now
renders `{autovacuum_enabled=false,vacuum_truncate=false,autovacuum_vacuum_threshold=100,autovacuum_vacuum_scale_factor=2.5}`.
pg_dump re-adds prefix per element → `WITH (toast.autovacuum_enabled='false',
toast.vacuum_truncate='false', toast.autovacuum_vacuum_threshold='100', toast.autovacuum_vacuum_scale_factor='2.5')`.

Files: internal/executor/operators_ddl.go (float gather), internal/testport/pgdump_connsetup_test.go
(optoast fixture carries all 4 options + updated combined-WITH assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 227), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor+parser+catalog PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: remaining RELOPT_KIND_TOAST int/real autovacuum options, each a one-line gather
reusing established int/float paths: toast.autovacuum_vacuum_cost_delay (REAL, slice-199
float path) / cost_limit (INT, slice-198 int path), toast.autovacuum_freeze_min_age/
max_age/table_age (INT), toast.log_autovacuum_min_duration (INT, allows -1 — needs lower
bound -1 not 0). NOTE: toast.autovacuum_analyze_* is RELOPT_KIND_HEAP ONLY → PG rejects
it; do NOT add. After: composite types.
