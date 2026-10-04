# M0146-0005dy — a multi-relation semi-join RHS is unique-ified as one joinrel

Status: done (2026-10-04). Parent: M0146-0005. Filed by slice 115, from the
M0146-0005dk residual ledgered 2026-10-03.

## The divergence

In TPC-DS Q69, and below the first divergence in Q10 and Q35, PG
unique-ifies `store_sales ⋈ date_dim` on `ss_customer_sk` (HashAggregate)
and probes `customer_pkey` once per distinct customer. goopg ran a Parallel
Hash Semi Join.

PG's `populate_joinrel_with_paths` (`./postgres/src/backend/optimizer/path/joinrels.c`)
JOIN_SEMI arm calls `create_unique_path` on any rel equal to the semi join's
`syn_righthand`, whatever its size. M0146-0005dk built that producer for a
single base relation only (`createPulledBaseUniquePath`).

## Change

### The joinrel producer

- **Member rels.** `makeJoinRel` records the base rels a joinrel is made of
  (`RelOptInfo.memberRels`, read through `baseMembers`).
- **`createPulledJoinUniquePath`** unique-ifies a joinrel. It resolves each
  problem-space `ColumnRef` in `SemiRhsExprs` to the member whose output
  span contains it, checking the column name.
  - `numGroups` is the product of each member's `estimate_num_groups` over
    its own keys, clamped to the joinrel's rows.
  - The cost election (sort+unique against hashed) is the single-rel tail,
    now shared as `finishPulledUniquePath`.
- A parameterised subpath is refused, as PG's `create_unique_path` refuses
  one.

### Wrong results, already on HEAD

`addPathsForJointype` demotes a unique-ified pair (JOIN_UNIQUE_OUTER or
JOIN_UNIQUE_INNER) to an INNER join before any arm runs. Only
`nestLoopOuterPaths`, the hash arm and the NLI arm substituted the
unique-ified side. The merge arms and the partial nested loop read the
rels' path lists directly, so they joined the raw side, once per duplicate.

On HEAD `98eaf593a` with `enable_hashjoin = off`,
`SELECT count(*) FROM cu WHERE ck IN (SELECT ck FROM ss WHERE dk < 30)`
returned 3625 rows; PG returns 725. The plan was a partial nested loop
driven by the raw `ss`. Forcing a serial merge gave the same count.

The arms now follow PG through `mergeUnique{side, path}`, the unique-ified
side's `create_unique_path` result:

| arm | PG (`joinpath.c`) | goopg |
|---|---|---|
| `sort_inner_and_outer` | :1406-1418 substitutes the side; :1423-1445 no partial for UNIQUE_OUTER, UNIQUE_INNER only if the unique path is parallel-safe | `sortInnerAndOuter`, `addPartialMergeJoinPath(innerOverride)` |
| `match_unsorted_outer` merge half | :2001-2003 no merge for UNIQUE_OUTER; :1881-1890 the unique inner only; :1636-1638 no presorted-inner search | `matchUnsortedOuterMerge`, `generateMergeJoinPaths(uniqueInner)` |
| parallel merge | :2017-2050 | `matchUnsortedOuterMergePartial` |
| `consider_parallel_nestloop` | :2017-2031 no UNIQUE_OUTER; :2165-2178 the unique cheapest-total inner only; :2134 no matpath | `addPartialNestLoopPaths` |

## Effect

- **Repro.** The repro query is
  `cu c WHERE EXISTS (SELECT 1 FROM ss, dd WHERE c.ck = ss.ck AND ss.dk = dd.dk AND dd.yr = 2001 AND dd.dk < 30)`.
  - Its default plan is PG's node for node: Nested Loop → HashAggregate
    (Group Key `ss.ck`) → Gather → Hash Join `ss ⋈ dd`, probing `cu_pkey`.
  - The value matches PG's 150 under every forced arm.
- **Fire set.** Matches are flat, SF0.25 41 and SF1 32. Q10 and Q69 fired at
  SF0.25.
  - CATEGORIES-EXCL-MATCH at SF0.25 (SF1 flat):

    | category | before | after |
    |---|---|---|
    | aggregation-strategy | 15 | 13 |
    | parallelism | 31 | 29 |
    | join-method | 27 | 26 |
    | sort-strategy | 27 | 26 |
    | scan-type | 28 | 29 |
  - **Q69** is down to join-order only. The store\_sales semi side now matches
    PG, and the plan total is 43430 against PG's 43133.
  - **Q10** lost aggregation-strategy and parallelism, and gained scan-type.
    Its first divergence is still the sublink restriction (M0146-0005dx1
    and dx2).
- **Other gates.**
  - The sweep passed 96/96.
  - The TPC-H arm matched 24/24, and tpch-spotcheck passed.
  - ea-ratchet passed. Its "Q92 fixed" line predates this change.
  - Regress A/B over 18 planner cases moved two:
    - `join`: the `t4 ⋈ t5 ⋈ t6` EXISTS now plans PG's Nested Loop over a
      HashAggregate probing `t3` (the diff fell by 2 lines).
    - `subselect`: only the parallel-worker NOTICE lines were reordered,
      which is nondeterministic.

Tests:

- `TestUniqueifiedSemiJoinrelRows` (executor) fails on HEAD (1825 rows
  against 365). It forces four arm sets and asserts PG's shape with
  `enable_hashjoin = off`.
- Three optimizer tests need a planned Semi join. Their stats-free fixture
  now unique-ifies, and PG unique-ifies the same shapes when unanalyzed.
  They moved to `analyzedUniqueThreeTablesCatalog`, on which PG 18.3 elects
  Hash Semi Join.

## Not done (ledgered)

- **Constant-pinned keys.** The unique-ify groups by every `SemiRhsExpr`,
  including one an equivalence class pins to a constant. Regress `join`
  shows Group Key `t4.a, t5.a`, where PG's is `t5.a`.
- **Merge semi join.** A forced merge plan still lacks PG's Merge Semi
  Join. SEMI stays merge-declined (M0142-0008a-3(iii)).
- **Q69's anti joins.** goopg places the web\_sales anti join above the
  probe chain; PG nests it inside.
