(idle — nothing in flight)

Last landed: DU-002 slice 158 (loop #125) — a function with a MULTI-STATEMENT SQL
body (`SELECT 1; SELECT $1 + 7;`) round-trips through pg_dump. CLEAN POSITIVE (no
production change). Every prior function/procedure slice (148–157) carried a
single-statement body; this drives two goopg-side paths nothing had exercised:
(1) the simple-query batch splitter must keep the inner `;` inside the dollar-quoted
body (else CREATE truncates at the first `;` and fails — caught by the runSQLSimple
fatal); and (2) the body is stored as prosrc verbatim and re-emitted by dumpFunc.
validateSQLFunctionBody (operators_ddl.go) parses the whole body, scans every stmt
for $N refs, requires only the LAST stmt to be a scalar SELECT → body accepted. The
body is opaque text to pg_dump (appendStringLiteralDQ only scans for `$`), so no new
dump branch — coverage is on goopg's splitter + verbatim-prosrc round-trip. `$1`
forces the `$_$` delimiter.

Test fixture: public.add_seven(integer) RETURNS integer LANGUAGE sql AS $$ SELECT 1;
SELECT $1 + 7; $$. Asserts `CREATE FUNCTION public.add_seven(integer) RETURNS integer`
+ the `LANGUAGE sql` / `AS $_$ SELECT 1; SELECT $1 + 7; $_$;` fragment.

Files: internal/testport/pgdump_connsetup_test.go (fixture after add_six ~1618,
assertion after slice-157 ~2194), docs/design/0110-0001-pg-dump-tap-port.md (slice 158
section after slice 157), .ralph/fix_plan.md (loop #125 PROGRESS).
Verified: gofmt OK; go build ./internal/... OK; TestPort_PgDumpConnectionSetup PASS
(2.26s, not skipped); pgbench smoke runs on commit.

Next direction (slice 159): a `TRANSFORM FOR TYPE` clause (protrftypes, currently NULL
in pg_proc_view — likely a feature gap, not a clean positive), a non-`sql` LANGUAGE
(e.g. plpgsql) function body, or a function returning a composite/RECORD type. The
function-attribute matrix (volatility/strict/secdef/leakproof/cost/rows/parallel/
prokind/argmodes/multi-stmt body) is now fully covered for the SQL-language path.
