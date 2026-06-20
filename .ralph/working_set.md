Task: DU-002 slice 298 — COMPLETE, committing + pushing.

Last landed: PRODUCTION fix — index-predicate BinaryOp full parenthesization (the slice-297
deferred twin). `executor.defaultExprToSQL` (deparse renderer for index predicates /
expression-index cols / partition-key exprs / function-arg defaults) emitted each `BinaryOp`
WITHOUT parens, so a partial index `WHERE (qty + id) * mgr_id > 0` deparsed to the
precedence-corrupt `WHERE qty + id * mgr_id > 0` → re-parses as `qty + (id * mgr_id) > 0` on
restore (SILENT change to which rows the partial index covers). Real pg_dump 18.3 FULLY
parenthesizes (verified): `WHERE (qty > 0)`, `WHERE (((qty + id) * mgr_id) > 0)`.

LEDGER OVERESTIMATED blast radius: the ONLY binary op flowing through defaultExprToSQL in the
fixtures was the slice-56 `WHERE qty > 0`, and its substring-Contains assertion (goopg-vs-self,
not a true PG diff) MASKED the divergence. Func-arg-default + expr-index fixtures use a bare
integer / func call (no binary op), so nothing else regressed.

Fix: new `binaryOpSymbolForDefault(parser.OpCode)` helper (executor twin of
catalog.binaryOpSymbol; duplicated — catalog⇄executor can't import each other) + BinaryOp arm
returns `"(" + left + " " + sym + " " + right + ")"`. Mirrors slice 297's formatExprForAttrdef.

Files:
- internal/executor/operators_ddl.go — binaryOpSymbolForDefault() before defaultExprToSQL (~L3390);
  BinaryOp case now wraps in parens (~L3454).
- internal/executor/default_validate_test.go — NEW TestDefaultExprToSQLBinaryParen + parser import.
- internal/testport/pgdump_connsetup_test.go — NEW foo_calc_partial_idx index (~L2421); corrected
  foo_qty_partial_idx assertion → `WHERE (qty > 0)` + nested assertion (~L6454); absence guard for
  corrupt `WHERE qty + id * mgr_id` (after indexDefs loop, ~L6510).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 298 section.
- .ralph/fix_plan.md (loop #66 progress); .ralph/deferral_ledger.md (slice 298 landed, 299 deferred).

Key symbols: binaryOpSymbolForDefault (operators_ddl.go), defaultExprToSQL (operators_ddl.go:3390+44),
binaryOpSymbol (catalog.go:7823 — the sibling twin, keep in sync).

Gates: gofmt clean; go vet clean; TestDefaultExprToSQLBinaryParen PASS; ./internal/executor/ full
PASS (1.56s); TestFormatExprForAttrdefExpr PASS; TestPort_PgDumpConnectionSetup PASS (4.73s vs real
pg_dump 18.3); make ralph-state-guard; pgbench pre-commit smoke (enforced by .githooks/pre-commit).

Next (slice 299+): add an oracle-verified fixture for ONE still-unfixtured defaultExprToSQL context
(expr-index `((a + b))`, partition-key `RANGE ((((a+b)*c)))`, or func-arg default `((1+2)*3)` with a
binary op) — the renderer already parenthesizes them, just no byte-verified fixture yet; OR a
multi-column / NULL-typed DEFAULT variant on the partition-leaf ALTER path.
