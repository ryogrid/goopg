# M0146-0049 — a parameterised inner path through a non-scan node

Status: in progress (slice a — recon — done 2026-10-03). Parent: M0146
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
  semi inner.
