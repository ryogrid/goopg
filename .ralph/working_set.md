(idle — nothing in flight)

Loop #32 landed + committed + pushed (4874f479): CREATE OPERATOR
COMMUTATOR/NEGATOR/RESTRICT/JOIN/MERGES/HASHES clauses + unary (prefix)
operators (M0119-0004, DU-002 slice 407) — extends the slice-406 skeleton
(FUNCTION/LEFTARG/RIGHTARG only) found already implemented-but-uncommitted
at loop start (a prior loop's WIP; working_set.md had gone stale/idle while
the diff sat uncommitted). This loop's actual work: verified the WIP
compiles + tests pass, ran the full gate suite, wrote the design-doc/README
extension + deferral-ledger row, and committed/pushed.

Parser: `parseOperatorRefName` (bare operator symbol or pg_dump's
`OPERATOR(schema.op)` form) for COMMUTATOR/NEGATOR; RESTRICT/JOIN parsed
like FUNCTION; bare MERGES/HASHES flags; LEFTARG now optional (unary).
Catalog: `UserOperator.{CommutatorOID,NegatorOID,RestrictOID,JoinOID,
CanMerge,CanHash}` + `LookupUserOperator`/`LookupUserOperatorByOID`/
`EnsureUserOperatorShell` (PG's OperatorShellMake, pg_operator.c —
forward-reference shell minting a stable OID, reused when the referenced
operator's own CREATE OPERATOR later fills it in). `pg_operator.VirtualRows`
renders the new columns, sets oprkind='l' for unary, skips unfilled shells.
Executor: two-pass COMMUTATOR/NEGATOR resolution (get_other_operator/
OperatorShellMake/OperatorUpd) with self-commutator support, self-negator
rejection, back-patching; OperatorValidateParams-style binary/boolean
attribute gating (42P13); postfix rejected.

Gates run this loop: build clean; parser+catalog+executor suites PASS
(-count=1); gofmt -l flags catalog.go/operators_ddl.go/ast.go but confirmed
via `git show HEAD:<file> | gofmt -l` that ALL THREE already failed gofmt
before this diff (pre-existing go1.25/1.26 drift, not new — see
goopg_gofmt_version_mismatch_no_w memory); TPC-H spotcheck Q12=2/Q13=33
PASS; pgbench smoke PASS via pre-commit hook; make ralph-state-guard
auto-repaired a stale progress.json marker (recurring pattern, harmless).
Design doc 0119-0004-create-operator-roundtrip.md + README index updated
in this commit. New deferral ledger row appended for `ALTER OPERATOR name
(...) SET (RESTRICT=..., JOIN=...)` — the post-hoc attribute-edit form is
still unparsed (only the generic OWNER-TO compat path handles ALTER
OPERATOR today); no fixture currently exercises it.

Next candidates (unchanged backlog):
(1) ALTER OPERATOR ... SET (RESTRICT/JOIN) — this loop's new ledger row;
(2) `regoper`/`regoperator` OID→name resolution — still no observable gap
(no column typed regoper yet); (3) `regprocedure` argument-type-list
disambiguation for overloaded functions; (4) M0119-0005 (pg_waldump server
tier), M0119-0006 (pg_amcheck server tier), M0119-0007 (pg_basebackup
recvlogical, blocked on logical decoding); (5) M0119-0002 (CLOG store swap
Part B) still open; (6) `datacl` (pg_database ACL) stays permanently
deferred (untestable under the connsetup harness); (7) CREATE OPERATOR
CLASS custom-operator pg_dump ordering fixture (`op_class_custom` in
002_pg_dump.pl) — CREATE OPERATOR CLASS already exists separately; not
investigated this loop whether it already passes.
