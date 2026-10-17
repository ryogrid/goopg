# M0146-0176 — re-basing a kept SubPlan across a lateral join

Status: closed 2026-10-13.

## Problem

TPC-DS Q32 and Q92 apply `ws_ext_discount_amt > (SubPlan 1)` as a `Filter`
on the finished join tree.

- No join path pays for the SubPlan there. Q92 SF0.25 elects a nested loop at
  2277.04, with no charge for 160 SubPlan calls.
- PostgreSQL keeps the clause at relids {web_sales, item}
  (`distribute_qual_to_rels`, initsplan.c). It evaluates it as the hash join's
  Join Filter and charges the SubPlan's per-call cost (`cost_qual_eval` in
  `final_cost_hashjoin`, costsize.c).

## Cause

M0146-0012a pre-lowers such a conjunct before the join search
(`preLowerSpanningScalarSublinks`, sublinkjoinclause.go):

- its correlated outer references become SubPlan arguments;
- `relidsOfExpr` then counts both relations, so the clause is placed at the join.

The pre-lowering uses the kept-SubPlan re-base (`keptRebase.rebaseOne`,
pulledsublink.go).

M0146-0170 fixed a wrong result in that re-base by refusing outright any
SubPlan whose plan contains a lateral join:

- In Q32's SubPlan the inner probe's nestloop parameter had been re-bound to
  the host row.
- Q92's SubPlan body is a nested loop whose `date_dim_pkey` probe reads
  `web_sales_1.ws_sold_date_sk`, so the refusal applied to it as well.
- `GOOPG_NLI_CENSUS=1` reports `REBASEFAIL cause=nested-sublink-lateral`.
- With the conjunct not pre-lowered, its relids were {web_sales} only, and the
  join search left it above the tree.

The fire-set finding (M0146-0014d F1) dated it to M0146-0130. The census and
the code history place it at M0146-0170.

## Frame model

- A `Join` with `Lateral` set drives its right child once per left row.
  `joinOp.openLateral` → `lateralJoinStream.bindOuter` pushes that row onto
  `ctx.OuterRows` for each right-side call.
- Inside the right child, `OuterColumnRef{Level: 1}` is therefore the join's
  own nestloop parameter, and every reference to the enclosing query sits one
  level further out.
- The join's own `Predicate` and keys evaluate outside that frame.
- No other join kind pushes a frame.

## Change

- **`lowerTraverseNodeHooked`** (subplan\_lower\_walk.go): the shared lowering
  traversal gains an optional node hook. `lowerTraverseNode` is the hook-free
  wrapper, so its existing callers are unchanged.
- **`keptRebase.rebaseNode`** hooks every `Lateral` join:
  - it rewrites the join's own expressions at the current depth;
  - it re-bases the left child at the current depth and the right child at
    depth + 1.
  - The nestloop parameter stays a frame reference, and only references that
    really escape become SubPlan parameters.
- **`rebaseOne`** drops M0146-0170's blanket refusal.

## Not changed

These gates keep their lateral refusal, and are ledgered:

- `keptSubplanAdmissible` / `keptPlanRefsAdmissible`, the pulled-body
  admission gate;
- the D4.1 lowering pass (`lowerSubPlanParams`), which leaves a lateral plan on
  the stack path.

Both could reuse the same frame rule.

## Evidence

Files are in `analysis/m0146/m0146-0176/`.

- **Q92 and Q32 at SF0.25:** both now have PostgreSQL's shape, a hash join
  with the SubPlan as its Join Filter. Their values are identical to PG.
- **TPC-DS fire set:**
  - SF0.25 match 72 → 74.
  - SF1 match is unchanged at 46. Join-method, scan-type and parallelism each
    drop by 2; the stale SF1 load keeps both queries divergent elsewhere.
- **TPC-H corpus:**
  - Only Q2 fires, and its values match.
  - Its plan moves to PostgreSQL's join structure. What remains is PG hashing
    on the SubPlan as a second key.
  - goopg in that shape runs 0.71 s against PG's 0.20 s. This is the
    M0146-0088 per-row executor residual.
- **Regress A/B:** `subselect`, `join`, `with` and `aggregates` are identical.
- **Test:** `TestCorrelatedSubPlanKeepsInnerNestloopParam` pins both the value
  and the qual's level. The level assertion fails with the M0146-0170 refusal
  restored, and the value assertion fails when the right child is walked at
  the old depth.
