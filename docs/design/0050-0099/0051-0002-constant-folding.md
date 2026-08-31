# 0051-0002 — Constant folding

**Status:** accepted
**Date:** 2026-05-05
**Milestone:** 0051 — Planner expression-level improvements
**Supersedes:** —

## Context

`WHERE x > 1 + 2` re-evaluates `1 + 2` once per tuple today. The planner
analyses the predicate but emits the original expression tree to the
executor. A constant-folding pass can collapse every all-constant
sub-tree to a single literal. The savings per row are small but ubiquitous;
they compound across every long-running query.

The same pass also handles boolean simplification (`a AND TRUE → a`,
`a OR FALSE → a`), comparison folding (`1 < 2 → TRUE`), and
`CAST(literal AS T)` to a literal of T. A natural extension is `IS NULL`
on a literal NULL → TRUE etc.

## Implementation (landed 2026-05-05)

### New file: `internal/planner/foldconst.go`

`FoldConstants(expr Expr) Expr` — bottom-up recursive pass. Per node type:
- **Literals** (`IntegerConst`, `StringConst`, `NumericConst`, `BooleanConst`,
  `NullConst`, `TypedStringLit`, `IntervalLit`, `ParamRef`) — returned unchanged.
- **BinaryOp** — both children folded first, then:
  - `AND`/`OR`: short-circuit regardless of the other child's type:
    `FALSE AND x → FALSE`, `TRUE AND x → x`, `TRUE OR x → TRUE`, `FALSE OR x → x`.
    Kleene rules for `NULL AND FALSE → FALSE` and `NULL OR TRUE → TRUE`.
  - NULL propagation: if either non-AND/OR operand is `NullConst`, result is `NullConst`.
  - Both children are literals: evaluate via `evalLiteralBinary` → return result literal.
  - Non-literal child present: return `BinaryOp` with recursively-folded children.
- **UnaryOp** — `-`/`+` on `IntegerConst`/`NumericConst`, `NOT` on `BooleanConst`.
- **CaseExpr** — each WHEN condition folded; `WHEN FALSE` branches dropped; `WHEN TRUE`
  branch terminates with that THEN, subsequent WHENs dropped.
- **Other** (`ExtractExpr`, `InExpr`, `FuncCall`) — children recursively folded; node type
  unchanged.

`foldPlanConstants(node Node)` — in-place plan-tree walk that applies `FoldConstants`
to every embedded expression (`Filter.Predicate`, `Project.Targets`, `Sort.Keys`,
`Limit.Limit/Offset`, `Aggregate.GroupExprs/Aggs`, `Join.LeftKey/RightKey/Predicate`,
`WindowAgg`).

### Wiring: `internal/planner/planner.go`

`foldPlanConstants(out)` called at the end of `planSelect()` just before the final
`return out, nil`. This covers SELECT, INSERT (via subquery planner), and any
UPDATE/DELETE that drives through `Plan()`.

### Operators evaluated by `evalLiteralBinary`

| Operands | Operations |
|----------|------------|
| int × int | `+`, `-`, `*`, `/` (integer division), `%` |
| int/num × int/num | `+`, `-`, `*`, `/` (promotes to float64 for numeric mix) |
| str × str | `\|\|` (concat), `=`, `<>`, `<`, `>`, `<=`, `>=` |
| int/num × int/num | comparisons `=`, `<>`, `<`, `>`, `<=`, `>=` |
| bool × bool | `AND`, `OR` (both literal — short-circuit path handles mixed) |

### Original Plan

1. New file `internal/planner/foldconst.go::FoldConstants(expr) Expr`.
2. Recursive walk; bottom-up.
3. Per node-kind:
   - `Literal` — return as-is.
   - `BinaryOp` — fold each child; if both children are Literals and
     the op is pure (`+`, `-`, `*`, `/`, `||`, comparison, boolean),
     evaluate against the executor's existing `evalBinary` and return a
     Literal of the result.
   - `UnaryOp` — same pattern.
   - `BoolOp` (`AND`, `OR`, `NOT`) — short-circuit on literal operands;
     drop dead branches.
   - `CaseExpr` — fold each WHEN; drop branches whose condition is
     `FALSE`; if a WHEN's condition is `TRUE`, drop subsequent WHENs.
   - `CastExpr` — evaluate against the type's input/output coercion when
     the inner is a Literal.
4. Wire-in: `foldPlanConstants` at the end of `planSelect()`.
5. Function calls are not folded (volatile-function exclusion still applies).

## Definition of Done

- `EXPLAIN SELECT * FROM t WHERE x > 1 + 2` shows `x > 3`.
- `EXPLAIN SELECT * FROM t WHERE TRUE AND x = 1` shows `x = 1` (the
  `TRUE AND` is gone).
- Regression matrix in `internal/planner/foldconst_test.go` covers
  every operator/literal combo.
- TPC-H 22/22 unchanged.

## Upstream reference

- `postgres/src/backend/optimizer/util/clauses.c` —
  `eval_const_expressions`.
- `postgres/src/backend/parser/parse_expr.c` — analyzer hook.

## goopg references

- `internal/analyzer/analyzer.go`, `internal/planner/expr.go`.
- `docs/design/root-0011-planner.md`.
