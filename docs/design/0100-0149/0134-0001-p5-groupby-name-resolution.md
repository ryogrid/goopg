# 0134-0001 P5 — GROUP BY name resolution: FROM columns outrank target-list aliases

- **Status:** accepted
- **Date:** 2026-08-17
- **Task:** M0134-0001 (`aggregates.sql`), slice S13
- **Related:** `0134-0001-aggregates-sql-divergence.md` (parent divergence doc),
  `.ralph/deferral_ledger.md` row 1382 (the symptom, filed 2026-08-15 without the
  mechanism — this doc supplies it)

## Symptom

`postgres/src/test/regress/expected/aggregates.out:1544-1547`:

```sql
-- Test handling of sublinks/aggregates in the GROUP BY clause
select t1.f1 from t1 left join t2 using (f1) group by f1;
ERROR:  column "t1.f1" must appear in the GROUP BY clause or be used in an aggregate function
```

goopg accepts the statement and returns `(0 rows)`. This is an **over-permit**:
a genuine correctness defect, not a formatting divergence. It owns exactly one
hunk (`@@ -1363,13 +1287,13 @@`) of the 30 residual hunks in the `aggregates`
case diff.

## What PG actually does

`postgres/src/backend/parser/parse_clause.c:2006 findTargetlistEntrySQL92`, reached
from `transformGroupClauseExpr` (`:2367-2379`). For an unqualified single-name
group item, PG resolves the name in **two ordered attempts**, and the order is
expression-kind dependent (`:2056-2076`):

```c
if (exprKind == EXPR_KIND_GROUP_BY) {
    if (colNameToVar(pstate, name, true, location) != NULL)
        name = NULL;        /* FROM-clause column wins; skip alias matching */
}
```

- **ORDER BY / DISTINCT ON**: the target-list *output name* (alias) wins.
- **GROUP BY**: the **FROM-clause input column** wins; the target-list alias is
  only a fallback when the name is not a visible input column. (SQL92 vs SQL99
  legacy; PG documents the asymmetry in `parse_clause.c`'s comment block.)

For the failing statement, `USING (f1)` creates a **merged join pseudo-column**
`f1` — a Var distinct from both `t1.f1` and `t2.f1`. It is a visible FROM-clause
name, so `colNameToVar` succeeds, the alias path is skipped, and PG groups by the
*merged* Var. `t1.f1` in the target list is then a different Var, ungrouped, and
`check_ungrouped_columns` (`postgres/src/backend/parser/parse_agg.c`) rejects it
with 42803.

`check_functional_grouping` (`postgres/src/backend/catalog/pg_constraint.c:1740`)
does not rescue it: `t1` has no primary key, and the functional-dependency
relaxation only licenses direct Vars of the *grouped* range table anyway — never a
Var of a different RTE than the one the grouping key came from.

## Root cause in goopg

goopg already has the correct downstream guard. `resolveExprAfterAggregate`
(`internal/optimizer/planner.go:7200-7205`, `*parser.ColumnRef` case, landed as
M0097-0155) rejects a *qualified* SELECT reference when the GROUP BY named the
unqualified USING-merged column. Its input signal is `groupByMergedByName`,
populated in `buildAggregateStage` at `planner.go:6554-6568` — a loop that
requires `cr.Table == ""`.

The guard never fires because that signal is destroyed one statement earlier:

```go
// planner.go:6515
g = resolveOrderBySubstitution(g, s.Targets)
```

`resolveOrderBySubstitution` (`planner.go:5497-5527`) performs the **ungated**
output-name/alias match — correct for ORDER BY, which is its other seven call
sites (`planner.go:1399,1446,1742,1765,1852,5885,6013`), and wrong for GROUP BY.
It rewrites the bare `f1` into the target list's `t1.f1` before the USING-merge
tracking loop ever inspects it. By the time line 6556 runs, `cr.Table == "t1"`,
so `groupByMergedByName` stays empty and the M0097-0155 guard is starved.

goopg is therefore missing exactly PG's `EXPR_KIND_GROUP_BY` gate, not the
downstream 42803 check.

## Design

Add PG's gate at the single GROUP BY call site. Do **not** change the shared
helper — the ORDER BY / DISTINCT ON call sites already implement PG's correct
(ungated) behaviour and must stay byte-identical.

In the `for _, g := range s.GroupBy` loop of `buildAggregateStage`, before line
6515: if `g` is a `*parser.ColumnRef` with `Table == ""` (an unqualified single
name — the only shape the alias path can rewrite) **and** that bare name is
visible as an input column of `inputCtx` (an ordinary relation column or a
USING-merged pseudo-column), skip the substitution and let `resolveExpr` bind the
FROM column directly. Otherwise the existing substitution runs unchanged.

This is deliberately a *name-visibility* probe, not a full resolve: it must not
raise on an ambiguous name. PG's `colNameToVar` does raise on ambiguity, but in
goopg the same ambiguity is reported by the immediately following `resolveExpr`
call on the same expression, so a probe that answers "visible (possibly
ambiguously)" and defers to `resolveExpr` produces the same user-visible error.

Non-goals of this slice (unchanged by construction):
- **Positional GROUP BY** (`GROUP BY 1`) — an `*parser.IntegerConst`, not a
  `ColumnRef`, so the gate never sees it and `resolveOrderBySubstitution` still
  handles it, as does the out-of-range 42P10 check at `planner.go:6517`.
- **Genuine alias GROUP BY** (`SELECT a+b AS x … GROUP BY x`, which TPC-H Q7
  relies on) — `x` is not a FROM column, the probe fails, substitution proceeds.
- **Qualified group items** (`GROUP BY t1.f1`) — never took the alias path.

### Behaviour change beyond the failing case

The gate also changes a legal-in-both-engines case: when a target-list alias
*shadows* a real FROM column name, e.g.

```sql
SELECT a AS b FROM t GROUP BY b;   -- t has columns a and b
```

goopg previously grouped by `t.a` (alias wins); it will now group by `t.b`
(FROM column wins), matching PG. That is the intended correction, and it is why
the guard tests below cover the shadowing shape explicitly rather than only the
USING shape.

## Verification

- FAIL-pre / PASS-post: the USING-merge statement must produce 42803 with PG's
  exact message text (`column "t1.f1" must appear in the GROUP BY clause or be
  used in an aggregate function`).
- The three preceding statements in the same `aggregates.sql` block must keep
  succeeding — they are the "still legal" side of the same boundary.
- Alias-shadowing case grouped by the FROM column.
- Positional and non-shadowing-alias GROUP BY unchanged.
- Case gates: `aggregates` (the owning hunk closes), plus `functional_deps` and
  `groupingsets` as blast-radius sentinels.

## Blast radius

One hunk (~12 lines) of the 1096-line `aggregates` diff. No existing goopg test
exercises the over-permissive pattern (grepped across `internal/**/*_test.go`).
The alias-shadowing change is the only way this can reach unrelated cases, hence
the two sentinel regress cases.
