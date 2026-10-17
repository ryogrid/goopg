# M0146-0224 — TPC-DS Q78: outer-join commutation over derived leaves

Status: slice 1 (legality) and slice 2 (derived-leaf subpath cost) landed 2026-10-17. Q78 still diverges: the WHERE
clause is not costed at the top joinrel (§6). Evidence: `analysis/m0146/m0146-0224/`.

## 1. Record

TPC-DS SF0.25 Q78 (S9 pass at 1a8760b93, qual-placement, SHAPE-DIFF at depth 2). The same swap is the SF1 join-order
record. Both persist under PG statistics.

The query is `ss LEFT JOIN ws ON (…) LEFT JOIN cs ON (…)` over three single-reference CTEs. Every ON clause names only
`ss` and its own leg, with bare column names (`ws_item_sk = ss_item_sk`, …).

- PG commutes the outer joins (identity 3): `(ss ⟕ cs) ⟕ ws`.
- goopg keeps the written order `(ss ⟕ ws) ⟕ cs`.

## 2. Slice 1: the commuted pair was illegal

goopg's top search never built the `{ss, cs}` joinrel (`probe/goopg-q78-top-search-before.txt`). A probe of the search's
SpecialJoinInfo list (`probe/goopg-q78-sjinfo.txt`) shows the cause: the cs link had `min_lefthand = {ss, ws}`, its
syntactic side.

PG's `make_outerjoininfo` (`initsplan.c`) sets `min_lefthand` to the clause's relids within the left side, here `{ss}`.
The lower ws join is in that left side, but the clause does not reference its righthand side, so the lower join adds
nothing. `{ss, cs}` is then a legal pair, and `join_is_legal` admits `(ss ⟕ cs) ⟕ ws`.

goopg ports that logic (`makeSpecialJoinInfoForSets`), but its clause resolver `resolveSjiColumn` gave up on any
unqualified column while a table-less leaf (subquery, CTE or table function) was in scope: "it might hold the column".
Every outer join over derived tables with bare column names therefore fell back to its syntactic sides and could not
commute.

PG resolves those names at parse analysis, against each RTE's `eref->colnames`:
- a subquery takes its target list's names (`addRangeTableEntryForSubquery`);
- a CTE takes the CTE's output names (`addRangeTableEntryForCTE`);
- in both cases the column alias list renames by position.

**Change** (`specialjoin.go`):
- `sjiLeaf` gains `cols` / `colsKnown`. `derivedLeafColumns` fills them for a table-less leaf, and only when every name
  is certain:
  - a CTE's planned schema;
  - a subquery whose targets are each an alias, a bare column or a plain function call (`certainTargetName`);
  - then the column alias list, by position.
- A star, a set operation, a VALUES list, a table function or any other target expression leaves the leaf opaque.
  The resolver then still falls back to syn, the safe direction.
- `resolveSjiColumn`'s unqualified arm scans a leaf with certain names like a table (`sjiLeafHasColumn`). An
  ambiguous name still bails.

After the change the cs link's `min_lefthand` is `{ss}`, and the search builds `(ss ⟕ cs) ⟕ ws`
(`probe/goopg-q78-top-search-after.txt`).

**Measured:**

| corpus | before | after |
|---|---|---|
| TPC-H | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | 90 / 99 | 90 / 99 |
| TPC-DS SF1 | 77 / 99 | 77 / 99 |

- Only SF1 Q78's plan changed. It now has PG's join order, `(ss ⟕ cs)` → Sort → `⟕ ws`.
- Its first divergence moved from join-order to qual-placement at the same depth, which is the SF0.25 record.
- SF1 CATEGORIES-EXCL-MATCH qual-placement went 5 → 6. Q78's category set gained it; join-order stays, from the ss leg's
  row estimate (558 against PG's 1124).
- The fired SF1 Q78 result is byte-identical, and the acceptance arm is value-identical.

**Pin:** `TestScopedUnqualifiedResolvesDerivedLeaves` covers:
- identity 3 narrowing over subquery leaves, by target alias and by column alias list;
- a star subquery keeping the syn fallback;
- an unnamed expression target keeping the syn fallback.

Mutation-checked: an always-opaque leaf fails it.

## 3. PG's verdict at the top joinrel (exact replica)

`probe/pg-exact-replica-q78-add-path.txt`. Relids are rtindexes: 1 = ss, 2 and 3 = ws and its outer join, 4 and 5 = cs
and its outer join.

| pair | PG | add_path |
|---|---|---|
| `(ss ⟕ ws) ⟕ cs` | 42027.8563..42467.8331, rows 762 | accepted first |
| `(ss ⟕ cs) ⟕ ws` | 42027.8563..42467.7731, rows 762 | accepted, evicting the first (0.06 cheaper on the tight fuzz) |

goopg now builds both, at 42027.889..42521.2259 each; they are equal to 1e-11, so the incumbent stays.

## 4. What still separates Q78 from PG (next slice)

1. **The WHERE clause is not costed at the joinrel.**
   - In PG, `(coalesce(ws_qty,0)>0 or coalesce(cs_qty,0)>0)` is a restriction of the top joinrel. Its selectivity
     gives the path rows 762, and `final_cost_mergejoin` charges its evaluation per `mergejointuples` (`qp_qual_cost`).
   - `mergejointuples` differs between the two orders, so the charge differs too. That is where PG's 0.06 asymmetry
     comes from.
   - goopg's top paths carry rows 1371 and apply the qual above the join. Shown as 21.3 above the path total.
2. **Input run costs.**
   - goopg's 2-rel merges cost more than PG's on run cost with equal startups: `ss ⟕ ws` +37 (28320.53 against
     28283.63), `ss ⟕ cs` +44 (33648.28 against 33604.25).
   - The inner Materialize over the ws GroupAggregate is 8862.37 against PG's 8852.87.
   - Next: compare `final_cost_mergejoin`'s terms one by one: `mergejointuples`, `rescanratio`, `materialize_inner`.

PG's residual join clause `ws_sold_year = ss_sold_year` is replaced by a constant TRUE in
`reconsider_outer_join_clauses`, once `ws_sold_year = 1998` is derived. goopg shows no Join Filter either, so it is
not a difference in the plan text. Whether goopg still costs the clause is part of item 2.

## 5. Slice 2 (landed): a derived leaf pays cost_subqueryscan once

`final_cost_mergejoin`'s terms on the replica (`probe2/pg-exact-replica-q78-final-cost-mergejoin.txt`) and in goopg
agree on every factor: `mergejointuples` (16 and 28), `rescanratio` 1, merge qual 0.005, and the forced Material over
an unsorted GroupAggregate. What differed were the inputs.

PG's subquery-scan paths are the aggregate plus `cpu_tuple_cost` per row: ss 19406.47 + 13.71 = 19420.18. goopg's
search leaves were the aggregate plus three times that: ss 19447.58, ws +14.25, cs +24.92
(`probe2/goopg-q78-derived-leaf-chain-before.txt`).
- A derived leaf arrives as SubqueryScan → Project → Aggregate.
- The legacy display cost charges `cpu_tuple_cost` per row at the SubqueryScan and again at the Project.
- `costSubplanLeaf` then adds `cost_subqueryscan`'s own charge.

In PG:
- the SubqueryScan is the scan `cost_subqueryscan` prices, so its subpath is the node beneath it;
- a projection over a projection-capable path costs only its target list's evaluation (`create_projection_path`'s
  dummypp arm).

**Change:**
- `subplanLeafSubpathCost` peels a SubqueryScan, then a Project whose child is projection-capable.
  `projectionCapableNode` mirrors `is_projection_capable_path` on a positive list: scans, joins, Aggregate,
  WindowAgg, Gather, GatherMerge and Result.
- A wrapper is peeled only when it carries no path cost of its own, and its initPlan charge is kept.

**Measured:**
- Q78's 2-rel merges now match PG: `ss ⟕ ws` 28283.61 against 28283.63; the ws Materialize displays 8854.06, as PG.
- Census verdicts and categories are unchanged (TPC-H 22, SF0.25 90, SF1 77).
- 13 TPC-DS plans per scale and TPC-H Q18 changed cost only, with no shape change. Q78 SF0.25 is 42519.72 against
  PG's 42510.48, from 42573.54. TPC-H Q18 is 404975.77 against 404713.55, from 406128.37.
- All 26 fired results are byte-identical, and the acceptance arm is value-identical.

**Pin:** `TestSubplanLeafPricesCostSubqueryscanOnce`. A projection over Material keeps its charge. Mutation-checked.

## 6. What still separates Q78 from PG (slice 3)

With the inputs equal, the two orders still tie in goopg. PG's 0.06 is the WHERE clause
`(coalesce(ws_qty,0)>0 or coalesce(cs_qty,0)>0)` evaluated as a restriction of the top joinrel:
- `qp_qual_cost` is 0.005;
- it is charged per `mergejointuples`, which is 28 for `(ss⟕ws)⟕cs` and 16 for `(ss⟕cs)⟕ws`;
- its selectivity sets the path rows to 762.

goopg holds a WHERE conjunct that reaches a nullable side above the whole searched tree (`tryJoinSearch`'s
`heldAbovePrefix`) and evaluates it as a Filter after the search. The placement is the same here, since the clause
needs both outer joins. What differs is that the search never prices it.

The resume point is the ledgered `C-05 pselec-held-above` (`joinrelsize.go` `pushedDownSelectivity`):
1. Distribute such a conjunct into the clause list with an `is_pushed_down` bit, required at the join that completes
   its outer joins.
2. Size it through `pselec`.
3. Charge it as `qp_qual` in the join producers, and apply it as the outer join's Filter (otherquals) rather than as a
   join qual.

## 7. Not ported (ledger)

- Strictness for derived leaves:
  - `sjiClauseRelids`'s strict relids come from `collectNonNullableTableNames`, which maps only catalog tables.
  - A strict clause over a subquery or CTE leaf is therefore under-strict (`LhsStrict` false).
  - That is the safe direction, but it withholds the RHS association and the identity-3 arm PG would allow when a
    clause references the lower join's righthand side.
- FigureColname for expression targets (casts, CASE, operators): such a leaf stays opaque to unqualified resolution.
