# M0146-0126 — COALESCE, GREATEST and LEAST resolve their arguments' common type

Status: landed 2026-10-12.
Parent: none (S2, placed into banner item 2a by the owner on the evening of
2026-10-12).

## Problem

goopg typed COALESCE / GREATEST / LEAST by their first typed argument and
coerced nothing. Against PostgreSQL 18.3:

- `coalesce(1, 2.5) / 3` returned `0`, because it was typed integer and
  divided in integers. PG returns `0.33333333333333333333` (numeric). The
  same happened with a column operand, `coalesce(a, 2.5) / 3`.
- `coalesce(1, 2::bigint)` was integer; PG gives bigint.
  `coalesce(1::smallint, 2)` was smallint; PG gives integer.
  `greatest(1, 2.5)` was integer; PG gives numeric.
- `CREATE TABLE cy AS SELECT coalesce(a, b) …`, with `a` int and `b` numeric,
  created an integer column and **stored 2.5 as 3**.
- `coalesce(a, c)` with `a` int and `c` text ran. PG raises `COALESCE types
  integer and text cannot be matched`.
- `coalesce(a, 'x')` returned the literal. PG coerces `'x'` to integer and
  fails.
- The executor skipped an argument whose evaluation failed, as if it were
  NULL, for COALESCE / NULLIF / GREATEST / LEAST / num\_nulls /
  num\_nonnulls. So `coalesce(null::int, 'x'::int)` was NULL, where PG
  raises the input error.

## PostgreSQL's rule

`transformCoalesceExpr` / `transformMinMaxExpr` (parse\_expr.c) call
`select_common_type` (parse\_coerce.c), then `coerce_to_common_type` on every
argument.

- **Seeding.** Untyped (unknown) arguments are skipped. If every argument is
  untyped, the type is text. Otherwise the first typed argument sets the
  type.
- **Across categories.** A later argument of another category is an error,
  positioned at that argument.
- **Within a category.** The type moves to a later argument's type when the
  current type is not the category's preferred type and converts to the
  later one implicitly, but not back.
- **Evaluation.** Evaluation is ordinary: an argument's error propagates.

## Design

- **`coerceCommonTypeArgs`** (`internal/optimizer/common_type.go`) runs on
  the resolved FuncCall at the three places one is built: `resolveExpr`,
  `resolveExprAfterAggregate`, and the window-surface resolver.
  - It normalises exprType's spellings (integer, bigint, double precision,
    character varying, timestamp with time zone, …) to internal names.
  - It walks the types with pg\_type categories and preferred types:
    - numeric: int2 / int4 / int8 / numeric / float4, with float8 preferred;
    - string: varchar / bpchar / name, with text preferred;
    - datetime: date / timestamp / time / timetz, with timestamptz preferred;
    - timespan and boolean.
  - Implicit conversions are pg\_cast's implicit and binary-coercible entries
    among those types.
  - Every argument whose type differs, untyped literals included, is wrapped
    in a planner-inserted `CastExpr` (not explicit, so EXPLAIN does not show
    it).
  - If every argument is NULL, the first one carries a cast to text.
  - A mix across categories raises 42804 `COALESCE types X and Y cannot be
    matched` at the offending argument. A non-convertible pair raises 42846.
  - A type outside the modelled categories, or an array, leaves the call
    exactly as it was.
- **Result types.** Both result-type functions, `exprType` and
  `funcCallResultType`, already read the first typed argument. They now see
  the common type through the casts, so neither changed.
- **Error propagation.** The executor evaluators for COALESCE, NULLIF,
  GREATEST, LEAST, num\_nulls and num\_nonnulls propagate an argument's
  error.

## Verification

- **Probe against PG 18.3** (`tmp/m0126/probe.sql`, 19 statements: constants,
  columns, bigint / smallint / float8 / unknown literals, all-NULL, mismatch
  errors, CREATE TABLE AS, UNION). It is identical to PG apart from two
  pre-existing differences, both present at HEAD and filed:
  - float8 division prints 15 digits (M0146-0164);
  - `now()` is typed `timestamp without time zone` (M0146-0163), which
    changes the wording of one mismatch error.
- **Tests.**
  - `TestCoalesceCommonType` (postmaster) fails on HEAD in 9 places.
  - `TestCoerceCommonTypeArgs` (optimizer) covers the type walk.
- **Regress A/B** over 25 expression cases: identical, except `join`, which
  returns the same five rows in another order for an unordered query, and
  `plpgsql` (run-context noise).
- **Gates.** Units; tpch-spotcheck; the acceptance arm (values identical);
  sf025 PASS=99; the TPC-DS fire-set gate (no plan changed).

## Residuals

- **Filed:**
  - M0146-0163: `now()` / `current_timestamp` typed timestamp, not
    timestamptz.
  - M0146-0164: float8 division output has 15 significant digits.
- **Ledgered:**
  - Types outside the modelled categories (oid, user enums, domains, arrays,
    ranges) keep first-argument typing.
  - The generated-column evaluator (`evalGenExpr`, parser AST) still skips
    failing arguments and applies no common type.
  - CASE and UNION use their own partial common-type resolvers, which this
    change does not unify.
