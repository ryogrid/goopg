(idle — nothing in flight)

Last loop (#51): M0119-0004 **REFERENCING transition-table trigger round-trip in
pg_dump** (DU-002 slice 328) — LANDED, committed.

pg_get_triggerdef_worker (ruleutils.c) reads pg_trigger.tgoldtable/tgnewtable and
emits `REFERENCING OLD TABLE AS <ot> NEW TABLE AS <nt> ` (OLD first, either/both
present) between the ON-table/deferrability clause and FOR EACH. goopg's parser had
no REFERENCING branch (parse error) and the deparser/projection were empty. Fix
(dump-fidelity only — transition tables not materialised):
- parser ast.go/ddl.go: CreateTriggerStmt.{OldTransitionTable,NewTransitionTable};
  parseCreateTriggerTail parses `REFERENCING { OLD | NEW } TABLE [AS] <name> [ … ]`
  (any order, optional AS, loops while next ident is OLD/NEW) after deferrability,
  before FOR EACH. OLD/NEW/REFERENCING matched as case-insensitive idents.
- catalog.go: Trigger.{OldTransitionTable,NewTransitionTable} → pg_trigger rows
  17/18 (tgoldtable/tgnewtable).
- operators_ddl.go: execCreateTrigger copies the two names.
- expr.go: buildTriggerDefString emits the REFERENCING clause (OLD first,
  pgQuoteIdent each) between deferrability and FOR EACH.

Files: internal/parser/{ast.go,ddl.go,create_trigger_test.go},
internal/catalog/catalog.go, internal/executor/{operators_ddl.go,expr.go,
triggerdef_test.go}, internal/testport/pgdump_connsetup_test.go (trg_ref + trg_refn
fixtures+asserts), docs/design/0119-0004-trigger-referencing-transition-tables.md
(+README 0119-0004ae).

Gates: parser/catalog/executor suites PASS; TestPort_PgDumpConnectionSetup PASS
(real pg_dump 18.3, byte-identical, 4.7s); go build clean; pgbench smoke via
pre-commit hook.

NEXT loop — the LAST `pg_get_triggerdef` gap: trigger `WHEN (condition)` (tgqual).
Needs a dedicated OLD/NEW-qualified expression deparser — `formatExprForAttrdef`
DROPS qualifiers so NEW.b would render as bare `b`; PG renders `WHEN ((new.b <>
old.b))` with lowercased old./new. qualifiers. Higher-effort than slices 326-328.
