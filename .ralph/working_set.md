(idle — nothing in flight)

Loop #27 COMPLETE: M0119-0004 DU-002 slice 387 — COMMENT ON FOREIGN DATA WRAPPER
pg_dump round-trip (sibling of slice 386 COMMENT ON SERVER).

A foreign-data wrapper (pg_foreign_data_wrapper, classoid 2328) can carry a
comment; pg_dump's dumpForeignDataWrapper re-emits `COMMENT ON FOREIGN DATA
WRAPPER <name> IS '...'`. parseCommentOnTail had no FOREIGN DATA WRAPPER branch →
silently swallowed → never reached pg_description → pg_dump couldn't re-emit.

Files (committed, pushed):
- internal/parser/parser.go: `case p.acceptKeyword(KwForeign)` arm in
  parseCommentOnTail (consumes DATA WRAPPER ident-keywords; ObjKind="foreign data
  wrapper", bare schema-less name).
- internal/executor/operators_ddl.go (execCommentOn): new "foreign data wrapper"
  case → ForeignDataWrapperOID(name) + SetComment(oidPgFdw=2328, oid, 0, desc);
  new oidPgFdw constant.
- internal/parser/comment_on_test.go: new TestParseCommentOnForeignDataWrapper.
- internal/testport/pgdump_connsetup_test.go: COMMENT ON FOREIGN DATA WRAPPER
  goopg_fdw fixture (setup batch) + assertion (dump-check batch).
- docs/design/0110-0001-pg-dump-tap-port.md: Slice 387 section.
- .ralph/fix_plan.md: slice 387 entry under M0119-0004.

Gates: TestParseCommentOn* PASS; TestPort_PgDumpConnectionSetup PASS (~5s,
byte-identical vs pg_dump 18.3); go build clean; gofmt clean on my edits
(operators_ddl.go's other hunks = pre-existing go1.25/1.26 noise); pgbench smoke
= pre-commit hook; ralph-state-guard OK. No new deferral.

Next loop: fresh M0119-0004 pg_dump slice. Candidates: COMMENT ON EXTENSION;
COMMENT ON LARGE OBJECT; COMMENT ON CONVERSION; COMMENT ON COLLATION; CREATE
COLLATION; range types; aggregates; operators; text-search configs.
