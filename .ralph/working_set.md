Loop #36 COMPLETE: M0119-0004 DU-002 slice 396 — a COMMENT on a user-defined CAST
(`COMMENT ON CAST (text AS bytea) IS '...'`) now round-trips through real pg_dump
18.3. Follow-on to slice 395 (CREATE CAST); mirrors slice 390 (COMMENT ON COLLATION).

Root cause: goopg's COMMENT-ON parser dispatch had no CAST branch — it fell through
and silently swallowed the statement, so the comment never reached pg_description
and dumpCast's trailing `COMMENT ON CAST (...) IS '...';` was never emitted.

Three-layer fix (committed):
- internal/parser/parser.go: new `case p.acceptIdentKeyword("cast")` in COMMENT-ON
  dispatch parses `(src AS tgt)` via existing parseCastTypeName into new
  CommentOnStmt.CastSource/CastTarget (ast.go). "cast" is an unreserved ident-kw.
- internal/executor/operators_ddl.go: new `case "cast"` in COMMENT handler resolves
  the cast OID via catalog.CastByTypes and SetComment(oidPgCast=2605, cast.OID, 0,
  desc); built-in/unknown cast (OID 0) → harmless no-op. Added oidPgCast=2605 const.
- internal/catalog/catalog.go: new CastByTypes(source,target) lookup (same
  lower(src)\x00lower(tgt) key as RegisterCast/DropCast). pg_description virtual view
  already walks AllComments generically → row surfaces with no view change.
- internal/testport/pgdump_connsetup_test.go: COMMENT ON CAST fixture + exit-0-block
  assertion. internal/parser/comment_on_test.go: TestParseCommentOnCast.
- docs/design/0110-0001-pg-dump-tap-port.md slice 396 section; fix_plan + ledger row.

Gates: TestPort_PgDumpConnectionSetup PASS (5.3s); parser+catalog units PASS; build
clean. Verified byte-identical against real pg_dump 18.3 live (/tmp/castpg cluster:
`COMMENT ON CAST (text AS bytea) IS 'binary-coercible text to bytea';`). pgbench
smoke runs via pre-commit hook. No query-exec/codec/planner path touched.

Next loop: fresh M0119-0004 pg_dump slice. Candidates: WITH FUNCTION cast (needs a
pg_proc row so dumpCast→findFuncByOid resolves the function — see slice-395 ledger
resume point); cast/collation registry restart persistence (WAL-log CREATE CAST +
its COMMENT like CREATE SCHEMA); CREATE CONVERSION (needs pg_encoding_to_char builtin
+ conproc regproc resolution — harder, conversion funcs are C-language).
