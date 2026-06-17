(idle — nothing in flight)

Last landed: DU-002 slice 147 (loop #112) — COMMENT ON FUNCTION now round-trips
through pg_dump. TWO coupled bugs: (1) parseCommentOnTail had no FUNCTION branch
(silently swallowed); (2) the load-bearing one — pg_proc is a virtual view whose
Table struct never set OID, so its `tableoid` system column resolved to 0
(resolveTableoidForBinding returns b.table.OID). pg_dump's collectComments matches
a pg_description row to a dumpable object by {classoid, objoid} where the function's
catId.tableoid comes from pg_proc.tableoid; 1255 ≠ 0 → comment discarded even though
it was in pg_description. TYPE/DOMAIN worked (slice 146) only because pg_type is
heap-backed with OID=1247.
Fix: (1) parser KwFunction branch → parseObjectName + parseFunctionArgList into new
CommentOnStmt.Args; (2) execCommentOn `function` case resolves routine via
Routines().Lookup, keys row under pg_proc (1255); (3) registerPgProcView sets
OID: catalog.ProcedureRelationId (new const 1255).
Key symbols: parser.parseCommentOnTail, CommentOnStmt.Args, ddlOp.execCommentOn,
catalog.Routines.Lookup, registerPgProcView, catalog.ProcedureRelationId,
resolveTableoidForBinding.
Files: internal/parser/ast.go, internal/parser/parser.go,
internal/executor/operators_ddl.go, internal/executor/comment_on_function_test.go,
internal/catalog/catalog.go, internal/initdb/pg_proc_view.go,
internal/testport/pgdump_connsetup_test.go, docs/design/0110-0001-pg-dump-tap-port.md.
Verified: gofmt/build OK; parser+catalog+initdb+executor suites PASS;
TestCommentOnFunctionStoresPgProcDescription PASS;
TestPort_PgDumpConnectionSetup PASS (2.49s). Committed + pushed.

Next direction (slice 148): a fresh pg_dump catalog-surface gap. Candidates:
COMMENT ON {COLLATION, EXTENSION, AGGREGATE} round-trip (none handled by
parseCommentOnTail; check each object is actually dumped by goopg pg_dump first —
the slice-147 lesson: a virtual catalog view must set its Table.OID or tableoid
resolves to 0 and pg_dump comment-matching silently drops the comment). Or the
deferred-check EXECUTION spike (validate at COMMIT, not per-row).
