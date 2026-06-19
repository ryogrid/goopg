(idle — nothing in flight)

Last landed: DU-002 slice 230 (loop #45) — first `RELOPT_KIND_TOAST` autovacuum-age
*integer* reloption (`toast.autovacuum_freeze_min_age`); seven-element TOAST reloptions array.

What happened: `autovacuum_freeze_min_age` is RELOPT_TYPE_INT, shares
RELOPT_KIND_HEAP|RELOPT_KIND_TOAST (reloptions.c:1885/274, range 0–1000000000, default -1), so PG
accepts the `toast.` prefix and stores it (no prefix) on the TOAST relation's reloptions. Added one
gather block in operators_ddl.go after the slice-229 cost_limit arm, reusing the parent-table int
path (slice 207): strconv.Atoi + 0≤N≤1e9 (non-int/oob → 22023; 0 valid), appended as
`autovacuum_freeze_min_age=<N>` via strconv.Itoa. catalog UNCHANGED — strings.Join over
ToastReloptions now renders a seven-element array ending in `autovacuum_freeze_min_age=200000000`.
pg_dump re-adds prefix per element → seven-element WITH clause ending in
`toast.autovacuum_freeze_min_age='200000000'`.

Files: internal/executor/operators_ddl.go (int gather), internal/testport/pgdump_connsetup_test.go
(optoast fixture carries all 7 options + updated combined-WITH assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 230), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor+parser+catalog PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: remaining RELOPT_KIND_TOAST int autovacuum-age options, each a one-line gather reusing the
slice-198/207 int path: toast.autovacuum_freeze_max_age (INT, 100000–2000000000, min is 1e5 so an
explicit -1 is rejected), toast.autovacuum_freeze_table_age (INT, 0–2000000000),
toast.log_autovacuum_min_duration (INT, lower bound -1 not 0 → allow -1). The multixact variants
(toast.autovacuum_multixact_freeze_min/max_age INT, toast.autovacuum_multixact_freeze_table_age INT)
also share RELOPT_KIND_TOAST and are valid candidates. toast.autovacuum_analyze_* is
RELOPT_KIND_HEAP ONLY → PG rejects it; do NOT add. After: composite types.
