# Milestone 0051 — Planner expression-level improvements

**Status:** planned
**Depends on:** root-0010 (parser), root-0011 (planner / catalog seam), Milestone 0033 (subquery unnesting — accepted), Milestone 0006 (planner statistics — selectivity hooks improve once histograms exist, but this milestone does not require them).
**Drives:** Close four expression-level planner gaps identified in `docs/reference/ref-010-parser.md` and `ref-011-planner.md` that show up as either annoying user-facing errors (false type-mismatch) or as missed-optimisation patterns (constant runtime evaluation, `LIKE 'prefix%'` always seqscan, identifier collisions with reserved words).

## 1. Context

The TPC-H result-parity work in M0041 closed correctness gaps but left four expression-level pain points that any non-toy workload notices within the first day:

1. **All keywords reserved.** goopg's lexer does not categorise keywords. A column or table called `name`, `value`, `type`, or `user` is rejected at parse time. Real schemas (especially app-framework-generated DDL) routinely use these names. Upstream's parser splits keywords into `reserved`, `unreserved`, `col_name`, and `type_func_name` and accepts each in the contexts where they cannot conflict.
2. **No constant folding.** `WHERE x > 1 + 2` evaluates `1 + 2` once per row at runtime. `WHERE 'foo' || 'bar' = name` likewise. Cheap to fix; immediate per-tuple win on every workload.
3. **No LIKE-to-range translation.** `WHERE col LIKE 'foo%'` cannot use a B-tree on `col` because the planner does not understand that the predicate is equivalent to `col >= 'foo' AND col < 'fop'`. TPC-H Q14 / Q16 / Q20 are all heavily affected.
4. **No implicit type coercion.** `int < numeric` errors out with "type mismatch" even though upstream silently coerces. The same for `int2 + int4 → int4`, `text || varchar → text`, etc. Forces users to write explicit casts everywhere.

Subquery flattening, predicate simplification, and a handful of related rewrites also live in this neighbourhood and are inexpensive once the constant-folding rewriter exists.

## 2. Required Design Docs

1. `docs/design/0051-0001-keyword-categorisation.md` — split goopg's keyword table into `reserved` / `unreserved` / `col_name` / `type_func_name`. Lexer returns the keyword token; parser-level "is this keyword usable as an identifier here?" predicate gates each ambiguous spot. Upstream cite: `postgres/src/include/parser/kwlist.h` (the canonical table).
2. `docs/design/0051-0002-constant-folding.md` — analyzer-stage rewriter: walk every expression tree, evaluate every node whose inputs are all literals, replace with a literal of the result's type. Includes string concatenation, numeric arithmetic, boolean simplification (`a AND TRUE → a`), comparison folding (`1 < 2 → TRUE`).
3. `docs/design/0051-0003-implicit-type-coercion.md` — analyzer-stage type-promotion using upstream's coercion lattice (`int2 → int4 → int8 → numeric → float4 → float8`; `text ↔ varchar ↔ char(n)`). Operator overload lookup walks the lattice when the exact-match operator is missing. Errors only when no coercion makes both sides agree.
4. `docs/design/0051-0004-like-to-range.md` — pattern-analysis pass: when a `LIKE` pattern starts with a fixed prefix and contains no other operator-meaningful escape inside the prefix, translate `col LIKE 'pre%'` into `col >= 'pre' AND col < 'prf'` *as an additional* index-eligible predicate (the original `LIKE` stays for tail-matching correctness). Planner sees the range predicate during index-scan selection.

## 3. Definition of Done

### 3.1 Keyword categorisation
- New per-keyword classification in `internal/parser/keywords.go` matching upstream's three-way split (reserved / col_name-OK / unreserved).
- A user-defined column named `name`, `value`, `type`, `user`, `data`, `time`, `month`, `year` (and the upstream "col_name keyword" set) parses without quoting.
- Regression test: a fresh CREATE TABLE that uses each col_name keyword as a column name compiles and round-trips.

### 3.2 Constant folding
- Analyzer rewriter pass folds: arithmetic on numeric literals, string concatenation on string literals, boolean operators with literal operands, comparisons of two literals, `CAST(literal AS T)` to a literal of T.
- Folding is post-coercion (so `1 + 2.0` first becomes `1::numeric + 2.0::numeric` then folds to `3.0::numeric`).
- Regression test: `EXPLAIN SELECT * FROM t WHERE x > 1 + 2` shows the filter as `x > 3` (not `x > (1+2)`).

### 3.3 Implicit type coercion
- Operator lookup at analyzer time walks the coercion lattice when the exact-type operator is missing.
- TPC-H queries that today require explicit `CAST(...)` to compile run unchanged after dropping the casts.
- Regression test: matrix of `(numeric, integer, smallint, real, double precision)` op-pair tests; each accepts both directions and produces the upstream-canonical result type.

### 3.4 LIKE-to-range
- `LIKE 'prefix%'` (prefix free of `_` / `\` and not entirely composed of `%`) generates an additional `col >= 'prefix' AND col < 'prefix\xff'`-style range predicate.
- Planner picks IndexScan when the column has a B-tree.
- TPC-H Q14 / Q20 against pgbench/HammerDB-shape data: IndexScan plan instead of SeqScan.
- Regression test: explain test asserts IndexScan on a B-tree-indexed `varchar` column with a fixed prefix predicate.

### 3.5 No regression
- `make ralph-state-guard` green every loop.
- `TestRunTPCHQueriesAgainstSyntheticData` 22/22 unchanged.
- `TestTPCHResultParity` identical=22 divergent=0 errored=0 unchanged.

## 4. Out of scope

- Cost-based optimiser overhaul / GEQO. Tracked under M0006.
- Predicate-pushdown across joins beyond what already exists. Tracked under M0043's neighbourhood.
- Genuine query-rewrite (e.g. CTE → derived-table inlining) — bigger surface, separate milestone.
- Citus-style distributed planner extensions. Out of project scope.

## 5. Reference

- `postgres/src/backend/parser/kwlist.h`, `parse_func.c`, `parse_oper.c` — keyword categories, operator-overload coercion.
- `postgres/src/backend/optimizer/util/clauses.c` — `eval_const_expressions`.
- `postgres/src/backend/utils/adt/like_match.c`, `like.c`, `selfuncs.c` — pattern-prefix → range derivation (`patternsel`, `make_greater_string`).
- `postgres/src/backend/parser/parse_coerce.c` — coercion lattice.
- `docs/reference/ref-010-parser.md`, `ref-011-planner.md` — gap inventory.
- `docs/design/root-0010-parser.md`, `root-0011-planner.md`.
