(idle — nothing in flight)

Last landed: DU-002 slice 231 (loop #46) — second `RELOPT_KIND_TOAST` autovacuum-age
*integer* reloption (`toast.autovacuum_freeze_max_age`); eight-element TOAST reloptions array.

What happened: `autovacuum_freeze_max_age` is RELOPT_TYPE_INT, shares
RELOPT_KIND_HEAP|RELOPT_KIND_TOAST (reloptions.c:1887/290, range 100000–2000000000, default -1), so PG
accepts the `toast.` prefix and stores it (no prefix) on the TOAST relation's reloptions. Added one
gather block in operators_ddl.go after the slice-230 freeze_min_age arm, mirroring the parent-table int
arm (slice 208): strconv.Atoi + 100000≤N≤2e9 (non-int/oob → 22023; the 1e5 floor rejects an explicit
-1), appended as `autovacuum_freeze_max_age=<N>` via strconv.Itoa. catalog UNCHANGED — strings.Join over
ToastReloptions now renders an eight-element array ending in `autovacuum_freeze_max_age=500000000`.
pg_dump re-adds prefix per element → eight-element WITH clause ending in
`toast.autovacuum_freeze_max_age='500000000'`.

Files: internal/executor/operators_ddl.go (int gather), internal/testport/pgdump_connsetup_test.go
(optoast fixture carries all 8 options + updated combined-WITH assertion),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 231), fix_plan.md.

Gates: gofmt OK; go build ./internal/... clean; executor+parser+catalog PASS;
TestPort_PgDumpConnectionSetup PASS; pgbench pre-commit smoke on commit.

Next: remaining RELOPT_KIND_TOAST int autovacuum-age options, each a one-line gather reusing the
slice-198/207/208 int path: toast.autovacuum_freeze_table_age (INT, 0–2000000000, 0 valid),
toast.log_autovacuum_min_duration (INT, lower bound -1 not 0 → allow -1). The multixact variants
(toast.autovacuum_multixact_freeze_min_age INT 0–1000000000, …max_age INT 10000–2000000000,
…table_age INT 0–2000000000) also share RELOPT_KIND_TOAST and are valid candidates.
toast.autovacuum_analyze_* is RELOPT_KIND_HEAP ONLY → PG rejects it; do NOT add. After: composite types.
