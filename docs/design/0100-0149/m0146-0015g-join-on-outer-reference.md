# M0146-0015g: outer references in a JOIN ON clause inside a correlated subquery

Status: **LANDED**. Task: `.ralph/fix_plan.md` — the anonymous item filed by
M0146-0015a (owner direction 2026-09-27: loud ERROR on valid SQL → normal
order, not item 2a). Evidence: `analysis/m0146/m0146-0015g/`.

## Defect

```sql
select sum((select count(e.unique1)
            from tenk1 d left join tenk1 e
              on e.unique1 = d.unique2 and e.hundred = a.hundred
            where d.thousand = a.thousand))
from tenk1 a where a.unique1 < 20;
```

goopg → `ERROR: column "hundred" does not exist` (42703); PG 18.3 → 2.
The same reference in the subquery's WHERE clause worked — the failure was
specific to JOIN ... ON.

## Root cause

`resolveColumnRef` (`internal/optimizer/planner.go`) walks
`ctx.parent` for the lexical scope chain. That chain is only usable where a
context actually carries a parent:

- The statement-level context receives `ctx.parent = planParent` at
  planner.go:1644 — *after* `planFromClause` returns. WHERE, the target
  list and everything resolved later see the outer scope; ON clauses do
  not, because they are resolved *inside* `planFromClause`.
- `planFromItem` builds `leftCtx`, `rightCtx` and `mergedCtx` per join link
  via `newResolveContext` with no `parent`. `planJoinPredicate` then ran
  `resolveExpr(join.On, mergedCtx)` — a scope walk that ends after the
  local bindings, so `a.hundred` missed at every level and raised the bare
  42703 form.

## Change

One site: `planJoinPredicate` resolves the ON expression against a shallow
copy of `mergedCtx` with `parent = planParent` stamped — the same
package-level channel `planSelectWithParent` installs for the whole nested
plan and that planner.go:5756/6685/… already read mid-plan. A copy, not the
live `mergedCtx`, because `mergedCtx` is reused as the next link's
`leftCtx` and feeds `buildUsingPredicate`/`naturalJoinColumns`, whose name
lookups must stay local-only (a USING name that misses locally must still
error, never bind an outer column).

Level arithmetic is unchanged: the stamped parent is a real scope boundary,
so `a.hundred` resolves to `OuterColumnRef{Level: 1}` — identical to the
WHERE-clause result.

## Why the rest of the pipeline tolerates an ON-clause OuterColumnRef

- `walkColumnRefsImpl` reports `OuterColumnRef` via `onOuter`, so
  `classifyConjunctSide` (the LEFT-JOIN inner-only push at
  planner.go:4234) returns `sideOutOfScope` and keeps the conjunct on the
  join — it is never pushed into a `Filter` under the join's right side.
- `shiftColumnRefsBy` rewrites `*ColumnRef` only; an `OuterColumnRef` index
  survives any sibling shift untouched.
- The executor already evaluates `OuterColumnRef` against
  `ctx.OuterRows`, which the correlated-subplan machinery populates per
  outer row (that machinery is what M0146-0015a exercised for index keys).

## Verification

- Live probe pair on private clusters (goopg :5533 vs PG 18.3 :5534):
  `analysis/m0146/m0146-0015g/goopg.txt` vs `pg18.txt` — byte-identical
  across LEFT/INNER/RIGHT joins, EXISTS, `IS NOT DISTINCT FROM`, an
  unqualified outer ref, and the filed regress shape (both answer 200 on
  the synthetic tenk1).
- Regression test: `internal/executor/join_on_outer_ref_test.go`.

## Residuals (filed in fix_plan, not fixed here)

- A *non-LATERAL derived table* inside a correlated subquery still cannot
  see outer-scope columns: `planSubqueryRangeVar` calls
  `planSelectWithParent(subq, …, nil, …)` when `lateralCtx == nil`, so the
  inner statement gets no parent chain at all (WHERE and ON alike).
  `siblings.txt` defect A.
- `coalesce(d.c, e.c) IS NOT NULL` demotes a FULL JOIN to inner in
  `reduceOuterJoins` (10 vs PG's 190 rows). `siblings.txt` defect B.
