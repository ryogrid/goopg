# M0146-0232 — set_subquery_pathlist coverage: the census, and an inlined CTE leaf as a nested-loop inner

Status: slice 1 landed 2026-10-20. CLOSED (the census names no further record).
Evidence: `analysis/m0146/m0146-0232/`.

## 1. The task as filed

M0146-0231 left two set_subquery_pathlist gaps:

- PG files the subquery final rel's partial paths;
- PG offers the final rel's paths for every subquery, including ungrouped,
  join-only bodies and bodies with ORDER BY, DISTINCT or a window above the
  grouping.

goopg offers alternatives only for a grouped body (subqueryLeafAggregate).
The filed first step was a census: which fired TPC-DS queries diverge under a
SubqueryScan or an inlined CTE leaf?

## 2. Census (fire-set capture of M0146-0234 slice 4)

| query | first divergence | under a subquery leaf? |
|---|---|---|
| SF1 Q13 | base-table join order and method | no |
| SF1 Q72 | qual placement in a nested loop with an Index Cond | no |
| SF0.25 Q77 | Append c0 (store branch): sort strategy under a partial aggregate | no |
| SF1 / SF0.25 Q77 | Append c1: the nested loop's inner over the inlined CTE `cr` | yes, grouped body |

No divergence needs a subquery partial path or an ungrouped-subquery
alternative. The one divergence under a subquery leaf is Q77's c1 branch,
which this slice fixes. Its body is grouped, so the record is a rescan and
Material question, not a pathlist one.

## 3. The Q77 c1 record

`cs` and `cr` are single-reference CTEs, cross-joined. PG plans:

```
Nested Loop
  ->  Finalize GroupAggregate          (cs)
  ->  Materialize
        ->  Subquery Scan on cr
              ->  Finalize GroupAggregate ...
```

goopg planned the bare aggregate as the inner, with no Material and no label.

### 3.1 Rescan class (materialize.go)

inline_cte (subselect.c) turns a single-reference CTE into an ordinary
RTE_SUBQUERY. Its scan is a T_SubqueryScan, which has two consequences:

- cost_rescan takes the default arm and re-executes the body per outer row;
- ExecMaterializesOutput does not list it, so match_unsorted_outer files a
  MaterialPath over it (joinpath.c:1890-1901).

goopg's join search reaches the inlined reference as a `*CTEScan`, and two
predicates classed every CTEScan as T_CteScan:

- `isTuplestoreScanNode` priced rescans at cpu_tuple_cost per tuple;
- `nodeMaterializesOutput` suppressed the Material.

Both now answer `!Inlined()` for a CTEScan. A kept CTE is still T_CteScan.
The executor already streams an inlined body (CTEScan.Inlined), so the
classification also matches what goopg executes.

### 3.2 The label under a MaterialPath (subqueryscan_strip.go)

create_material_plan plans its child with `flags | CP_SMALL_TLIST`
(createplan.c:1650), so use_physical_tlist declines. The scan's tlist is then
the consumed columns, and a subset consumption keeps `Subquery Scan on cr`.

`subqueryStripTlistReset`'s comment already named create_material_plan, but
its switch had no `*Materialize` arm. Adding one exposed the second kind of
Material: create_mergejoin_plan's make_material over an elected
materialize_inner (createplan.c:4659). That inner was planned under the join's
own flags, so the physical regime holds and setrefs strips the label (TPC-DS
Q58's `ws_items`, Q78's `cs`).

The two are told apart by `Path.mergeJoinMaterial`, set by
mergeMaterialInner and carried to `Materialize.mergeJoinInner`. Only a
MaterialPath's Material resets the regime.

## 4. Measurements

- Repro (SF0.25 clone, `cs × cr` alone): NL 16940.92 → 16940.73 (PG
  16940.76), with Materialize over Subquery Scan on cr.
- SF0.25 full capture: only Q77's plan text changed. Q77 drops `scan-type`
  (CATEGORIES-EXCL-MATCH scan-type 1 → 0), and its first divergence is now the
  c0 store branch alone. Match count 98/99 → 98/99.
- SF1: the c1 branch now matches PG. The first divergence moves to the c2 web
  branch: PG has a Merge Left Join, goopg a Hash Left Join with `Subquery Scan
  on wr` under the Hash. That divergence predates this slice. Match count
  96/99 → 96/99, categories unchanged.
- TPC-H 22/22. sf025 PASS=99, same=98, changed=1 (Q77). Fire set PASS.

## 5. Pins

`internal/postmaster/inlined_cte_material_test.go`
(TestInlinedCTEInnerIsMaterialized) compares plan shape, costs stripped,
against PG 18.3's EXPLAIN on the same statements.

Both halves are mutation-checked: reverting the rescan class drops the
Material, and reverting the tlist arm drops the label.

The merge-join arm is covered by the fire set (Q58, Q78), not by a unit pin.
A small schema did not provoke a merge-join materialize_inner over a subquery
leaf on PG.

## 6. Not done (ledgered)

- Subquery partial paths and alternatives for ungrouped subqueries: no fired
  query needs them (census §2).
- The add_paths_to_joinrel producer-order audit for the precheck: no record
  shows an order effect.
- A wrapper SubqueryScan's display cost still differs from PG's (Q5's ssr
  21930.49 vs 21930.42, Q47's v1). This is a rendering-cost gap present before
  this slice.
