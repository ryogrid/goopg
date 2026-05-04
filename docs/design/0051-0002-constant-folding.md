# 0051-0002 — Constant folding

**Status:** draft
**Date:** 2026-05-04
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

## Plan

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
4. Wire-in: analyzer's existing post-bind phase calls `FoldConstants`
   on every expression in: target list, WHERE, ON, HAVING, GROUP BY,
   ORDER BY, JOIN-ON predicates, CHECK constraints.
5. Pure-function predicate: a small allow-list of stable+immutable
   built-ins (`length`, `upper`, `lower`, `substring(literal, ..., ...)`).
   Volatile / stable functions (`now`, `random`) are not folded.

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
