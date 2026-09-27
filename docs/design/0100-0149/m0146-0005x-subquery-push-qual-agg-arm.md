# M0146-0005x — subquery_push_qual aggregate arm + tlist-regime trivial_subqueryscan

Status: landed 2026-09-27.

## Problem

TPC-DS Q34/Q73 shape: `FROM (SELECT … GROUP BY …) dn JOIN … WHERE dn.cnt
BETWEEN x AND y`. PostgreSQL:

1. `subquery_push_qual` (allpaths.c:4023, called from
   `set_subquery_pathlist`) proves the restriction names only the
   subquery's own output columns, rewrites it through the subquery's
   target list (`ReplaceVarsFromTargetList`), and appends it to
   `subquery->havingQual` because the subquery aggregates — the qual
   becomes `Filter: ((count(*) >= 15) AND (count(*) <= 20))` on the
   aggregate plan node.
2. The pushed qual leaves `baserestrictinfo`, so the SubqueryScan's
   qpqual is empty; `trivial_subqueryscan` (setrefs.c) then deletes the
   wrapper when its scan tlist regurgitates the subplan's targetlist
   position-for-position.

goopg kept `Subquery Scan on dn` with the qual as a leaf `Filter`
(`Filter: ((cnt >= 15) AND (cnt <= 20))`).

## Decision

### The pushdown (subquerypushdown.go)

The seam (joinsearchseam.go) already decomposes `WHERE` into per-leaf
local conjuncts and attaches them as a `Filter` above the leaf. For a
`*SubqueryScan` leaf the new hook first offers each conjunct to
`pushQualsIntoSubqueryLeaf`:

- Safety (`subqueryQualPushdownSafe`, built on `walkExprRefs` under
  `scopeVeto` — the expr-inventory gate forbids new hand switches):
  no inner-scope/subplan children (upstream's contain_subplans
  refusal), no OuterColumnRef/ParamRef/ExecParamRef/CTIDExpr/
  TableOidExpr, no planned InExpr sublink, no volatile builtin, at
  least one leaf column. Volatile conjuncts decline outright — PG
  admits them into grouped subqueries, but keeping one leaf-local is
  never wrong (ledgered).
- Nullable-side leaves decline at the caller (outerChainLink.nullable):
  an ON-qual-derived conjunct must evaluate after null extension.
  Verified against live PG: `LEFT JOIN … WHERE u.c > 0` degenerates to
  inner in both engines (the push is then legal), while `FULL JOIN …
  WHERE u.c > 0` keeps the qual at join level in both.
- Rebase (`subqueryLeafPosToAggExpr` via `subqueryLeafRebase`):
  descend `ss.Child` through `*Filter`/`*Sort`/`*IncrementalSort`
  (position-transparent) and `*Project` (position mapped through
  `Targets` — bare `*ColumnRef` targets only; a computed target is a
  decline, the strict-subset stand-in for ReplaceVarsFromTargetList).
  The leaf-level desync guard (localized ref must name-check against
  the leaf schema) stays; at the aggregate the rebased ref ADOPTS the
  aggregate's own output name (`count`, not the leaf alias `cnt`) —
  that is what `expandAggOutputRef`'s render guard
  (`sch[idx].Name == col.Name`) needs to print `count(*)`.
- Splice (`spliceFilterAboveSubqueryAgg`): one `*Filter` is stacked
  outermost of the aggregate-adjacent Filter run — inner-first
  rendering prints existing HAVING text before the pushed text, the
  `make_and_qual(havingQual, qual)` order. Only the passthrough
  wrappers between the wrapper and the aggregate are shallow-copied;
  the aggregate subtree itself is shared, so `PartialSource`
  Finalize↔Partial linkage survives and a seam decline leaves the
  caller's fallback the original untouched tree.
- Unpushed conjuncts keep the ordinary leaf `Filter` — nothing is
  dropped or double-applied. `EstimateRows` already threads
  `SubqueryScan → Filter → Aggregate`, so the leaf estimate falls
  honestly when the push lands.

### The strip correction (subqueryscan_strip.go)

Slice 24's consumption-identity proxy is only half of
`trivial_subqueryscan`. PG's test is structural (no qual + positional
tlist identity), and which tlist the scan emits is decided by the flags
each ancestor passes `create_plan_recurse` — verified line-by-line in
createplan.c and probed on live PG 18.3:

| ancestor of the leaf | child flags | regime |
|---|---|---|
| top-level create_plan | CP_EXACT_TLIST | pathtarget |
| Sort, IncrementalSort, Memoize, WindowAgg | flags\|CP_SMALL_TLIST | pathtarget (reset) |
| Gather, GatherMerge, RecursiveUnion, append/setop arms | CP_EXACT_TLIST | pathtarget (reset) |
| Join (all algos incl. NestedLoopIndexJoin), Aggregate, ProjectSet | 0 / CP_LABEL_TLIST | physical |
| Limit, LockRows, Unique/Distinct, Filter, Result, Project | flags passthrough | propagate |

Physical regime ⇒ `use_physical_tlist` ⇒ the scan emits the whole
subquery output in attno order ⇒ tlist identity holds by construction ⇒
strip whenever qual-free, regardless of consumption. Pathtarget regime
⇒ consumption-identity decides (the slice-24 rule).

The walk (`stripTrivialSubqueryScans`) now tracks the regime per level:
a breaker sets `physical`, a reset node clears it, a passthrough
propagates it, a region boundary (derived subtree / CTEScan /
SubqueryScan interior / set-op arm) restarts at EXACT — matching each
subplan's own `CP_EXACT_TLIST` entry into `create_plan`. The
qual-free + width-equality guard is unchanged.

Live-PG probes that pinned the model (on :65438): subset `u.c` at top
level, under `Sort`, under `Limit`, under `DISTINCT` all KEEP the
label; the same leaf under a Nested Loop or a top Aggregate strips; a
subset set-op leaf under a join strips; `select u.c+1` (computed
select list) KEEPS — a SubqueryScan path is projection-capable, so PG
folds the expr into the scan's own tlist rather than creating a
projection node, which is why goopg's `*Project` is a passthrough, not
a breaker.

## Result

Q34/Q73 on SF0.25 render PG's exact shape — bare `GroupAggregate`
carrying `Filter: ((count(*) …))`, no `Subquery Scan on dn` — with row
counts 87/0 matching the oracle. Q8's `a1` arm keeps its label
(nontrivial INTERSECT-arm consumption). Gates: units, tpch-spotcheck,
SF0.25 sweep, acceptance arm, fireset both scales — see
`analysis/m0146/m0146-0005/slice25/gates.txt`.

## Out of scope (ledgered)

- The non-aggregate arm of `subquery_push_qual`: for a flat subquery PG
  re-enters the qual into the subquery's own jointree (planned as an
  inner restriction, which can elect index paths). goopg's splice is
  post-planning and cannot re-open the subquery's jointree — that arm
  needs a `planSubqueryRangeVar`-level re-entry, a different and larger
  mechanism.
- Volatile qual pushdown (PG admits; goopg declines).
- The kind-level regime approximation's one unmodelable cell: a merge
  join that demands sortkeys from a directly-attached leaf would pass
  CP_SMALL_TLIST. No corpus consumer exists.
