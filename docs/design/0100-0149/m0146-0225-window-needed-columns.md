# M0146-0225 — window functions keep the needed-column set (TPC-DS SF1 Q53/Q63)

Status: landed 2026-10-19. Filed by the S9 pass at 0d94a7133
(`analysis/m0146/m0146-0014/routing-20261019a/`). Evidence: `analysis/m0146/m0146-0225/`.

## 1. Record

TPC-DS SF1 Q53 and Q63 diverged at depth 9, at the probe into `store` under a Memoize:

- PG: `Index Only Scan using store_pkey on store (cost=0.14..0.15 rows=1 width=4)`.
- goopg: `Index Scan using store_pkey on store (cost=0.14..0.15 rows=1 width=1812)`.

The costs were identical and goopg's width was the whole row. Both records persisted under PG statistics.

## 2. Cause

The parameterised index producer (`pathparamindex.go`) marks a probe index-only when `check_index_only` holds:
the statement's needed-column set is known, the leaf is bare, and the index covers every column read from the
relation. Q53's join block alone got the index-only probe. Adding a window function turned it back into a plain
probe, with or without the enclosing derived table.

`neededColumnNames` (`pathindexonlyneed.go`) declined any `FuncCall` with an `OVER` clause and any statement with a
`WINDOW` clause ("a window function's frame can name columns"). A declined set means "every column of every
relation is needed". So no relation of a statement with a window function could get an index-only path, and none of
its leaves could be narrowed (`narrowoutput.go` keys off the same `NeededColsKnown`).

## 3. PG

`build_base_rel_tlists` (initsplan.c) adds every Var of the final target list. A window function's PARTITION BY and
ORDER BY expressions are target-list entries (resjunk sort/group clauses), and its arguments are walked like any
other expression. Frame offsets cannot reference a column: `transformFrameOffset` (parse_clause.c) calls
`checkExprIsVarFree`. So a window reads exactly its arguments, FILTER, PARTITION BY and ORDER BY.

## 4. Change

- `collectWindowDefColumnNames` walks a `WindowDef`'s PARTITION BY and ORDER BY expressions. It also walks the
  frame offsets: they can name no column, but over-stating the set is the safe direction.
- `collectNamedWindowColumnNames` walks each `WINDOW name AS (...)` entry of a SELECT. Window names are scoped to
  their SELECT, so a function's `RefName` always resolves to an entry walked there.
- `collectExprColumnNames`' `FuncCall` arm walks the `OVER` definition instead of declining.
- `collectStmtColumnNames` (needed set) and its sibling `collectOutputColumnNames` (above-tree set) walk the WINDOW
  clause instead of declining. Both are changed together (rule #2).
- `collectSublinkOuterNames` still declines a window function: one cannot appear in WHERE or JOIN ON
  (`transformWindowFuncCall` rejects it), and declining stays safe.

## 5. Measured

Census `analysis/m0146/m0146-0225/census/`, against census4's PG arms:

- SF1 PLAN-PARITY 78→80 (Q53, Q63 MATCH); CATEGORIES-EXCL-MATCH scan-type 12→10, no other category moved.
- SF0.25 unchanged at 91/99 (Q53/Q63 already matched there: a hash join over `Seq Scan on store`).
- TPC-H unchanged (no window functions).
- Ten window queries change plan text at each scale (Q12, Q20, Q47, Q51, Q53, Q57, Q63, Q70, Q89, Q98). Apart
  from Q53/Q63, only the widths change: narrowed leaves now reach the window statements (Q12 SF1: 2460→1936).
  Costs are unchanged.
- Fire set: all 20 fired results byte-identical between baseline and candidate.

Pin: `TestWindowFunctionKeepsNeededSet` (needed and output sets; inline window, named window, frame offset),
mutation-checked by dropping the PARTITION BY walk. `TestOutputColumnNamesDeclinesLikeNeeded` loses its window case.
