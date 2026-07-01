Task: M0119-0004 DU-002 slice 411 — pg_amop/pg_amproc + synthetic pg_depend
member store for CREATE OPERATOR CLASS's own AS-list OPERATOR/FUNCTION
entries (loop #35/#36's shared backlog item #1). IMPLEMENTATION COMPLETE,
verified against live PG 18.3, tests passing — only the TPC-H spotcheck gate
was still running in the background when this loop's context ended.

Files: internal/parser/ast.go (CreateOpClassStmt.Members + new
OpClassMember struct); internal/parser/ddl.go (parseCreateOpClassTail
OPERATOR/FUNCTION branches capture full member info via
parseOperatorRefName + parseTypeNameAfterCast, was skip-only before);
internal/catalog/catalog.go (new AmOpMember/AmProcMember +
Register/List*; pg_amop/pg_amproc VirtualRows now render them;
dependVirtualRows appends the 2 pg_depend rows/member; DropUserOperatorClass
cascades member cleanup; new LookupUserOperatorByName; new
RegoperatorName; added btint4cmp to builtinProcsByName; fixed ::regtype
InvalidOid→"0" bug (should be "-") in both branches);
internal/executor/operators_ddl.go (execCreateOpClass calls new
registerOpClassMembers/resolveOpClassOperator/resolveOpClassFunction);
internal/executor/expr.go (new regoper/regoperator CastExpr branch calling
RegoperatorName; regtype InvalidOid fix). Tests added in
internal/executor/create_operator_test.go (3 new tests, all PASS).

Key symbols: catalog.AmOpMember/AmProcMember, RegisterAmOpMember/
RegisterAmProcMember, RegoperatorName, LookupUserOperatorByName;
executor.resolveOpClassOperator/resolveOpClassFunction/
registerOpClassMembers; parser.OpClassMember.

Verification done this loop: go build ./... clean; go vet clean;
internal/catalog+executor+parser+server+planner+initdb suites PASS;
TestPort_PgDumpConnectionSetup PASS; gofmt clean on all touched files
(pre-existing go1.25/1.26 drift on catalog.go/expr.go/operators_ddl.go/
ast.go only, confirmed via git stash — same as every prior loop);
live-diff against a freshly built PG 18.3 (postgres/local_install):
`CREATE OPERATOR public.~=~ (...); CREATE OPERATOR FAMILY public.op_family
USING btree; CREATE OPERATOR CLASS public.op_class FOR TYPE int4 USING
btree FAMILY public.op_family AS OPERATOR 1 ~=~ (int4, int4), FUNCTION 1
btint4cmp(int4, int4);` dumps byte-identical on both engines EXCEPT one
newly-confirmed, ledgered gap: PG schema-qualifies the OPERATOR entry's
operator name (`public.~=~(integer,integer)`), goopg does not
(`~=~(integer,integer)`) — root cause: pg_dump's connection always runs
search_path='' so format_operator/format_procedure ALWAYS force-qualify;
goopg's RegoperatorName/RegprocedureName never do. Ledgered as a resume
point (simplest fix: unconditionally prepend schema. to both renderers,
no visibility-check logic needed).

Design docs updated: docs/design/0119-0004-create-operator-roundtrip.md
(new "Loop #37" addendum) + README.md row 833 addendum. New deferral
ledger row (status `-`, slice 411) covering: (a) FOR ORDER BY sort-family
parsed-and-discarded, (b) builtin-operator-catalog gap still blocks
resolving builtin OPERATOR/FUNCTION references (pre-existing, reconfirmed),
(c) NEW regoperator/regprocedure schema-qualification gap.

Next step (if TPC-H spotcheck gate — still running when this loop ended —
came back PASS): commit + push. Commit message should cover: parser
Members capture, catalog member store + pg_depend wiring, executor
resolution, the 2 adjacent regtype/regoperator bug fixes, new tests, design
doc + ledger updates. If the gate FAILED: investigate before committing
(re-run `scripts/tpch-spotcheck.sh` fresh — expected Q12=2/Q13=33).

Next candidates after this lands (backlog, carried from loop #36, updated):
(1) Builtin operator catalog (pg_operator rows for builtins, keyed by
name+left/right type) — large, standalone feature; unlocks regoper/
regoperator resolution for builtins AND opclass-member OPERATOR resolution
for realistic (non-custom-operator) fixtures. (2) regoperator/regprocedure
schema-qualification (this loop's new finding) — small, isolated fix:
InMemory OID→schema-name lookup + unconditional schema. prefix. (3)
FOR ORDER BY sort-family resolution (small). (4) M0119-0005/0006/0007
(pg_waldump/pg_amcheck/pg_basebackup server tiers). (5) M0119-0002 (CLOG
store swap Part B) — flagged highest blast radius, needs dedicated
full-gate session. (6) datacl (pg_database ACL) — permanently deferred.
