(idle — nothing in flight)

Last landed: DU-002 slice 179 (loop #147) — row-constructor column DEFAULT now
round-trips through pg_dump; sibling-path divergence with the executor twin closed.

`DEFAULT (1, 2)` parses to a `*parser.RowExpr` (explicit `ROW(1,2)` parses to a FuncCall
named "row", already handled). validateDefaultExpr accepts it (falls through to nil; no
DDL-time type coercion), so the node reaches pg_attrdef.adbin. Neither
catalog.formatExprForAttrdef nor executor.defaultExprToSQL had a `*RowExpr` arm → both
fell through to fmt.Sprintf("%v", e) (Go pointer string), corrupting the dump. Fix: both
twins gain a `*RowExpr` case rendering `ROW(e1, e2, …)` (elems rendered recursively).
PG's ruleutils ALWAYS prints the ROW keyword (get_rule_expr T_RowExpr: "for simplicity we
always print it"), confirmed in postgres/src/backend/utils/adt/ruleutils.c:9904. Display-
only; can't share code (catalog below executor in import graph).

Files: internal/catalog/catalog.go (+1 case), internal/executor/operators_ddl.go (+1 case,
twin), internal/catalog/catalog_test.go (row constructor + nested cases),
internal/testport/pgdump_connsetup_test.go (defcol gains `pair integer DEFAULT (1, 2)` +
assertion `DEFAULT ROW(1, 2)`), docs/design/0110-0001-pg-dump-tap-port.md (Slice 179 section).
Gates: gofmt OK; go vet clean; catalog format tests PASS;
TestPort_PgDumpConnectionSetup PASS (2.93s, not skipped); pgbench pre-commit smoke on commit.

Next (slice 180 candidates): (1) FuncCall "row" renders lowercase `row(1, 2)` — PG renders
`ROW(1, 2)`; round-trips but not PG-faithful (low value). (2) COALESCE/NULLIF/GREATEST/LEAST
parse as FuncCall → render lowercase; PG uppercases (round-trips; faithfulness only).
(3) deferred MINVALUE/MAXVALUE keyword-AST-node slice (HIGHER RISK: partition routing).
(4) close the validateDefaultExpr array/row/CASE-element recursion gap (executor semantic
change — needs its own gates). Audit appears near-exhausted for distinct fall-through nodes.
