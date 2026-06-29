(idle — nothing in flight)

Last loop (#46): M0119-0004 **CREATE RULE (DO [INSTEAD] NOTHING) round-trip in
pg_dump** (DU-002 slice 324) — LANDED, committed.

pg_dump's getRules reads pg_rewrite and dumpRule re-emits each non-view rule via
pg_get_ruledef(oid) verbatim. goopg parsed CREATE RULE only as a CompatNoopStmt
(recorded nowhere) + empty pg_rewrite stub, so a rewrite rule was silently lost
on dump. The query-rewrite system is out of scope (action deparse = full
reverse-compiler), so this slice closed the contained DO-NOTHING subset.

Fix (dump-fidelity only — goopg implements no query rewrite):
- ast.go/ddl.go: CreateRuleStmt returned ONLY for the unconditional DO-NOTHING
  form on INSERT/UPDATE/DELETE (isNothing && !hasWhere && !hasAction); every
  other shape (action command, WHERE, ON SELECT) keeps the historical
  CompatNoopStmt with the same RuleKind the COPY-DML path needs.
- catalog.go: RuleInfo{Name,OID,Event,Instead} + Table.Rules + RuleInfo.EvType();
  pg_rewrite.VirtualRows projects them (ev_enabled='O', ev_qual/ev_action NULL —
  getRules never reads them).
- operators_ddl.go: execCreateRule (dup→42710, OID via AllocOID, no heap sync;
  PRESERVES the prior RegisterCompatObject + RegisterTableRuleKind bookkeeping)
  + execDropRule removes the modelled rule from Table.Rules.
- expr.go: pg_get_ruledef/pg_get_ruledef_ext builtin → buildRuleDefString
  reconstructs PG's PRETTYFLAG_INDENT text
  (`CREATE RULE n AS\n    ON EVENT TO schema.rel DO [INSTEAD ]NOTHING;`).
- planner.go: CreateRuleStmt added to DDL passthrough.

Files: internal/parser/{ast.go,ddl.go,rule_test.go},
internal/catalog/catalog.go, internal/executor/{operators_ddl.go,expr.go,
storage_ddl_test.go}, internal/planner/planner.go,
internal/testport/pgdump_connsetup_test.go (slice 324 fixture+assert),
docs/design/0119-0004-create-rule-rewrite.md (+README 0119-0004aa).

Gates: parser/catalog/planner/executor suites PASS; TestPort_PgDumpConnectionSetup
PASS (real pg_dump 18.3 — INSERT INSTEAD / UPDATE ALSO / DELETE plain byte-identical);
go build clean. DEFERRED (ledgered): conditional WHERE rules (OLD/NEW qual
deparse), action-command rules (full reverse-compiler), ALTER TABLE ENABLE/DISABLE
RULE.

NEXT loop — remaining M0119-0004 pg_dump getter-battery gaps: GRANT/ACL (relacl +
dumpACL — needs real ACL storage; GRANT is a CompatNoopStmt today), named-role
policies (needs a role-OID registry), or conditional/action CREATE RULE forms
(WHERE-qual deparse is the smaller next step). Pick one and scope a contained slice.
