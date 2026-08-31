# M0134-0180 — `tsrf.sql`: sizing + one contained fix (SRF rejection in LIMIT/OFFSET)

Status: **PARKED** (case remains genuinely `failed`; one independent, bounded
root cause fixed; the dominant residual gap is a REFACTOR-tier SRF execution
model — see below).

## What the file tests

`postgres/src/test/regress/sql/tsrf.sql` is PG 18.3's "tSRF" (target-list
set-returning function) regression case: 185 lines exercising every placement
rule PostgreSQL enforces for a set-returning function (`generate_series`,
`unnest`, a user-defined SRF operator) that appears in the SELECT target
list, rather than in `FROM`. Unlike a typical M0134 case this is not really
one gap with a long tail of unrelated errors — it is ~10 largely independent
placement rules, each ported from a different part of
`postgres/src/backend/parser/{parse_expr.c,parse_func.c,parse_agg.c}` and
`src/backend/executor/nodeProjectSet.c`.

## Sizing (this loop, 2026-09-01)

`scripts/pg-regress-runner.sh -v tsrf`: **793 → 785 diff lines, `^-` 235 →
232, `^+ERROR` unchanged at 10** (0% parity either way — the file has 90+
independently-checked statements, so a handful of matching lines don't move
the pass/fail needle, but the diff-line delta is the real progress metric
per the M0134-0179 lesson: never park on line count alone, track which
*specific* assertions flip).

### What already works (no diff at all against PG 18.3)

Confirmed via live reproduction, not just diff inspection:

- **Sibling-SRF lockstep zipping**: `generate_series(1,3) AS x, generate_series(3,6)+1 AS y` —
  goopg already zips independent same-nesting-level SRFs to the longest,
  NULL-padding the shorter ones exactly like PG's `nodeProjectSet.c`. This is
  `internal/executor/operators_project_set.go`'s `openSelectSrfMode`
  (`maxLen` computed across all SRF column slices, lines ~121-303).
- **SRFs computed after aggregation, when not referenced by GROUP BY**: e.g.
  `SELECT few.dataa, count(*), ..., unnest('{1,1,3}'::int[]) ... GROUP BY few.dataa`
  is byte-identical to the oracle.
- **SRFs in ORDER BY**, **DISTINCT ON placement rules**, **GROUP BY
  CUBE + SRF**, **HAVING with/without SRF in GROUP BY** — all unchanged in
  the diff.

### Landed this loop: SRF rejection in LIMIT/OFFSET

PG's `transformLimitClause` (`parse_clause.c`) runs SRF-placement checking
(`parse_func.c:2500-2680`, keyed on `EXPR_KIND_LIMIT`/`EXPR_KIND_OFFSET`)
*before* coercing the expression to a numeric type, so
`SELECT 1 LIMIT generate_series(1,3)` raises `set-returning functions are
not allowed in LIMIT` even though `generate_series` is int8-typed (which
would otherwise satisfy goopg's plain `isIntegerLike` check). goopg had no
such check at all: the LIMIT/OFFSET analyzer block
(`internal/parser/analyzer/analyzer.go`, `analyzeSelect`) only type-checked
the resolved expression, so the SRF silently evaluated via the executor's
scalar-function fallback for `generate_series`
(`internal/executor/expr.go` — "generate_series used as a scalar
expression... returns the start value only") and produced a wrong,
single-row result instead of an error.

Fix: a new `exprHasSRF(e parser.Expr, cat catalog.Catalog) bool` walker in
`internal/parser/analyzer/analyzer.go`, structurally identical to the
existing `exprHasWindowFunc` (same node coverage: BinaryOp/UnaryOp/
CastExpr/ExtractExpr/CaseExpr/IsNullExpr/IsBoolExpr/CollateExpr/
IsDistinctFromExpr/InExpr/FuncCall), checking each `FuncCall` name against a
small builtin-SRF name set (mirrors, but cannot import — layering: executor
imports analyzer, not the reverse — `internal/executor/
operators_ddl_partition.go`'s `isBuiltinSRF`) plus a `Routines().LookupByName`
catalog check for user-defined `ReturnsSet` routines. Wired into the
existing `s.Limit`/`s.Offset` blocks in `analyzeSelect`, raising PG's exact
wording (`set-returning functions are not allowed in LIMIT`/`OFFSET`,
SQLSTATE `0A000`) before the type check runs.

Guard: `internal/parser/analyzer/analyzer_test.go` —
`TestAnalyzeSRFInLimitOffsetRejected` (builtin `generate_series` in both
LIMIT and OFFSET, plus a user-defined `ReturnsSet` routine) and
`TestAnalyzeLimitOffsetNonSRFStillAccepted` (a plain scalar function and a
plain integer literal must still analyze cleanly — regression guard for the
new gate over-triggering).

## What's still broken (PARKED, ledgered)

Everything else in the file traces to one of three genuinely separate,
REFACTOR-tier gaps — none is a small follow-up to the LIMIT/OFFSET fix:

1. **Nested SRF as another SRF's own argument doesn't expand at all.**
   `SELECT generate_series(1, generate_series(1, 3))` should cross-multiply
   to 6 rows; goopg produces 1 (root-caused exactly: the inner
   `generate_series(1,3)` is resolved as an ordinary scalar sub-expression,
   which the executor's `generate_series`-as-scalar fallback reduces to its
   first argument, `1` — so the outer series becomes `generate_series(1,1)`).
   This needs a **recursive/stacked ProjectSet** — one nesting level per
   SRF-inside-an-SRF-argument, matching PG's `transformTargetList`/executor
   stacking — not a fix to the existing flat "zip siblings to maxLen" model.
2. **GROUP BY referencing a target-list SRF changes pre-aggregation
   cardinality.** `... GROUP BY few.dataa, unnest('{1,1,3}'::int[])` must
   expand the SRF *before* aggregating (PG groups the per-element unnested
   values, changing `count(*)` per group) — an entirely different placement
   rule from "SRFs run after aggregation when NOT referenced by GROUP BY",
   which already works. goopg's `resolveExprAfterAggregate` finds no match
   for the raw SRF-containing GROUP BY key and falls through to plain
   scalar-function resolution, which errors `function unnest does not
   exist` (unnest is never registered as an ordinarily-callable function —
   it is only special-cased inside `buildSelectSrfProjectSet`). Confirmed
   live: this reproduces byte-for-byte even for `GROUP BY few.dataa, 5`
   (grouping by the SRF column's *ordinal*), so the bug is the GROUP BY-key
   resolution path, not name lookup specifically.
3. **Six more "set-returning functions are not allowed in X" contexts are
   unimplemented**: CASE, COALESCE, aggregate-call arguments, window-call
   arguments, UPDATE SET, RETURNING, and standalone VALUES. Unlike
   LIMIT/OFFSET (a single well-isolated two-call-site hook with no existing
   precedent needed), aggregate/window-argument checking has **zero
   precedent in goopg today** — it needs a `parse_agg.c`-style
   "am I directly inside this aggregate/window's own arguments, not inside
   a nested sub-select" walker, and CASE/COALESCE need a hook inside
   whatever resolves a target-list expression generally (not just after
   GROUP BY, since these can appear ungrouped too). Scoped as its own
   follow-up task (ledgered `0180a`) rather than folded in here, to keep
   this loop's diff bounded and independently revertable.
4. **`|@|` as a user-defined prefix operator over `unnest` fails at parse
   time, unrelated to SRFs.** The lexer already tokenizes `|@|` as one
   `TokenOperator` (the M0134-0179 maximal-munch scanner handles this
   correctly). The failure is that both parser front ends (hand-written
   `internal/parser/select.go:parseUnary` and the yacc grammar's
   `internal/parser/support.go:prefixOp`) hard-code the PREFIX-operator set
   to exactly `{-, +, NOT, ~}` — by design (comment: "widening past legacy
   is a behaviour change, not a port"). `CREATE OPERATOR |@|
   (PROCEDURE = unnest, RIGHTARG = ANYARRAY)` registers fine; using it as
   `|@|ARRAY[1,2,3]` never reaches operator resolution because the parser
   rejects the token before then. This is the same class of gap as the
   already-ledgered closed-`OpCode`-enum / no-`pg_operator`-lookup follow-up
   from M0134-0179 — generalizing prefix-operator parsing to consult the
   registered-operator catalog is its own task.
5. **Correlated `LIMIT ... OFFSET <outer column>` inside a scalar
   subquery** (`SELECT (SELECT generate_series(1,3) LIMIT 1 OFFSET few.id)
   FROM few`) fails with `column "few.id" does not exist` /
   `OFFSET must be an integer expression` — LIMIT/OFFSET clause resolution
   apparently doesn't thread the outer query's correlated scope the way a
   WHERE-clause subquery does. Unrelated to items 1-4; not investigated
   further this loop (out of scope, own resume point).

## Resume points

- Ledger row **0180a** (aggregate/window-arg SRF rejection + CASE/COALESCE +
  UPDATE/RETURNING/VALUES): start from `collectAggregateCalls`/
  `collectWindowCalls` (`internal/optimizer/planner.go:8078`/`6334`) for the
  aggregate/window-arg walk (their existing per-arg `exprHasAggregate`
  nesting check is the right shape to extend), and `analyzeSelect`'s
  `s.Returning`/UPDATE-target analysis (`internal/parser/analyzer/
  analyzer.go`) for RETURNING/UPDATE SET.
- Ledger row **0180b** (nested-SRF-of-SRF cross-multiply + GROUP BY-SRF
  cardinality change): both need the same underlying primitive — a
  *recursive* SRF-expansion model in `buildSelectSrfProjectSet`/
  `ProjectSet`/`openSelectSrfMode`, not a bug fix to the existing flat one.
  Do not attempt either as an isolated patch; they are two symptoms of the
  planner only supporting one level of target-list SRF expansion.
- Ledger row **0180c** (generalized prefix-operator parsing): resolve
  together with the already-ledgered M0134-0179 closed-`OpCode`-enum /
  `pg_operator` catalogue-lookup follow-up — both are "the parser's operator
  handling is a closed spelling set, not a catalogue lookup."
- Ledger row **0180d** (correlated LIMIT/OFFSET): resume at wherever
  LIMIT/OFFSET's `resolveExpr` is called for a scalar subquery
  (`internal/optimizer/planner.go:617/1625/3775`) and compare against how a
  correlated WHERE clause threads the outer `resolveContext`.

## Gates run

- `go build ./...` clean.
- `go test ./internal/parser/...` (incl. `TestParityGoldensAreCurrent` — zero
  golden-corpus diff, confirming no other query's parse/analyze behavior
  moved) PASS.
- `go test ./internal/optimizer/...`, `go test ./internal/executor/...` PASS
  (no sibling-path regression from the new analyzer check).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS.
- `scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=34 — required for
  analyzer/planner-adjacent changes).
- Live repro against a throwaway capped server: `SELECT 1 LIMIT
  generate_series(1,3)` / `... OFFSET generate_series(1,3)` now raise
  `0A000`; `SELECT * FROM few LIMIT 1 OFFSET 0` and `SELECT generate_series
  (1,5) LIMIT 3` (non-SRF and SRF-as-a-legitimate-target-list-expression,
  respectively) are unaffected; the unrelated correlated-OFFSET bug (item 5
  above) reproduces identically before and after.
