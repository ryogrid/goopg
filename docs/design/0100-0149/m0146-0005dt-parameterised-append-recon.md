# M0146-0005dt — recon: what Q54's parameterised Append needs

Status: done (2026-10-03, recon). Parent: M0146-0005dp (banner item 3).

## The plan PG builds

TPC-DS Q54's `my_customers` subquery reads a UNION ALL of `catalog_sales`
and `web_sales` joined to `item`. At SF0.25, PG 18.3 plans:

```
Nested Loop
  ->  Parallel Seq Scan on item   (i_category = 'Music' AND i_class = 'country')
  ->  Append
        ->  Bitmap Heap Scan on catalog_sales
              Recheck Cond: (cs_item_sk = item.i_item_sk)
              ->  Bitmap Index Scan on catalog_sales_pkey
        ->  Index Scan using web_sales_pkey on web_sales
              Index Cond: (ws_item_sk = item.i_item_sk)
```

The subtree costs 6537. goopg's subtree costs 19832: it is a Parallel Hash
Join of a Parallel Append, which seq-scans both fact tables, against `item`.

## The PG mechanism

- **Members are rels.** `pull_up_simple_union_all` makes each member a
  child rel of the appendrel. `set_append_rel_size`
  (`./postgres/src/backend/optimizer/path/allpaths.c:956`) translates the
  parent's quals into each child, and each child builds its own base
  paths. This includes index paths parameterised by the join clause
  `cs_item_sk = item.i_item_sk`, translated from the parent's
  `item_sk = i_item_sk`.
- **Parameterised Append paths.** `add_paths_to_append_rel`
  (`allpaths.c:1321`) collects every child's parameterisations
  (`all_child_outers`, `allpaths.c:1343` / `:1500`). For each one, it asks
  `get_cheapest_parameterized_child_path` (`allpaths.c:2048`) for a child
  path with that `required_outer`, and builds
  `create_append_path(..., required_outer, ...)`
  (`./postgres/src/backend/optimizer/util/pathnode.c:1303`). That gives
  an Append path parameterised by `item`.
- **Parameters bind into any inner subtree.** `create_nestloop_plan`
  (`./postgres/src/backend/optimizer/plan/createplan.c:4341`) turns the
  inner's outer references into `NestLoopParam`s
  (`replace_nestloop_params`, `createplan.c:5036`). The executor rescans
  the whole inner subtree per outer row, and `ExecReScanAppend`
  (`./postgres/src/backend/executor/nodeAppend.c:421`) propagates the
  changed params to each child.

## What goopg has

- **The appendrel exists, but only as a hoist.** M0145-0004 /
  M0145-0004a flatten a FROM-clause UNION ALL into a leaf whose partial
  path is re-targeted from the nested scope's SETOP rel
  (`addAppendRelPartialPaths`, jointreeappendrel.go). The members stay
  finished nodes inside the leaf's nested scope; they are not rels of
  the parent search. That design doc's deferral list records "member
  rtable entries / parent-qual distribution into members" as not done.
  Q54's plan already prints the flattened `Parallel Append` over
  `catalog_sales` / `web_sales`.
- **There is no Append path kind.** `PathKind` (path.go) has no Append. The
  hoist files prebuilt paths.
- **A parameterised inner must be a single `*IndexScan`.** goopg's only
  parameterised nested loop is the NLI. `createplannl.go`'s header
  states it: the inner of `*NestedLoopIndexJoin` is typed `*IndexScan`
  because the driver calls `Rescan` on it with the outer slot bound.
  There is no `NestLoopParam` over a general inner subtree: no Append,
  no join (the Q95 shape in M0146-0005dq), no Bitmap Heap Scan inner.
- **The executor substrate largely exists.** `lateralJoinStream`
  (internal/executor/join_lateral_stream.go) re-executes an arbitrary right
  subtree once per outer tuple, with the outer row bound through
  `ctx.OuterRows`. That is goopg's equivalent of PG's per-outer-tuple
  rescan. Whether an index scan inside that subtree can probe with an
  `OuterColumnRef` key is the open question M0146-0012 also carries.

## Conclusion

M0146-0005dp needs a feature no open task owns: **a parameterised inner
path through a non-scan node** (Append, join). That needs three pieces:

1. per-member parameterised paths for a flattened UNION ALL leaf;
2. a parameterised Append path built from them;
3. a general lowering of a parameterised inner into a per-outer-row
   rescan.

The same blocker is the one the owner sequenced on 2026-09-25 for
M0145-0008ac / M0145-0008y ("parameterised inner paths through a join").
That decision names the "M0146-0012 / M0145-0010 family", but M0145-0010
is closed and M0146-0012 covers only correlated base-rel index quals. Filed
as **M0146-0049**, with M0146-0005dp marked `[!]` on it.

## Expected movement (S5)

- **Q54** (M0146-0005dp). The `my_customers` subtree should become PG's
  parameterised Append, roughly 19832 → about 6537. That moves Q54's
  `join-method` / `parameterisation` first divergence at SF0.25 and SF1.
- **Q95** (M0146-0005dq; M0145-0008ac's preserved patch). The
  parameterised Hash Join on the semi inner. The SF1 timeout that
  blocked 0008ac goes away once the inner probes by parameter.
- **TPC-H Q2** (M0145-0008y), through the M0146-0012 half.
- **Measurement:** the fire set at both scales (`CATEGORIES-EXCL-MATCH`
  `parameterisation` / `join-method`, match), plus the sweep values gate.
