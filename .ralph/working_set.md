(idle — nothing in flight)

Last landed: DU-002 slice 157 (loop #124) — a function carrying STABLE PARALLEL
RESTRICTED round-trips through pg_dump. CLEAN POSITIVE (no production change), closes
the volatility/parallel cell matrix. Slice 149 hit IMMUTABLE (provolatile='i'), slice
150 hit PARALLEL SAFE (proparallel='s'); STABLE (provolatile='s') + PARALLEL RESTRICTED
(proparallel='r') were the last non-default cells dumpFunc emits. Parser already maps
STABLE→'s' (function.go:184) and RESTRICTED→'r' (function.go:253); executor stores both
on catalog.Routine; pg_proc_view emits r.Volatile/r.Parallel verbatim. dumpFunc appends
volatility before parallel (pg_dump.c:13535 then :13583) → `LANGUAGE sql STABLE PARALLEL
RESTRICTED`.

Test fixture: public.add_six(integer) RETURNS integer LANGUAGE sql STABLE PARALLEL
RESTRICTED AS $$ SELECT $1 + 6 $$. Asserts `CREATE FUNCTION public.add_six(integer)
RETURNS integer` + the `LANGUAGE sql STABLE PARALLEL RESTRICTED` / `AS $_$ … $_$;`
fragment ($1 forces $_$ delimiter).

Files: internal/testport/pgdump_connsetup_test.go (fixture after proc_inout ~1600,
assertion after slice-156 ~2177), docs/design/0110-0001-pg-dump-tap-port.md (slice 157
section), .ralph/fix_plan.md (loop #124 PROGRESS).
Verified: gofmt OK; go build ./internal/... OK; TestPort_PgDumpConnectionSetup PASS
(2.16s, not skipped); pgbench smoke runs on commit.

Next direction (slice 158): a multi-statement SQL-procedure/function body (exercise the
body re-render path beyond a single statement), or a TRANSFORM FOR TYPE / WINDOW
function clause (remaining dumpFunc clauses not yet driven).
