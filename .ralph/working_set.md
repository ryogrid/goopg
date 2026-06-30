(idle — nothing in flight)

Loop #91 COMPLETE: M0119-0004 DU-002 slice 360 — a BARE FUNCTION-CALL
expression-index key (`lower(name)`, `lpad(name, 5)`) now dumps WITHOUT the
extra wrapping parens that the arithmetic key (slice 299) carries. PG's
pg_get_indexdef_worker prints a COERCE_EXPLICIT_CALL FuncExpr key as-is and
wraps every other expression in `(%s)`. Fix = catalog.indexKeyIsBareFuncCall
(keyed on the parsed Index.ColExprs AST) gating the wrap in catalog.BuildIndexDef.
Byte-identical vs real pg_dump 18.3. Committed.

Files: internal/catalog/catalog.go (helper + BuildIndexDef gate),
internal/testport/pgdump_connsetup_test.go (slice 360 DDL + asserts),
docs/design/0110-0001-pg-dump-tap-port.md (slice 360 entry), deferral ledger.

Deferred (ledgered): (a) typed string-literal cast inside a function-arg key —
PG dumps `upper((name || '_x'::text))` but goopg's type-blind defaultExprToSQL
renders `'_x'` with no cast/inner-parens (same gap as slice 302's `'-N'::type`);
(b) restart persistence — ColExprs in-memory only + pg_index.indexprs dumps NULL.

Next loop: pick a fresh M0119-0004 pg_dump slice. Candidate next surfaces:
the deferred (a) typed-literal-in-funcarg cast (needs operand-type threading);
USING hash index dump (goopg stores hash as btree — known gotcha); or another
catalog-view gap. Heavier M0119 items: M0119-0002 (CLOG swap), M0119-0005/0006
server tiers, M0119-0007 (logical decoding — not actionable).
