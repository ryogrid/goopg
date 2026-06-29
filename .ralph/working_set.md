(idle — nothing in flight)

Last loop (#52): M0119-0004 **WHEN-condition trigger round-trip in pg_dump**
(DU-002 slice 329) — LANDED, committed. This was the LAST `pg_get_triggerdef` gap;
the getter battery (timing, OR-ed events, UPDATE OF, CONSTRAINT TRIGGER,
REFERENCING, WHEN) is now complete.

pg_get_triggerdef_worker (ruleutils.c) reads pg_trigger.tgqual and emits
`WHEN (<cond>) ` between FOR EACH and EXECUTE FUNCTION, building old/new-aliased
RTEs + get_rule_expr(varprefix=true) so refs render `old.`/`new.` lowercased;
prettyFlags=0 fully parenthesizes the OpExpr → `WHEN ((new.b <> old.b))`. goopg's
parser recognised WHEN but DISCARDED the body (paren-balance token loop). Fix
(dump-fidelity only — WHEN not evaluated at firing time):
- parser ast.go/ddl.go: CreateTriggerStmt.WhenExpr; parseCreateTriggerTail parses
  `WHEN '(' a_expr ')'` via p.parseExpr (lexer already lowercases unquoted NEW/OLD
  qualifier onto each *ColumnRef → new.b/old.b).
- catalog.go: Trigger.WhenExpr parser.Expr. tgqual projection left "" (pg_dump
  drives off pg_get_triggerdef, never reads tgqual directly).
- operators_ddl.go: execCreateTrigger copies WhenExpr.
- expr.go: buildTriggerDefString emits `WHEN (defaultExprToSQL(WhenExpr)) ` —
  chose defaultExprToSQL (executor twin) over formatExprForAttrdef because the
  latter DROPS the ColumnRef qualifier; defaultExprToSQL preserves it AND fully
  parenthesizes binary OpExprs (slice 298) = PG prettyFlags=0 output.

Files: internal/parser/{ast.go,ddl.go,create_trigger_test.go},
internal/catalog/catalog.go, internal/executor/{operators_ddl.go,expr.go,
triggerdef_test.go}, internal/testport/pgdump_connsetup_test.go (trg_when +
trg_whna fixtures+asserts), docs/design/0119-0004-trigger-when-condition.md
(+README 0119-0004af).

Gates: parser/catalog/executor suites PASS; TestPort_PgDumpConnectionSetup PASS
(real pg_dump 18.3, byte-identical, 5.6s); go build ./... clean; pgbench smoke via
pre-commit hook.

NEXT loop — pick the next M0119-0004 DU-002 surface from fix_plan: GRANT/ACL
(`relacl`, needs per-role OID registry + the ARRAY(SELECT…)/quote_ident query
stack), named-role policies, or extended-protocol commit-time deferral. Triggers
WHEN runtime evaluation (not dump) is a separate non-DU task.
