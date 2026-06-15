Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 43 COMPLETE
and (to be) pushed. NOTHING in flight; next loop starts on slice 44
(add pg_get_function_sqlbody builtin).

=== DONE (loop #66) — DU-002 slice 43 ===
Added the `pg_get_function_identity_arguments(oid)` executor dispatch case.
Root cause: seed pg_proc already registered it (OID 2232,
internal/initdb/pg_proc_seed_data.go) and its siblings
pg_get_function_arguments/pg_get_function_result already had cases, but
identity_arguments had NO case in the big func switch in
internal/executor/expr.go → call raised 42883 "function ... does not exist".
Fix: added a case right before `pg_get_function_result` that looks up the
routine by OID (rs.LookupByOID) and returns buildFunctionArguments(r).
Why identical to pg_get_function_arguments: upstream ruleutils.c
print_function_arguments differs only by print_defaults (true vs false);
goopg's buildFunctionArguments never emits DEFAULT clauses, so identity ==
full arg list. Documented the invariant inline.
Files: internal/executor/expr.go (new case),
internal/executor/pg_get_function_identity_arguments_test.go (NEW:
TestPgGetFunctionIdentityArguments + ...OutMode),
internal/testport/pgdump_connsetup_test.go (header slice 43 + next blocker),
docs/design/0110-0001-pg-dump-tap-port.md (slice 43 entry).
Gates: gofmt/build clean; executor pkg PASS (1.4s);
TestPort_PgDumpConnectionSetup PASS (advanced past the call).
tpch-spotcheck N/A (catalog builtin addition; no executor row-path/codec change).

=== NEXT STEP — DU-002 slice 44 (pg_get_function_sqlbody) ===
pg_dump now fails in dumpFunc with `function pg_get_function_sqlbody does not
exist` (EXECUTE dumpFunc('1654')). dumpFunc projects pg_get_function_sqlbody(
p.oid) — PG14+, the deparsed SQL-standard body of `LANGUAGE sql ... BEGIN
ATOMIC` functions; NULL for non-SQL / non-atomic routines. Add the builtin in
internal/executor/expr.go (mirror the pg_get_functiondef/pg_get_function_*
cases nearby ~line 6833-6895). Likely returns NULL for every goopg routine
(no BEGIN ATOMIC support). Check seed pg_proc for its OID (probably already
registered like 2232 was). Then RUN
`go test -count=1 -v -run TestPort_PgDumpConnectionSetup ./internal/testport/`
to confirm + find the next blocker.

ORTHOGONAL PRE-EXISTING (track separately): plpgsql user functions can't be
dumped (plpgsql not in pg_language → prolang=0 → dumpFunc join still 0 rows).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
