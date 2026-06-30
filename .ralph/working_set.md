(idle — nothing in flight)

Loop #28 COMPLETE: M0119-0004 DU-002 slice 388 — COMMENT ON EXTENSION pg_dump
round-trip (sibling of slices 386 COMMENT ON SERVER / 387 COMMENT ON FOREIGN DATA
WRAPPER).

An installed extension (pg_extension, classoid 3079) can carry a comment;
pg_dump's dumpExtension re-emits `COMMENT ON EXTENSION <name> IS '...'` after the
CREATE EXTENSION line. parseCommentOnTail had no EXTENSION branch → silently
swallowed → never reached pg_description → pg_dump couldn't re-emit.

Files (committed, pushed):
- internal/parser/parser.go: `case p.acceptIdentKeyword("extension")` arm in
  parseCommentOnTail (ObjKind="extension", bare schema-less name).
- internal/catalog/catalog.go: new ExtensionOID(name) method (reads runtime
  `extensions` registry).
- internal/executor/operators_ddl.go (execCommentOn): new "extension" case →
  ExtensionOID(name) + SetComment(oidPgExtension=3079, oid, 0, desc); new
  oidPgExtension constant.
- internal/parser/comment_on_test.go: new TestParseCommentOnExtension.
- internal/testport/pgdump_connsetup_test.go: CREATE EXTENSION amcheck +
  COMMENT ON EXTENSION amcheck fixture (setup batch) + assertion (dump-check).
- docs/design/0110-0001-pg-dump-tap-port.md: Slice 388 section.
- .ralph/fix_plan.md: slice 388 entry under M0119-0004.

Gates: TestParseCommentOn* PASS; TestPort_PgDumpConnectionSetup PASS (~5.3s, dump
contains the CREATE EXTENSION + COMMENT ON EXTENSION lines); go build clean;
gofmt clean on my edits (catalog.go/operators_ddl.go other hunks = pre-existing
go1.25/1.26 noise); pgbench smoke = pre-commit hook; ralph-state-guard OK. No new
deferral.

Next loop: fresh M0119-0004 pg_dump slice. Candidates: COMMENT ON COLLATION /
CREATE COLLATION; COMMENT ON CONVERSION; COMMENT ON LARGE OBJECT; range types;
aggregates; operators; text-search configs.
