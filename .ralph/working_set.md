(idle — nothing in flight)

Last landed: DU-002 slice 229 (loop #44) — second `RELOPT_KIND_TOAST` *integer*
reloption (`toast.autovacuum_vacuum_cost_limit`); six-element TOAST reloptions array.

What happened: `autovacuum_vacuum_cost_limit` is RELOPT_TYPE_INT, shares
RELOPT_KIND_HEAP|RELOPT_KIND_TOAST (reloptions.c:265, range 1–10000, default -1), so PG
accepts the `toast.` prefix and stores it (no prefix) on the TOAST relation's reloptions.
Added one gather block in operators_ddl.go after the slice-228 cost_delay arm, reusing the
parent-table int path (slice 198): strconv.Atoi + 1≤N≤10000 (non-int/oob → 22023), appended
as `autovacuum_vacuum_cost_limit=<N>` via strconv.Itoa. catalog UNCHANGED — strings.Join over
ToastReloptions now renders
`{autovacuum_enabled=false,vacuum_truncate=false,autovacuum_vacuum_threshold=100,autovacuum_vacuum_scale_factor=2.5,autovacuum_vacuum_cost_delay=10.5,autovacuum_vacuum_cost_limit=500}`.
pg_dump re-adds prefix per element → six-element WITH clause ending in
`toast.autovacuum_vacuum_cost_limit='500'`.

Files: internal/executor/operators_ddl.go (int gather), internal/testport/pgdump_connsetup_test.go
(optoast fixture carries all 6 options + updated combined-WITH assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 229), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor+parser+catalog PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: remaining RELOPT_KIND_TOAST int autovacuum-age options, each a one-line gather reusing
the slice-198 int path: toast.autovacuum_freeze_min_age (INT, 0–1e9),
toast.autovacuum_freeze_max_age (INT, 1e5–2e9), toast.autovacuum_freeze_table_age (INT, 0–2e9),
toast.log_autovacuum_min_duration (INT, allows -1 → lower bound -1 not 0). NOTE the multixact
variants (toast.autovacuum_multixact_freeze_min/max_age) also share RELOPT_KIND_TOAST and are
valid candidates too. toast.autovacuum_analyze_* is RELOPT_KIND_HEAP ONLY → PG rejects it; do
NOT add. After: composite types.
