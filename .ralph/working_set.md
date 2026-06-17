(idle — nothing in flight)

Last landed: DU-002 slice 154 (loop #120) — first CREATE PROCEDURE (prokind='p')
round-trip through pg_dump. CLEAN POSITIVE (verified empirically), not a divergence.
Every prior slice dumped only functions; this exercises dumpFunc's PROCEDURE keyword
branch (pg_dump.c:13484) and the no-RETURNS path (:13498). Two procedure-shape details:
(1) procedures always carry an argmode → buildFunctionArguments (expr.go:11329-11340)
emits `IN ` on the named param; (2) body has no `$` → pg_dump's appendStringLiteralDQ
picks bare `$$` (prior bodies had `$N`, forcing `$_$`). Path already wired
(execCreateProcedure sets IsProcedure; pg_proc_view emits prokind='p').

Test fixture: public.ins_foo(a integer) LANGUAGE sql AS $$ INSERT INTO public.foo
(id) VALUES (a) $$. Asserts `CREATE PROCEDURE public.ins_foo(IN a integer)` + the
`LANGUAGE sql` / `AS $$ … $$;` fragment (no stray RETURNS).

Files: internal/testport/pgdump_connsetup_test.go (fixture ~1551, assertion ~2060),
docs/design/0110-0001-pg-dump-tap-port.md (slice 154 section), .ralph/fix_plan.md
(loop #120 PROGRESS).
Verified: gofmt OK; go build ./internal/... OK; TestPort_PgDumpConnectionSetup PASS
(2.29s, not skipped); ralph-state-guard consistent (auto-repaired stale completed
marker); pgbench smoke runs on commit.

Next direction (slice 155): a procedure carrying an OUT or INOUT parameter — exercises
buildFunctionArguments' `OUT `/`INOUT ` argmode render through dumpFunc (the `IN ` path
is now covered). pg_dump renders `CREATE PROCEDURE name(IN a integer, OUT b integer)`.
Verify execCreateProcedure stores argmode 'o'/'b' and pg_get_function_arguments emits
it. Alternative lower-risk: a STABLE or PARALLEL RESTRICTED volatility-marker variant
(clean-positive coverage of the remaining provolatile/proparallel cells).
