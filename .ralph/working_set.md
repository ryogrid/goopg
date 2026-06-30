(idle — nothing in flight)

Loop #9 COMPLETE: M0119-0004 DU-002 slice 370 — `COMMENT ON TRIGGER <name> ON
<table>` now round-trips through pg_dump (PRODUCTION fix).

How: PG stores a trigger comment in pg_description keyed (classoid=pg_trigger=
2620, objoid=trig.oid, objsubid=0); pg_dump's dumpTrigger (pg_dump.c:19251) calls
dumpComment so a dump re-emits `COMMENT ON TRIGGER … ON … IS '...';`. goopg's
parseCommentOnTail had NO TRIGGER branch → the stmt fell to the unsupported-
default arm and the server silently swallowed it (never reached pg_description).
Added: parser branch capturing `TRIGGER <name> ON [schema.]table` (same
`<name> ON <table>` shape as COMMENT ON CONSTRAINT) → SubName=name, ObjName=table,
ObjKind="trigger"; executor `execCommentOn` case "trigger" resolves the trigger by
name on the table (LookupTable→Table.Triggers) → SetComment(2620, trig.OID, 0,
desc). pg_trigger already exposes oid/tableoid=2620 (slice 319); collectComments
reads the keyed row → NO catalog-query change.

Files:
- internal/parser/parser.go (parseCommentOnTail: KwTrigger case)
- internal/parser/comment_on_test.go (TestParseCommentOnTrigger)
- internal/executor/operators_ddl.go (execCommentOn: oidPgTrigger + case "trigger")
- internal/testport/pgdump_connsetup_test.go (slice-370 trg_biu fixture+assert)
- docs/design/0110-0001-pg-dump-tap-port.md (Slice 370)
- .ralph/deferral_ledger.md + .ralph/fix_plan.md (slice 370)

Gates: parser suite PASS; executor Comment tests PASS; TestPort_PgDumpConnection
Setup PASS (5.3s, byte-identical vs real pg_dump 18.3); build clean; pgbench smoke
= pre-commit hook. gofmt -l flags operators_ddl.go line 8293 — PRE-EXISTING
version-mismatch artifact (comment alignment), NOT my edit.

Deferred (ledger): restart persistence (in-memory pg_description); COMMENT ON
{RULE,POLICY,COLLATION,LANGUAGE,DATABASE,EXTENSION} still dropped (no parser
branch — sibling slices).

Next loop: pick a fresh M0119-0004 pg_dump slice. Candidate: COMMENT ON {RULE,
POLICY} (RULE/POLICY objects already dump, so these may be bounded siblings).
