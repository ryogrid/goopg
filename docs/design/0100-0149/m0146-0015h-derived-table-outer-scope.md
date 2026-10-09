# M0146-0015h — non-LATERAL derived table / VALUES inside a correlated subquery resolves outer query levels

| field | value |
|---|---|
| Status | landed 2026-09-27 |
| Kind | bug (ERROR on valid SQL) |
| Parent | none (M0146 family; sibling of M0146-0015g) |
| Movement | none — correctness bug outside the parity instruments' reach |

## Defect

```sql
select (select count(*) from (select x.u1 from tk x where x.h = a.h) s)
from tk a;
```

goopg raised `ERROR: column "h" does not exist`; PostgreSQL 18.3 resolves
`a.h` against the outermost query level and returns one row per outer row.

The same gap rejected a `VALUES` range item in the identical position:

```sql
select (select count(*) from (values (a.h)) v) from tk a;
```

## Root cause

`planSubqueryRangeVar` (internal/optimizer/planner.go) has two arms:

- `lateralCtx != nil` — copies the lateral ctx, chains
  `latCtxWithCat.parent = planParent`, plans via `planSelectWithParent`.
- `lateralCtx == nil` — called `planSelectWithParent(subq, cat, nil, …)`:
  **no parent scope at all**.

`lateralCtx` is non-nil whenever the item is not the first FROM entry
(`len(bindings) > 0`, planner.go:3894), and `planFromItem` explicitly
nils it for `j.Right.Subquery != nil && !j.Right.Lateral` (4175). So the
nil arm covers exactly:

1. the first FROM item of any statement, and
2. every non-LATERAL subquery on a JOIN's right side.

In both positions PostgreSQL links the parent ParseState —
`parse_relation.c`'s subquery arm sets `pstate->p_parentParseState`
independent of `rte->lateral`; LATERAL only controls visibility of
same-level FROM siblings, never of outer query levels. goopg conflated
"no sibling visibility" with "no outer visibility".

## Fix

Three coordinated points, all in `internal/optimizer/planner.go`:

1. **First-item / bare position** — the nil arm now passes `planParent`
   (the enclosing statement's ctx) as the parent. `planParent`'s bindings
   never include the statement's own FROM items, so sibling visibility
   stays closed while outer refs resolve at level 1 — matching the
   executor's OuterRows stack, which holds exactly the pushed
   subplan-boundary row at that depth (no openLateral hop exists here).

2. **Non-LATERAL JOIN right side** — `planFromItem` replaces the old
   `joinLateralCtx = nil` with a binding-free link
   `&resolveContext{parent: planParent}`. The marker is deliberately a
   non-`lateralSibling` hop: once the right subtree resolves an
   OuterColumnRef, `Join.Lateral = nodeReferencesOuter(rightNode)`
   (planner.go:4289) makes `openLateral` push the left row per
   re-evaluation, so the outer ref must count one more level (level 2).
   Empty bindings keep same-level siblings invisible — PG-correct
   rejection — while `planParent` keeps the outer levels reachable.
   It also flows through `planSubqueryRangeVar`'s existing
   `lateralCtx != nil` arm unchanged (cat/rtScope re-stamped there).

3. **VALUES range items** — `planValuesSubquery`'s ctx now parents to
   `planParent` when `lateralCtx == nil`; the marker-ctx path covers the
   join-right-side level-2 case identically.

`resolveColumnRef` needed no change: it already increments `level` per
non-`lateralSibling` hop and produces `OuterColumnRef` for parent hits.

## Verification

Live A/B on a private pair (goopg :5533 vs PG 18.3 :5534), evidence in
`analysis/m0146/m0146-0015h/`:

| shape | goopg | PG 18.3 |
|---|---|---|
| first-item derived `x.h = a.h` | 10/row | 10/row |
| first-item `VALUES (a.h)` | 1/row | 1/row |
| non-lateral JOIN right side ref'ing `a` | 1000/row | 1000/row |
| same, ref'ing sibling `o` | error | error (42P01) |
| `JOIN LATERAL` ref'ing `o` | 1000/row | 1000/row |
| EXISTS over correlated derived | 100 | 100 |
| derived target-list / ORDER BY+LIMIT / unqualified ref | match | match |

Regression pin: `internal/executor/derived_table_outer_ref_test.go`
(fails pre-fix on every correlated case).

## Sibling defect filed, not fixed

A non-LATERAL **comma-separated** derived table takes the
`lateralCtx != nil` arm and still sees same-level siblings
(`from tk o, (select ... where x.h = o.h) s` returns rows where PG errors
"invalid reference to FROM-clause entry"). Fixing it needs a
bindings-stripped ctx for non-lateral comma items — same mechanism, wider
blast radius. Filed in `.ralph/fix_plan.md`.

## Gates

- units (`RALPH_PRECOMMIT_SCOPE=units`): PASS.
- `tpch-spotcheck`: Q12=2, Q13=33 — PASS.
- TPC-H acceptance arm: 24/24 MATCH vs `bench/tpch/baseline-digests.txt`.
- TPC-DS SF0.25 sweep: PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0;
  plan-shape same=99 changed=0.
- `tpcds-fireset` (planner.go in scope): PASS, fires=none at SF0.25+SF1 —
  zero moved plans.
