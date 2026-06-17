(idle — nothing in flight)

Last landed: DU-002 slice 145 (loop #110) — COMMENT ON {VIEW,SEQUENCE,INDEX,SCHEMA}
now round-trips through pg_dump. parseCommentOnTail recognised only TABLE/INDEX/
COLUMN/CONSTRAINT/STATISTICS, so COMMENT ON VIEW/SEQUENCE/SCHEMA fell through to the
unsupported default branch and the server's COMMENT fallback silently swallowed them
(nothing reached pg_description → pg_dump re-emitted nothing). INDEX was parsed/stored
(classoid=pg_class) but never asserted through real pg_dump. Fix: (1) parser
parseCommentOnTail (internal/parser/parser.go) gains VIEW (KwView) + SEQUENCE/SCHEMA
(acceptIdentKeyword, neither is a lexer keyword) branches; (2) execCommentOn
(internal/executor/operators_ddl.go ~L8403) folds view+sequence into the `table`
case (both pg_class relations, classoid 1259, shared LookupTable path — pg_dump picks
keyword from relkind, stored pg_description row is keyword-agnostic) and adds a
`schema` case → im.SchemaOID(name) keyed under classoid=pg_namespace (2615). No
catalog-schema change.
Key symbols: parser.parseCommentOnTail, parser.CommentOnStmt.ObjKind,
ddlOp.execCommentOn, InMemory.LookupTable, InMemory.SchemaOID, InMemory.SetComment.
Files: internal/parser/parser.go, internal/parser/ast.go,
internal/executor/operators_ddl.go, internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/fix_plan.md.
Verified: gofmt/build/vet OK; parser+catalog+executor suites PASS;
TestPort_PgDumpConnectionSetup PASS (2.88s, 4 new asserts: foo_view VIEW, plain_seq
SEQUENCE, foo_name_idx INDEX, schema s — verified vs real pg_dump 18.3).
Committed + pushed.

Next direction (slice 146): a fresh pg_dump catalog-surface gap. Candidates:
COMMENT ON {MATERIALIZED VIEW, TYPE, DOMAIN, FUNCTION} round-trip through pg_dump
(none handled by parseCommentOnTail yet → silently swallowed). Or the deferred-check
EXECUTION spike (validate at COMMIT, not per-row) — a larger txn-machinery milestone,
separate from dump-fidelity.
