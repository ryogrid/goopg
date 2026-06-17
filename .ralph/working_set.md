(idle — nothing in flight)

Last landed: DU-002 slice 155 (loop #121) — procedure with a mixed IN/OUT signature
through pg_dump. CLEAN POSITIVE (verified empirically), not a divergence. First slice
to drive a non-IN argmode (OUT, proargmodes 'o') through buildFunctionArguments'
`OUT ` branch (expr.go) → dumpFunc. Slice 154's ins_foo was IN-only; functions with
all-IN params suppress the mode prefix. Parser maps OUT→'o' (FuncArgOut,
operators_ddl.go:5522); INSERT body always accepted by validateSQLFunctionBody.

Test fixture: public.proc_out(a integer, OUT b integer) LANGUAGE sql AS $$ INSERT INTO
public.foo (id) VALUES (a) $$. Asserts `CREATE PROCEDURE public.proc_out(IN a integer,
OUT b integer)` + the `LANGUAGE sql` / `AS $$ … $$;` fragment.

Files: internal/testport/pgdump_connsetup_test.go (fixture after ins_foo ~1566,
assertion after slice-154 ~2091), docs/design/0110-0001-pg-dump-tap-port.md (slice 155
section), .ralph/fix_plan.md (loop #121 PROGRESS).
Verified: gofmt OK; go build ./internal/... OK; TestPort_PgDumpConnectionSetup PASS
(2.11s, not skipped); pgbench smoke runs on commit.

Next direction (slice 156): a procedure carrying an INOUT param — exercises the last
unrendered argmode, 'b' → `INOUT ` (buildFunctionArguments expr.go:11352). pg_dump
renders `CREATE PROCEDURE name(INOUT x integer)`. Alternative lower-risk: a STABLE or
PARALLEL RESTRICTED volatility variant (provolatile='s' / proparallel='r' cells not
yet hit through pg_dump; same plumbing as slices 149/150).
