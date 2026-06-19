(idle — nothing in flight)

Last landed: DU-002 slice 228 (loop #43) — second `RELOPT_KIND_TOAST` *real*
reloption (`toast.autovacuum_vacuum_cost_delay`); five-element TOAST reloptions array.

What happened: `autovacuum_vacuum_cost_delay` is RELOPT_TYPE_REAL, shares
RELOPT_KIND_HEAP|RELOPT_KIND_TOAST (reloptions.c:393, range 0.0–100.0, default -1), so PG
accepts the `toast.` prefix and stores it (no prefix) on the TOAST relation's reloptions.
Added one gather block in operators_ddl.go after the slice-227 autovacuum_vacuum_scale_factor
arm, reusing the parent-table float path (slice 202): strconv.ParseFloat + !(f>=0&&f<=100)
(rejects NaN/±Inf; non-float/oob → 22023), appended as `autovacuum_vacuum_cost_delay=<F>`
via FormatFloat(f,'g',-1,64). catalog UNCHANGED — strings.Join over ToastReloptions now
renders `{autovacuum_enabled=false,vacuum_truncate=false,autovacuum_vacuum_threshold=100,autovacuum_vacuum_scale_factor=2.5,autovacuum_vacuum_cost_delay=10.5}`.
pg_dump re-adds prefix per element → `WITH (toast.autovacuum_enabled='false',
toast.vacuum_truncate='false', toast.autovacuum_vacuum_threshold='100',
toast.autovacuum_vacuum_scale_factor='2.5', toast.autovacuum_vacuum_cost_delay='10.5')`.

Files: internal/executor/operators_ddl.go (float gather), internal/testport/pgdump_connsetup_test.go
(optoast fixture carries all 5 options + updated combined-WITH assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 228), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor+parser+catalog PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: remaining RELOPT_KIND_TOAST int autovacuum options, each a one-line gather reusing
the slice-198 int path: toast.autovacuum_vacuum_cost_limit (INT, range 1–10000),
toast.autovacuum_freeze_min_age (INT, 0–1e9), max_age (INT, 1e5–2e9), table_age (INT,
0–2e9), toast.log_autovacuum_min_duration (INT, allows -1 → lower bound -1 not 0). NOTE:
toast.autovacuum_analyze_* is RELOPT_KIND_HEAP ONLY → PG rejects it; do NOT add. After:
composite types.
