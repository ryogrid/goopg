Task: DU-002 slice 297 — COMPLETE, committing + pushing.

Last landed: PRODUCTION fix. Nested-arithmetic column DEFAULT full parenthesization.
`catalog.formatExprForAttrdef` rendered each `BinaryOp` WITHOUT parens, so `DEFAULT (1 + 2) * 3`
(AST `Mul(Add(1,2),3)`) dumped as `1 + 2 * 3` → re-parses to `Mul(1,Mul(2,3))` = 7 not 9: a SILENT
precedence corruption on restore. PG's pg_get_expr (prettyFlags=0, the adbin mode) FULLY
parenthesizes every binary OpExpr — empirically verified vs real PG 18.3 (`1+1`→`(1 + 1)`,
`(1+2)*3`→`((1 + 2) * 3)`). Fix wraps each `BinaryOp` `(left op right)` via new helper
`binaryOpSymbol(parser.OpCode) string`; recursion parenthesizes operands → byte-identical.

SCOPE: confined to ISOLATED `formatExprForAttrdef` (pg_attrdef column-default path). Twin
`executor.defaultExprToSQL` is overloaded (index predicates / expr-index cols / func-arg defaults /
partition keys), same under-parenthesization — DEFERRED to slice 298 (deferral ledger 2026-06-20).

Files:
- internal/catalog/catalog.go — new binaryOpSymbol() before formatExprForAttrdef (~L7819); BinaryOp
  case now `"(" + left + " " + op + " " + right + ")"` (~L7900).
- internal/catalog/catalog_test.go — TestFormatExprForAttrdefExpr: binary add→`(1 + 1)`, binary
  concat→`('a' || 'b')`, NEW `binary nested precedence`→`((1 + 2) * 3)`, row nested→`ROW(1, ('a' || 'b'))`.
- internal/testport/pgdump_connsetup_test.go — defcol fixture (+`calc integer DEFAULT (1 + 2) * 3`,
  ~L2259) + slice-297 assertion block (after slice-181 IS-DISTINCT assert, ~L5749) incl. absence guard
  for the corrupt `DEFAULT 1 + 2 * 3`.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 297 section + Next (298) note.
- .ralph/fix_plan.md — slice 297 progress (loop #65); .ralph/deferral_ledger.md — slice-298 twin defer.

Key symbols: binaryOpSymbol (catalog.go), formatExprForAttrdef (catalog.go:7861), defaultExprToSQL
(executor/operators_ddl.go:3390 — UNCHANGED, the deferred twin).

Gates: gofmt clean; go vet clean; TestFormatExprForAttrdefExpr PASS; ./internal/catalog/ full PASS;
TestPort_PgDumpConnectionSetup PASS (4.59s vs real pg_dump 18.3); make ralph-state-guard; pgbench
pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 298+): apply the same full-parenthesization to executor.defaultExprToSQL and update its
4 fixture families (index predicate `WHERE qty > 0`→`WHERE (qty > 0)`, expr-index, func-arg default,
partition key) — the deferred twin; OR a multi-column / NULL-typed DEFAULT variant on the partition-leaf
ALTER path.
