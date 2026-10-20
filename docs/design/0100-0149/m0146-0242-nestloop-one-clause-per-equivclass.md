# M0146-0242 — a nested loop's restrictlist holds one equality per equivalence class

Status: landed 2026-10-20 — CLOSED (TPC-DS SF1 98 → 99/99: Q72 matches PG; all three corpora now match every plan).
Evidence: `analysis/m0146/m0146-0242/`.

## 1. Record

SF1 Q72 was the only open record left in all three corpora. Its divergence is
qual-placement at the item probe:

| | probe | Join Filter |
|---|---|---|
| PG | `Index Cond: (i_item_sk = catalog_sales.cs_item_sk)` | none |
| goopg | `Index Cond: (i_item_sk = inventory.inv_item_sk)` | `(catalog_sales.cs_item_sk = item.i_item_sk)` |

The equivalence class is {cs_item_sk, inv_item_sk, i_item_sk}.
`cs_item_sk = inv_item_sk` and `i_item_sk = cs_item_sk` are written;
`i_item_sk = inv_item_sk` is inferred.

## 2. PG mechanism

- build_joinrel_restrictlist → generate_join_implied_equalities_normal emits
  ONE equality per class between the join's two sides. It equates the first
  best-scoring outer member to an inner member, so `i = cs` for Q72.
- create_nestloop_path drops a restrict clause only if it is movable into the
  parameterised inner (join_clause_is_movable_into).
  - The cs-keyed probe enforces `i = cs`: no Join Filter.
  - The inventory-keyed probe cannot take `i = cs` (catalog_sales is not in
    its parameterisation), so it pays a Join Filter.
- The inventory probe is cheaper per scan, because get_loop_count amortises
  it over inventory's larger row count. The filter's cost is what tips PG to
  the cs probe.

## 3. goopg before

The clause list handed to the path generators is the joinrel's full
restriction list, holding every member pair of the class. calcJoinrelSize
reduces it with oneClausePerEquivClass for sizing, but nestloopResidualClauses
did not. So each probe dropped the clause it enforced and kept the other
sibling. Both probes paid a filter, and the inventory probe won on
amortisation, filter included.

## 4. Change

nestloopResidualClauses (joinpathsnli.go) reduces its input with
oneClausePerEquivClass, the rule calcJoinrelSize already sizes the join with,
then applies the existing identity-and-movability drop. The cs-keyed probe is
now filterless; the inventory-keyed probe keeps `i = cs` as its Join Filter.

A first attempt dropped every sibling of a probe-enforced class. That made
both probes filterless and handed the inventory probe the win. It contradicts
PG, which charges the inventory probe the filter, and was replaced.

## 5. Measurements

- **SF1 Q72:** MATCH; fire-set SF1 98 → 99/99, only Q72 fired.
- **SF0.25:** 99/99, no plan changed.
- **TPC-H:** 22/22, plans byte-identical.

## 6. Pins

- `internal/optimizer/joinpathsnli_test.go`,
  TestNLIResidualKeepsOneClausePerEquivClass: a probe keyed on the class's
  equality leaves no residual; one keyed on the inferred member leaves exactly
  that equality. It fails without the reduction.
- `internal/postmaster/nestloop_equivclass_residual_test.go`,
  TestNestLoopKeepsOneEquivClassClausePerJoin: PG 18.3's EXPLAIN for both
  directions, end to end. A shape guard only: the pre-fix code also produced
  these two plans, because only a near-tie between probes, Q72's, exposes the
  difference.

## 7. Not done (ledgered)

- The class's representative. PG takes the first best-scoring outer member in
  the class's member order, which follows clause order (a scratch-PG probe
  merged on `ed.k = ea.k` although `ed.k = ef.k` was written).
  oneClausePerEquivClass prefers a written clause over an inferred one, then
  list order. Q72's written clause is also the first member, so they agree
  there.
- The other path generators (hash, merge, plain nested loop) still receive
  the full member-pair list.
