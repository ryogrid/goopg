(idle — nothing in flight)

Loop #10 COMPLETE: M0119-0004 DU-002 slice 371 — `COMMENT ON POLICY <name> ON
<table>` now round-trips through pg_dump (PRODUCTION fix).

How: PG stores an RLS-policy comment in pg_description keyed (classoid=pg_policy=
3256, objoid=pol.oid, objsubid=0); pg_dump's dumpPolicy (pg_dump.c) calls
dumpComment(fout, "POLICY <name> ON", qtabname, …, polinfo->dobj.catId=3256, 0)
so a dump re-emits `COMMENT ON POLICY <name> ON <schema>.<table> IS '...';`.
goopg's parseCommentOnTail had NO POLICY branch → the stmt fell to the
unsupported-default arm and the server silently swallowed it. Added: parser branch
capturing `POLICY <name> ON [schema.]table` (same shape as COMMENT ON TRIGGER) →
SubName=name, ObjName=table, ObjKind="policy"; executor execCommentOn case
"policy" resolves the policy by name on the table (LookupTable→Table.Policies) →
SetComment(3256, pol.OID, 0, desc). CREATE POLICY already round-trips (slices
323/330; pg_policy exposes each policy's oid); pg_description path is
classoid-agnostic (slice 370) → NO catalog-query change.

Files:
- internal/parser/parser.go (parseCommentOnTail: "policy" case)
- internal/parser/comment_on_test.go (TestParseCommentOnPolicy)
- internal/executor/operators_ddl.go (execCommentOn: oidPgPolicy + case "policy")
- internal/testport/pgdump_connsetup_test.go (slice-371 p_simple fixture+assert)
- docs/design/0110-0001-pg-dump-tap-port.md (Slice 371)
- .ralph/deferral_ledger.md + .ralph/fix_plan.md (slice 371)

Gates: parser suite PASS; TestPort_PgDumpConnectionSetup PASS (5.2s, byte-identical
vs real pg_dump 18.3); build clean; pgbench smoke = pre-commit hook.

Deferred (ledger): restart persistence (in-memory pg_description); COMMENT ON
{RULE,COLLATION,LANGUAGE,DATABASE,EXTENSION} still dropped (no parser branch —
sibling slices).

Next loop: pick a fresh M0119-0004 pg_dump slice. Candidate: COMMENT ON RULE
(CREATE RULE already round-trips via the pg_rewrite virtual catalog — bounded
sibling), then COLLATION/LANGUAGE/DATABASE/EXTENSION when each becomes dumpable.
