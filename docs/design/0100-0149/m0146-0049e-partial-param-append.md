# M0146-0049e — a partial outer drives the parameterised Append

Status: done 2026-10-07 (`e75280c05`).

## Symptom

PG 18.3 runs TPC-DS Q54's `my_customers` part as:

```
Gather
  -> Nested Loop
       -> Nested Loop
            -> Parallel Seq Scan on item
            -> Append
                 -> Bitmap Heap Scan on catalog_sales   (probe cs_item_sk = i_item_sk)
                 -> Index Scan using web_sales_pkey      (probe ws_item_sk = i_item_sk)
       -> Index Scan using date_dim_pkey on date_dim
```

goopg built the parameterised Append (`PathParamAppend`, M0146-0049) but
kept the nested loop over it serial.

## Cause

Four refusals stood between the path and a Gather:

1. **`PathParamAppend.ParallelSafe` was never set,** so
   `try_partial_nestloop_path`'s port (joinpathsnli.go, veto V7) refused it
   as an inner. Its member rels (`newRelOptInfo`) also never ran
   `set_rel_consider_parallel`, so their probes were parallel-unsafe too.
2. **The gather whitelist** `partialPathDrivingKind` admitted only a bare
   index or bitmap probe as a lateral inner, not an Append of them.
3. **The plan-node twin** `lateralProbeIsPartialProbe`, and through it every
   planner gate keyed on `lateralProbeJoinIsPartialCapable` (`drivingScan`,
   `stampParallelScan`, `HasShareableHashJoin`, …), admitted only a bare
   index probe.
4. **The executor twin** `lateralProbeJoinPartial` admitted only an index
   scan operator. Separately, the claim walks (`attachParallelScan`,
   `attachParallelBitmapScan`, `attachParallelIndexScan`) refuse a nested
   loop whose inner holds any bitmap scan, and Q54's members probe by
   bitmap.

## PG

- `create_append_path` (pathnode.c:1339, :1380): the Append is parallel-safe
  when the rel considers parallelism and every subpath is parallel-safe.
- Each appendrel child gets its own `set_rel_consider_parallel`
  (allpaths.c:589), bounded by the parent's.
- `try_partial_nestloop_path` needs only a parallel-safe inner. Every worker
  re-runs the parameterised inner for its own outer rows.

## Change

- **paramappend.go.**
  - `member.ConsiderParallel = rel.ConsiderParallel &&
    relConsiderParallel(base, tbl, cat)`.
  - `PathParamAppend.ParallelSafe = parallelSafeWith(rel, children...)`.
- **Path twin (gatherpaths.go).** The NestLoop arm admits a
  `PathParamAppend` inner (not Memoize-wrapped) when every member passes
  `paramProbePathIsPartialProbe`: either a parameterised `PathIndexScan`
  with index clauses, or a `PathBitmapHeapScan` over exactly one
  `PathBitmapIndexScan`. The V8 subset test is re-checked.
- **Node twin (parallel.go).** `lateralProbeIsPartialProbe` gains a
  `*SetOp` arm, `paramAppendIsPartialProbe`. It requires a UNION ALL chain
  whose leaves, under Project/Filter, are bare index probes or bitmap probes
  (`nliBitmapProbeIsPartialProbe`). `LateralParamAppendProbe(j)` exports
  "admitted lateral join with an Append inner" for the executor.
- **Executor twin (parallel_scan.go).** `lateralProbeJoinPartial` admits a
  `*setOp` inner whose leaves (`paramAppendOpsArePartialProbes`, through
  setOp/project/filter/instrumented) are index or bitmap scan operators.
  The three `HasBitmapScan(x.plan.Right)` refusals skip this shape.
  - Why that is safe: no claim walk descends a nested loop's right side,
    and `collectBitmapScans` reaches only the outer. So each worker's
    member bitmap op takes no claim and builds a private TIDBitmap per
    rescan, the argument M0146-0005 slice 27 made for the fused NLI bitmap
    probe.
  - Each member re-binds through `ctx.OuterRows` exactly as in serial
    execution; the `setOp` operator is not `lateralBindable`.

## Verification

- `TestParallelParamAppendProbeIdentity`:
  - two indexed inner tables under UNION ALL, 100 outer rows, 100,000
    non-matching filler rows per inner;
  - asserts the lateral param-Append plan and the planner twin;
  - runs a Gather at 1/2/4 workers under both probe multipliers: 180 rows
    each, equal to serial.

  With the executor arm disabled it fails with 360 rows (the N-copy
  signature), so the pin is not vacuous.
- On an SF0.25 clone, Q54 plans `Gather → NL(NL(Parallel Seq Scan item,
  Append(bitmap probes)), date_dim)`, PG's inner shape. The
  `cs_or_ws_sales × item` core runs with Workers Launched: 1 and returns
  the same aggregate parallel, serial and on PG 18.3:
  `13607|673460188|33246550157|451`.
- Gates:
  - units, tpch-spotcheck, arm 24/24, ea-ratchet PASS;
  - fire set: SF0.25 fires = Q54 only (PASS in both arms), SF1 no fires;
  - sf025 96/96, plan shapes 98 same / 1 changed, no runtime move.

## Not covered

- Q54's first-divergence category does not move: its plan still differs
  from PG upstream of this subtree.
- At SF1 goopg elects one Gather over the whole join tree, so the
  `item × Append` NL runs serial inside a Hash build under the
  store_sales Gather. PG instead uses two sibling Gathers under a serial
  nested loop. A parallel-path election difference, ledgered.
- A Memoize-wrapped parameterised Append and non-UNION-ALL members stay
  refused.
