# M0146-0201 — a Subquery Scan is parallel-safe when its body is; the ANY_subquery wrapper is stripped

Status: done 2026-10-16 — TPC-H Q18 matches PG.

## Problem

TPC-H Q18 pulls `o_orderkey IN (SELECT l_orderkey FROM lineitem GROUP BY
l_orderkey HAVING sum(l_quantity) > 313)` up into a join against the
subquery RTE `ANY_subquery` (`convert_ANY_sublink_to_join`).

PG plans the whole join tree under one Gather:

```
Parallel Hash Join (orders.o_custkey = customer.c_custkey)
  Nested Loop
    Hash Join (Parallel Seq Scan on orders, Hash -> HashAggregate)
    Index Scan using idx_lineitem_orderkey_fkidx on lineitem
  Parallel Hash -> Parallel Seq Scan on customer
```

goopg instead hash-joined `customer` serially below the `lineitem` probe,
and printed `Subquery Scan on "ANY_subquery"` over the HashAggregate. Two
independent causes:

1. **No partial paths above `ANY_subquery`.** The DP trace's
   consider-parallel veto read `site=base rel={ANY_subquery} veto=B2
   leaf=other`. `relConsiderParallel` sends a planned subtree through
   `subtreeHasNoParallelHazard`, and that walk fails closed on any node it
   does not enumerate. `*SubqueryScan` was not enumerated, even though
   `parallelChildren` and every stamp/splice sibling already treat it as a
   pass-through (M0146-0005w).

   The rel was therefore unsafe. Every joinrel containing it got no
   partial path, and the Gather stamped over the serial tree. PG's
   `RTE_SUBQUERY` arm (allpaths.c) refuses only `limit_needed`, and its
   SubqueryScanPath is parallel_safe when its subpath is.
2. **The wrapper survived setrefs.** `stripTrivialSubqueryScans` counts a
   wrapper's consumption by `ColumnRef.SourceTableIdx == scan.src`.
   - The ANY leaf is planned in its own one-item FROM list, so `src` is 1
     in that list's numbering.
   - The link equality's reference to `ANY_subquery.l_orderkey` is
     synthesised with SourceTableIdx 0.
   - The outer query's first FROM item (`customer` in Q18, `o` in the unit
     probe) also has src 1, so its columns were counted as the leaf's
     consumption, failed to map, and kept the label.

   PG's `trivial_subqueryscan` removes the node: the subquery's one output
   is the link's whole consumption.

## Change

- `subtreeHasNoParallelHazard` (`considerparallel.go`) descends
  `*SubqueryScan`. The label has no expression channel of its own, and a
  `Limit` inside still refuses.
- `SubqueryScan.anyLink` (`plan.go`) marks the wrapper
  `pullUpAnyDerivedBody` (`jointreepullup.go`) builds. In
  `stripTrivialSubqueryScans`, its consumption is position 0, the link's.
  The remaining keep rules still apply: a leaf Filter, a computing Project,
  a computed Sort key.
- Tests:
  - `TestSubqueryScanIsParallelSafeWhenItsBodyIs`.
  - `TestAnySublinkSubqueryScanIsStripped`.
- A scratch server probe checked IN over GROUP BY/HAVING, DISTINCT, LIMIT,
  NOT IN and a computed body. Results were correct, and the wrapper is
  stripped exactly where its consumption is trivial.

## Measured

Captures in `analysis/m0146/m0146-0201/`, compared with the M0146-0200
captures:

- **TPC-H:** match 16 → 17 (Q18 MATCH). No other plan changed.
- **TPC-DS SF0.25 and SF1:** plan text byte-identical (84/99, 71/99).

The acceptance arm executed Q18's new parallel plan with values identical
(12 rows; 24 MATCH).

## Residual

The wrapper is still kept over a computed body that PG strips
(`IN (SELECT k+0 FROM l GROUP BY k)`), because goopg reads its body Project
as the leaf's own target list. This is the existing `projectComputes`
reading, not specific to ANY.
