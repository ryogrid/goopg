# M0134-0156 — VALUES column type resolution (`select_common_type`) and the single `FigureColname`

Status: **landed 2026-08-29**. `psql_crosstab.sql` promoted `not-tried` → `pass`
(100% parity, `scripts/pg-regress-runner.sh --verbose psql_crosstab`).

## What the regress case actually exercised

`postgres/src/test/regress/sql/psql_crosstab.sql` is nominally a test of psql's
client-side `\crosstabview`. Because goopg is tested with the *real* psql from
`postgres/local_install/bin`, the client half was never in question — every one
of the file's 65 diverging lines came from two server-side planner bugs, both
reached through its fixture:

```sql
CREATE TABLE ctv_data (v, h, c, i, d) AS
VALUES
   ('v1','h2','foo', 3, '2015-04-01'::date),
   ('v2','h1','bar', 3, '2015-01-02'),
   ...
```

and its first query, `SELECT v, EXTRACT(year FROM d), count(*) ... GROUP BY 1, 2`.

## Bug 1 — VALUES column types ignored PG's `select_common_type`

Only the first row's `d` carries `::date`; the rest are unadorned string
literals. PostgreSQL resolves the column with `select_common_type`
(`postgres/src/backend/parser/parse_coerce.c:1342`, called from
`transformValuesClause`, `parse_clause.c:1675`), whose defining property is that
**`UNKNOWNOID` candidates are skipped**: a bare literal is still unknown at that
point, so any row supplying a real type wins regardless of position, and the
literals are coerced to it afterwards. Only if *every* input is unknown does the
column resolve to `TEXT` (`parse_coerce.c:1451`).

goopg had two independent, both-wrong implementations of this — a textbook
instance of the "sibling paths must agree" failure mode:

| path | function | behaviour before |
|---|---|---|
| standalone `VALUES ...` | `planStandaloneValuesSelect` | unified across all rows, but `exprType(*StringConst)` is `"text"`, and `unifyValueTypes` treats text as the sink — so `date` + literal collapsed to `text` |
| `(VALUES ...) AS t(c)` in FROM | `planValuesSubquery` | took the **first row's type only**; no unification at all |

They masked each other: the subquery form happened to answer `date` for the
fixture (its typed row is first) while the standalone form — which is what
`CREATE TABLE ... AS VALUES` plans through — answered `text`. Reversing the row
order flipped which one was wrong.

The user-visible damage was worse than a wrong `pg_typeof`: with the column
typed `text`, the already-typed `date` datum was *stringified through DateStyle*
on insert, so `'2015-04-01'::date` landed in the table as the literal text
`04-01-2015`, and `EXTRACT(year FROM d)` then failed outright with
`EXTRACT(year FROM …) requires timestamp/date input` (`internal/executor/expr.go:7343`)
because `parseCopyTimestamp` cannot read `04-01-2015`.

### Fix

`internal/optimizer/planner.go`:

- `valuesCandidateType(e Expr)` — reports a bare `*StringConst` as `"unknown"`,
  standing in for PG's UNKNOWNOID literal. (`*NullConst` already reported
  `unknown` via `exprType`.)
- `resolveValuesColumnType(planRows, col)` — folds `unifyValueTypes` over
  *every* row starting from `unknown`, then applies the all-unknown → `text`
  fallback.
- Both `planStandaloneValuesSelect` and `planValuesSubquery` now call it, so the
  two paths cannot drift again.

`exprType` itself is deliberately unchanged: `*StringConst` → `text` is correct
everywhere else, and the unknown-literal notion only exists inside common-type
resolution.

## Bug 2 — GROUP BY had its own private `FigureColname`

With bug 1 fixed the case still failed on one line: the column header read
`?column?` where PG prints `extract`.

PostgreSQL has exactly one implicit-column-label routine, `FigureColname`
(`parse_target.c`), and grouping does not consult a different one — `SELECT
EXTRACT(year FROM d), count(*) ... GROUP BY 1` is labelled `extract` just as it
is without the `GROUP BY`. goopg's equivalent is `targetMeta`, which already had
an `*ExtractExpr` → `"extract"` arm (M0097-0004). But the aggregate planner
built its output schema from `groupExprName`, an independent mini-copy that knew
only `*ColumnRef` and `*FuncCall` and sent everything else to `?column?`. So the
label silently degraded the moment a `GROUP BY` appeared — for `ExtractExpr`,
`CASE`, typed literals and scalar subqueries alike.

### Fix

`groupExprName` now delegates to `targetMeta(e, parser.ResTarget{})` instead of
duplicating it. `targetMeta` tolerates the zero `ResTarget` (empty alias, nil
`Expr`), and this restores the single-routine invariant PG has.

## Verification

`scripts/pg-regress-runner.sh` before/after over 20 VALUES/GROUP-BY-adjacent
regress files (`select aggregates groupingsets union case insert subselect join
with limit select_having select_implicit select_distinct int4 int8 numeric date
timestamp text arrays`) — same PASS/FAIL set, and **two files improved**:

- `numeric.diff` 3830 → 3828: VALUES columns that are now correctly numeric are
  right-aligned by psql instead of left-aligned as text.
- `join.diff` 20939 → 20924: the `lateral (values(a.unique1),(-1))` plan lost a
  *duplicated* `Join Filter: (b.unique2 = x)` that restated its own `Hash Cond`,
  and a VALUES-driven join's row order now matches expected. `count(*)` results
  are unchanged (10000, matching PG).

No file regressed.

Regression tests: `internal/optimizer/values_common_type_test.go` —
`TestValuesColumnTypeSelectCommonType` (typed row first *and* last, in both the
standalone and subquery spellings, plus the all-unknown → text and
NULL-literal cases) and `TestGroupByTargetLabelMatchesUngrouped` (asserts the
grouped and ungrouped labels are equal, which is the actual PG invariant).

## Deferred

`goopg types every integer literal as int8` where PG uses int4 for values that
fit (`exprType(*IntegerConst)` → `"int8"`). This is pre-existing and unrelated to
the fixture's assertions, but common-type resolution now *propagates* it:
`(VALUES (a_int4_col), (-1))` resolves to `int8` in goopg and `int4` in PG. Under
the old first-row-only rule the subquery path accidentally answered `int4` here.
Ledgered — see `.ralph/deferral_ledger.md`, 2026-08-29, M0134-0156.
