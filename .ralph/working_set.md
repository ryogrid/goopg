(idle — nothing in flight)

Last landed: DU-002 slice 156 (loop #122) — procedure with a single INOUT param
through pg_dump. CLEAN POSITIVE (verified empirically), not a divergence. Closes the
argmode-render coverage matrix IN/OUT/INOUT. A lone proargmodes 'b' element sets
showMode (the `m == "o" || m == "b"` detector, expr.go) so pg_dump emits the explicit
`INOUT ` prefix (expr.go:11352, case "b"). Parser maps INOUT→FuncArgInout
(operators_ddl.go:5524); INSERT body accepted by validateSQLFunctionBody.

Test fixture: public.proc_inout(INOUT x integer) LANGUAGE sql AS $$ INSERT INTO
public.foo (id) VALUES (x) $$. Asserts `CREATE PROCEDURE public.proc_inout(INOUT x
integer)` + the `LANGUAGE sql` / `AS $$ … $$;` fragment.

Files: internal/testport/pgdump_connsetup_test.go (fixture after proc_out ~1582,
assertion after slice-155 ~2126), docs/design/0110-0001-pg-dump-tap-port.md (slice 156
section), .ralph/fix_plan.md (loop #122 PROGRESS).
Verified: gofmt OK; go build ./internal/... OK; TestPort_PgDumpConnectionSetup PASS
(2.29s, not skipped); pgbench smoke runs on commit.

Next direction (slice 157): a STABLE or PARALLEL RESTRICTED volatility variant
(provolatile='s' / proparallel='r' cells not yet driven through pg_dump; same plumbing
as slices 149/150). Alternative: a multi-statement SQL-procedure body to exercise the
body re-render path beyond a single INSERT.
