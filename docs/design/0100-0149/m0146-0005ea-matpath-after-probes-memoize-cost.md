# M0146-0005ea — the materialised inner comes after the index probes, and Memoize charges its first entry

Status: done (2026-10-04). Parent: M0146-0005. Filed by M0146-0005dx1.

## The divergence

After M0146-0005dx1, TPC-DS Q10's only difference from PG was the
`customer_address` join.

| | `customer_address` join | join qual |
|---|---|---|
| PG 18.3 | `customer_address_pkey` probe | Index Cond |
| goopg | Nested Loop over Materialize(Seq Scan) | Join Filter |

The two paths cost about 21980879 and 21984115, within STD\_FUZZ\_FACTOR.
Under LIMIT, `consider_startup` is on, and the tight 1.0000000001
comparison is COSTS\_DIFFERENT (the probe's startup is 0.25 higher). So
`add_path` keeps whichever path was filed first:

- **PG** files the `cheapest_parameterized_paths` loop (bare inner,
  parameterised probes, their Memoize) before the materialised inner
  (`match_unsorted_outer`, `./postgres/src/backend/optimizer/path/joinpath.c`).
  The probe is its incumbent.
- **goopg** filed the materialised inner first, inside `addNestLoopPath`.

## The first attempt and Q8

Moving the matpath after `addNLIPaths` made Q10 match at both scales. But it
also cost Q8 its match at both scales, and ea-ratchet failed on a new
`Q8:date_dim+store+store_sales` node. The attempt is preserved under
`analysis/m0146/m0146-0005dx1/`.

The instrumented PG 18.3 (M0144-0005, `debug_plan_candidates`, scratch
cluster on :5560) traced Q8's candidates
(`analysis/m0146/m0146-0005ea/q8-pg-plancand.txt`). At
`{store_sales, date_dim, store}`, PG files:

| path | cost | verdict |
|---|---|---|
| plain `store_pkey` probe nested loop | 3069.35..19150.2 | incumbent |
| its Memoize twin | 3069.36..19043.2 | rejected `via=tie` |

The Memoize twin is cheaper on total but 0.01 dearer on startup.
`create_memoize_path` (`./postgres/src/backend/optimizer/util/pathnode.c`)
charges the subpath's costs **plus one cpu\_tuple\_cost**. With the Memoize
gone:

- the rel's cheapest total is a Gather Merge (18949.7..19045.6);
- a hash join above it starts late;
- the bushy nested loop (12332.4..28504.9) wins the top level.

goopg's Memoize wrapper copied its subpath's costs exactly. With
the reorder, the Memoize twin (3050.35..19023.1, the same startup as the
plain probe) beat the plain probe outright, became the rel's cheapest path,
and let a hash join on top undercut the nested loop.

## Change

- **The order.** `addNestLoopPath` files only the bare inner. The new
  `addMaterialNestLoopPath` files the materialised inner and runs after
  `addNLIPaths`, as `match_unsorted_outer` does.
- **The Memoize cost.** `getMemoizePath` charges its subpath's startup and
  total plus `cpu_tuple_cost`, as `create_memoize_path` does.

## Effect

- **Fire set.** Q8 keeps its match; ea-ratchet passed.

  | metric | SF0.25 | SF1 |
  |---|---|---|
  | matches | 41 → 42 (Q10) | 32 → 33 (Q10) |
  | join-order | 50 → 49 | — |
  | scan-type | 30 → 29 | 35 → 36 |
  | qual-placement | 12 → 11 | 9 → 11 |
  | join-method | — | 26 → 24 |
  | aggregation | — | 20 → 19 |

  - Q35 at SF1 went from 2 categories to 6 (ledgered).
  - Q26 and Q59 changed categories at SF1.
- **Other gates.**
  - The sweep passed 96/96.
  - The TPC-H arm matched 24/24, and tpch-spotcheck passed.
  - Regress A/B over 18 planner cases showed noise only.

Tests:

- `TestMaterialNestLoopOfferedAfterIndexProbes` fails on HEAD (the offer
  order was `join.nestloop, join.nestloop, nestloop.index`).
- `TestGetMemoizePathGates` now pins the subpath's cost plus
  cpu\_tuple\_cost.

## Not done (ledgered)

- **Q35 at SF1** now differs in join-order, scan-type, parameterisation
  and qual-placement as well as sort and parallelism. PG reads
  `customer_address` through Gather Merge + Incremental Sort below a
  Materialize, and goopg's new election does not reach that shape.
- **Per-outer interleaving.** PG interleaves per OUTER path: the
  bare/probe/Memoize/matpath group runs for one outer path before the next.
  goopg runs each arm over every outer path in turn, so ties between
  different outer paths can still resolve differently.
