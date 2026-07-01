(idle — nothing in flight)

Loop #30 landed + committed (dc18de99): verified and closed out the
backgrounded pg_dump slice-continuation agent's work — CREATE OPERATOR
round-trip through pg_dump (M0119-0004, DU-002 slice 406, design
`0119-0004-create-operator-roundtrip.md`). CREATE OPERATOR was previously a
name-registration-only compat no-op; parser now captures FUNCTION/PROCEDURE
alongside LEFTARG/RIGHTARG (`CompatNoopStmt.OpFuncName`); catalog gained a
`UserOperator` registry (`RegisterUserOperator`/`DropUserOperator`/
`ListUserOperators`) feeding a populated `pg_operator.VirtualRows` so
pg_dump's getOperators/dumpOpr re-emit `CREATE OPERATOR public.~~ (FUNCTION
= int4eq, LEFTARG = integer, RIGHTARG = integer);` + the trailing `ALTER
OPERATOR ... OWNER TO` line byte-identical to real PG 18.3. New builtin
`int4eq` (OID 65) curated in `builtinProcsByName`. Gates run this loop:
build+vet clean; internal/catalog+parser+executor suites PASS;
TestPort_PgDumpConnectionSetup PASS; gofmt -d confirmed zero drift in the
new code (pre-existing go1.25/1.26 mismatch noise only, untouched); TPC-H
spotcheck Q12=2/Q13=33 PASS; pgbench smoke PASS via pre-commit hook.
Design doc + README index entry added (0119-0004bk). Deferral ledger row
appended for COMMUTATOR/NEGATOR/RESTRICT/JOIN/MERGES/HASHES clauses + unary
operator forms (still unparsed). fix_plan.md intentionally NOT edited
(driver-churn — record progress in ledger + working_set only, per memory).
`make ralph-state-guard` needed one auto-repair (progress.json's stale
"completed" marker reconciled to in_progress) — now consistent.

Next candidates (unchanged backlog, from prior loops' notes):
(1) CREATE OPERATOR COMMUTATOR/NEGATOR/RESTRICT/JOIN/MERGES/HASHES clauses +
unary operators (this loop's new ledger row) — needs a two-pass forward-
reference patch-up analogous to PG's own AlterOperator; (2) `regoper`/
`regoperator` OID→name resolution — no column typed `regoper` yet, still no
observable gap; (3) `regprocedure` argument-type-list disambiguation for
overloaded functions; (4) M0119-0005 (pg_waldump server tier), M0119-0006
(pg_amcheck server tier), M0119-0007 (pg_basebackup recvlogical, blocked on
logical decoding); (5) M0119-0002 (CLOG store swap Part B) still open;
(6) `datacl` (pg_database ACL) stays permanently deferred (untestable under
the connsetup harness).
