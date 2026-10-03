# M0146-0049 — a parameterised inner path through a non-scan node

Status: in progress — slices (a) recon and (b)+(c) landed 2026-10-03 (`f661e6933`). Parent: M0146
(banner item 3: owner-named next in the structural line, the blocker of
M0146-0005dp/dq and M0145-0008ac/0008y).
Background: `docs/design/0100-0149/m0146-0005dt-parameterised-append-recon.md`.

## PG mechanism (recap)

- `add_paths_to_append_rel` builds an Append for every child
  parameterisation (`./postgres/src/backend/optimizer/path/allpaths.c:1321`,
  `get_cheapest_parameterized_child_path` `:2048`).
- `create_nestloop_plan` binds `NestLoopParam`s into ANY inner subtree
  (`./postgres/src/backend/optimizer/plan/createplan.c:4341`,
  `replace_nestloop_params` `:5036`).
- `ExecReScanAppend` (`./postgres/src/backend/executor/nodeAppend.c:421`)
  propagates the changed params to each child.

## Slice (a) — recon: the executor substrate (2026-10-03)

The probe used `li(id pk, cat)`, `cs1(item, ord) pk` (100k rows) and
`ws1(item, ord) pk` (50k rows), with `li.cat = 3` keeping 40 rows.

| query | PG 18.3 | goopg |
|---|---|---|
| `li, LATERAL (SELECT amt FROM cs1 WHERE item = li.id UNION ALL SELECT amt FROM ws1 WHERE item = li.id) x` | Nested Loop → Append(Bitmap Heap cs1, Bitmap Heap ws1), `Index Cond: (item = li.id)` | the SAME shape, values identical (3000 rows, same sum) |
| `li, (SELECT item, amt FROM cs1 UNION ALL …) x WHERE x.item = li.id` | the same parameterised Append (Q54 in miniature) | Hash Join over a Parallel Append of seq scans |

So the executor already runs PG's parameterised-Append shape. goopg's
explicit LATERAL plans each member as a one-relation scope whose
correlated restriction becomes an index probe (M0146-0015a). The lateral
join stream then re-opens the right subtree per outer row with the outer
tuple bound.

The lowering contract also exists. Since R25 the NLI arm lowers to
`Join{Algo: NestedLoop, Lateral: true, Right: IndexScan}` with keys as
level-1 `OuterColumnRef`s (createplannl.go). A parameterised Append is the
same contract with an `Append` of such probes on the right.

What is missing is entirely planner-side:

- the appendrel leaf (M0145-0004's partial-path hoist) has no
  parameterised paths;
- there is no Append path kind;
- NL path generation never pairs an outer rel with a parameterised
  non-index inner.

The LATERAL probe also showed an estimate gap: goopg's NL over the
LATERAL Append estimates 1 row where PG estimates 3000.

## Slices (b)+(c) — landed together (2026-10-03)

(b) alone would have filed paths nothing consumes, so the two landed as
one change.

**Producer.** `addParameterizedAppendPaths` (paramappend.go) runs after
the base-rel parameterised producers (pathindexordered.go).

- It applies to a level-1 rel marked `appendrel` whose baseLeaf is a
  plain serial UNION ALL chain (`unionAllMembers`, orderedappend.go's
  predicate).
- Every member must be `[Project →] [Filter →] scan`. Each gets a
  synthetic member rel whose baseLeaf is that scan.
- The leaf's `indexableJoinClausesFor` candidates are re-read onto the
  member column at the same output position. The `restrictInfo` is kept,
  so `probeEnforcedClauses` (now intersecting over an Append's children)
  still recognises the enforced clause and the join drops it.
- `addOneParameterizedIndexPath` and `buildOneParameterizedBitmapPath`
  price the member's probes: index and bitmap, as `create_index_paths`
  builds both.
- The cheapest member path per parameterisation is kept. If any member
  has none, that parameterisation is abandoned, as
  `get_cheapest_parameterized_child_path` returning NULL does.
- The Append is costed as `cost_append`'s unordered arm
  (`./postgres/src/backend/optimizer/path/costsize.c:2255`): the first
  child's startup, the summed totals and rows, plus
  `cpu_tuple_cost × 0.5 × rows`.
- Member probes are never index-only: the statement-wide needed set
  cannot attribute a member's columns.

**Lowering.** `PathParamAppend` gets its own `createPlanNode` arm, which
rebuilds the leaf's UNION ALL around the member probes, each under the
member's own projection. `createNestLoopPlan` dispatches it to
`createNestLoopParamAppendPlan`:

- a `Join{Algo: NestedLoop, Lateral: true}`, R25's NLI contract;
- each probe's keys re-rooted as level-1 `OuterColumnRef`s by
  `outerParamKey`;
- for a bitmap member, the recheck equality is kept on the heap scan's
  `Cond`, because `BitmapQual`'s merged outer++inner coordinates do not
  exist under a lateral join.

**Effect.**
- TPC-DS Q54's `my_customers` subtree becomes Nested Loop(item,
  Append(Bitmap Heap catalog_sales, Bitmap Heap web_sales)): cost 19832 →
  8561, PG 6537.
- `TestParameterisedAppendOverUnionAll` pins the shape and PG's values
  for an inner join, a LEFT JOIN, a member with no index (declines) and a
  three-member chain.

**Gates:** units, spotcheck, SF0.25 sweep 96/96, fire set (only Q54 fires,
at both scales, no timeouts), TPC-H arm, ea-ratchet 10/10. Regress union,
join, subselect, inherit, partition_join, partition_prune, select,
equivclass and with are byte-identical.

**Movement: none.** Q54's categories are unchanged:
- PG drives the probe from a Parallel Seq Scan under a Gather, and goopg's
  param Append is serial-only (0049e);
- PG probes `web_sales` by Index Scan, where goopg's bitmap is cheaper by
  its own cost.

## Remaining slices

- **(b)** Per-member parameterised index paths for a flattened UNION ALL
  leaf whose members are single-table scans. The join clause
  `leafcol = outer` is translated through the member's output column to
  `membercol = outer`. Built with `addOneParameterizedIndexPath`'s pricing
  (pathparamindex.go), as `get_cheapest_parameterized_child_path`.
- **(c)** A parameterised Append path for the leaf: cost and rows are
  the sum of its children (`create_append_path` with `required_outer`).
  NL path generation also needs to accept it as an inner, and it lowers
  to `Join{Lateral}` over `Append{param probes}`.
- **(d)** The through-a-join case: Q95's parameterised Hash Join on the
  semi inner (M0146-0049d).
- **(e)** Parallel: a partial outer driving the parameterised Append
  (PG's Q54 Gather). `PathParamAppend` is not parallel-safe yet
  (M0146-0049e).
- **(f)** The IN/semi form: `li.id IN (SELECT item FROM cs1 UNION ALL …)`
  probes in PG, not in goopg (M0146-0049f).
