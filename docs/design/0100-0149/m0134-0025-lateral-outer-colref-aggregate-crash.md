# M0134-0025 — LATERAL correlated column reference in an aggregating subquery crashes the backend

Status: implemented (2026-08-20)
Milestone: M0134 (`groupingsets.sql` regress-sql digestion)
Task: M0134-0025

## Summary

A correlated (outer-level) column reference appearing in the target list of a
subquery that contains an aggregate call panicked the goopg backend, killing the
client connection. The bug is **engine-wide** — it has nothing to do with
grouping sets; `groupingsets.sql` merely happened to be the first regress file in
the sweep that combined *LATERAL subquery* + *aggregate* + *correlated reference
in the target list*.

## Symptom

```sql
create table onek (four int, ten int);
insert into onek select i%4, i%10 from generate_series(1,20) i;
select * from (values (1),(2)) v(a)
  left join lateral (select v.a, four, count(*) from onek group by four) s on true
  order by v.a, four;
```

→ `server closed the connection unexpectedly`. Server log:

```
panic="interface conversion: optimizer.Expr is *optimizer.OuterColumnRef, not *optimizer.ColumnRef"
internal/optimizer.resolveExprAfterAggregate  planner.go:7386
internal/optimizer.buildSelectSrfProjectSet   planner.go:4186
```

The same panic reproduces with **no GROUP BY at all**
(`select v.a, count(*) from onek`), because `buildSelectSrfProjectSet` runs for
any SELECT containing an aggregate call.

## Root cause

`resolveColumnRef` (`internal/optimizer/planner.go:13288-13334`) walks the
lexical scope chain exactly like PG's `varlevelsup`: a match at `level == 0`
returns a plain `*ColumnRef` addressing the local input row; a match at any
parent level returns an `*OuterColumnRef{Level, Index}` whose `Index` addresses
`ctx.OuterRows[Level]` — **a different addressing space entirely**.

`resolveExprAfterAggregate`'s `case *parser.ColumnRef:` branch
(`planner.go:7381`) performed an unconditional type assertion
`col := resolved.(*ColumnRef)` and then used `col.Index` as a key into
`agg.groupByInputCol`, `agg.funcDepCols`, `agg.prunedInputCols` and
`agg.node.Passthrough` — all of which are indexed against `agg.input`'s **local**
child schema. There was no branch for `*OuterColumnRef`, so the assertion
panicked.

## PG oracle

`postgres/src/backend/parser/parse_agg.c:check_ungrouped_columns` — PG's
equivalent grouping-surface check explicitly **skips any Var whose
`varlevelsup != 0`** relative to the query being checked. An outer-level Var is
by construction not part of the query being aggregated; it is constant for the
whole evaluation of this subquery instance (fixed per outer row), so no
grouping-membership proof is required or even meaningful.

## Fix

Return an `*OuterColumnRef` unchanged, before the cast — no
`groupByInputCol`/`funcDepCols` lookup, no `Passthrough` registration, since all
of that machinery exists solely to prove *local* GROUP BY membership.

This mirrors the idiom already established in the same file for the plain,
non-aggregated resolver (`planner.go:13035-13036`):

```go
case *parser.ColumnRef:
    return resolveColumnRef(x, ctx)   // returns ColumnRef OR OuterColumnRef, unchanged
```

and matches the `*OuterColumnRef` special-cases already present at
`planner.go:6108` and `planner.go:9748`.

## Sibling-site audit (Hard-won Rule #2)

Every hard (non-`ok`-checked) `.(*ColumnRef)` assertion in `internal/optimizer/`
was audited:

| site | context | reachable with a correlated outer ref? |
|---|---|---|
| `planner.go:7386` | `resolveExprAfterAggregate` | **YES — this bug** |
| `planner.go:7580` | `resolveTargetsAfterAggregate`, star expansion via `expandStarTarget` | No — a bare `*`/`t.*` expands only against local FROM bindings |
| `planner.go:13785` | index-only-scan covered-column remap over already-planned local `Project` targets | No — unrelated subsystem |
| `cte_inline_pushdown.go:304` | CTE inline substitution over a CTE's own resolved target list | No — unrelated subsystem |

The one soft-checked lookalike, `groupBySlotContested` (`planner.go:7258`),
returns `found=false` for an `OuterColumnRef` and routes the caller back into the
same `7386` site — it converges on this crash rather than opening a second path.

**Verdict: one branch, not a family.**

## Guard test

A "does not panic" assertion is too weak — it would pass if the fix silently
produced NULL or the wrong outer row. The guard asserts real values, verified
byte-for-byte against a scratch real PG 18.3 instance:

- `LATERAL (select v.a, four, count(*) from onek group by four)` → 8 rows
  `(1,1,0,5) (1,1,1,5) (1,1,2,5) (1,1,3,5) (2,2,0,5) (2,2,1,5) (2,2,2,5) (2,2,3,5)`
- `LATERAL (select v.a, count(*) from onek)` (no GROUP BY) → 2 rows
  `(1,1,20) (2,1,20)`

Because the bug is engine-wide, the guard lives with LATERAL/aggregate coverage,
not in grouping-sets-specific tests.

## Case status

`groupingsets.sql` remains `failed`. Measured: **2373 -> 2689 diff lines, 25 ->
41 `^+ERROR`, 6 -> 6 `^-ERROR`.**

**The counts going UP is the expected shape of progress here, and was proven so
by a stash A/B rather than assumed.** Before the fix, pg_regress collapsed the
~1480 statements after the crash into a single "expected but missing" hunk
(`@@ -1079,1482 +892,9 @@`) — those statements never executed, so they could not
contribute their own mismatches. With the crash gone they now run and each
reports its own (mostly pre-existing) feature gap.

A/B evidence (planner.go stashed, case re-run, stash popped):

- the pre-crash region that executed in BOTH runs (before-diff lines 1-886) is
  **byte-identical**, with the same 25 `+ERROR` / 2 `-ERROR`;
- all 16 new `+ERROR` and all 4 new `-ERROR` lines fall entirely in the
  newly-executing tail (25+16=41, 2+4=6 — fully accounted for);
- the `server closed the connection unexpectedly` cascade is gone (0 occurrences).

**Verdict: NO REGRESSION.** The case has eight further independent root-cause
buckets (see the M0134-0025 deferral rows), of which the two largest — the
grouping-sets aggregation *strategy* selection (`GroupAggregate`/`MixedAggregate`
chains vs one `HashAggregate`) and tied-row emission ordering — are REFACTOR-tier.
