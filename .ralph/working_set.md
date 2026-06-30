(idle — nothing in flight)

Loop #11 COMPLETE: M0119-0004 DU-002 slice 372 — `COMMENT ON RULE <name> ON
<table>` now round-trips through pg_dump (PRODUCTION fix).

How: PG stores a query-rewrite-rule comment in pg_description keyed
(classoid=pg_rewrite=2618, objoid=rule.oid, objsubid=0); pg_dump's dumpRule
(pg_dump.c:19359) builds prefix "RULE %s ON" + qtabname and calls dumpComment so
a dump re-emits `COMMENT ON RULE <name> ON <schema>.<table> IS '...';`. goopg's
parseCommentOnTail had NO RULE branch → fell to unsupported-default arm, server
silently swallowed it. Added: parser branch capturing `RULE <name> ON
[schema.]table` (same shape as COMMENT ON TRIGGER/POLICY) → SubName=name,
ObjName=table, ObjKind="rule"; executor execCommentOn case "rule" resolves the
rule by name on the table (LookupTable→Table.Rules) → SetComment(2618, r.OID, 0,
desc). CREATE RULE already round-trips (slice 324; each RuleInfo carries its own
OID, projected into pg_rewrite virtual catalog); pg_description path is
classoid-agnostic (slices 370/371) → NO catalog-query change.

Files:
- internal/parser/parser.go (parseCommentOnTail: "rule" case)
- internal/parser/comment_on_test.go (TestParseCommentOnRule)
- internal/executor/operators_ddl.go (execCommentOn: oidPgRewrite + case "rule")
- internal/testport/pgdump_connsetup_test.go (slice-372 r_noins fixture+assert)
- docs/design/0110-0001-pg-dump-tap-port.md (Slice 372)
- .ralph/deferral_ledger.md + .ralph/fix_plan.md (slice 372)

Gates: parser suite PASS; TestPort_PgDumpConnectionSetup PASS (5.7s, byte-identical
vs real pg_dump 18.3); build clean; pgbench smoke = pre-commit hook.

Deferred (ledger): restart persistence (in-memory pg_description); COMMENT ON
{COLLATION,LANGUAGE,DATABASE,EXTENSION} still dropped (no parser branch —
sibling slices).

Next loop: pick a fresh M0119-0004 pg_dump slice. Remaining COMMENT ON targets
(COLLATION/LANGUAGE/DATABASE/EXTENSION) each block on that object type becoming
dumpable; otherwise look for a fresh non-COMMENT DU-002 catalog-view parity slice.
