# M0142-0005c — probe-cost input attribution (recon evidence)

Date: 2026-09-19. Recon loop #16, task M0142-0005c (Kind: recon — no production
code touched). Question carried over from M0142-0005b: why does
`indexProbeCostMultiplier=2.0` stay load-bearing after the streamed probe?

## Method

- `GOOPG_PGSHAPED_DP_TRACE=1` server on the private TPC-H clone
  (`tmp/goopg-spotcheck-tpch-data`, port 5534, `GOOPG_INDEX_PROBE_MULT=1`),
  `EXPLAIN` for Q9/Q10; DPPATH/DPTRACE harvested from
  `tmp/m0142-0005c/server-mult1.log`.
- PG 18.3 reference `:65432` (read-only): forced-NL plans via session GUCs
  (`enable_hashjoin/mergejoin off`), catalog stats via `pg_stats`/`pg_class`.
- A Python re-implementation of `costIndexScanCore` + `btreeIndexAMCostPages` +
  `indexPagesFetched` (`internal/optimizer/costindex.go`) used to replay both
  engines' observed costs term-by-term.

## Result 1 — the formula is faithful; the inputs diverge

Replaying the Q9 `partsupp` probe (`partsupp_part_fkidx` parameterized by
filtered `part`):

| | goopg sim | goopg DPPATH | PG-input sim | PG forced-NL |
|---|---|---|---|---|
| probe total | 3.8248 | 3.8248689 | 7.0676 | 7.13 |

Input-swap ladder (goopg inputs → PG inputs, one at a time, baseline 3.8249):

| input | goopg | PG | Δ cost |
|---|---|---|---|
| correlation (ps_partkey) | 1.0 | 0.8450783 | +0.918 |
| loopCount (=min outer filtered rows) | 12121 | 6061 | +0.779 |
| indexPages (real relpages) | 1092 | 2198 | +0.365 |
| relPages | 18916 | 18047 | −0.035 |
| totalTablePages | ≈186k | ≈180k | ±0 |
| **all swapped** | | | **7.07 ≈ 7.13 ✓** |

No missing term: descent, Mackert-Lohman (both tuple-grain and page-grain arms),
correlation² blend, qpqual charge, and pro-rating by `loop_count` are all
already ported (`costindex.go:233-303, 327-404, 421-460`; oracle
`costsize.c:560,795-797,908`, `selfuncs.c:7051`).

## Result 2 — the dominant input is heap physical correlation

`pg_stats.correlation` for probe-leading key columns:

| column | goopg :5534 | PG :65432 |
|---|---|---|
| partsupp.ps_partkey | 1.0 | 0.845 |
| lineitem.l_orderkey | 1.0 | 0.195 |
| orders.o_orderkey | 1.0 | 0.194 |
| part.p_partkey | 1.0 | 0.846 |
| non-key columns | ≈0 | ≈0 (agree) |

Block-boundary check (max(key) of block N vs min(key) of block N+1):
goopg partsupp has **0 inversions** — the heap is genuinely, perfectly clustered
on ps_partkey. PG's heap has ~12,235 inversions → 0.845 honestly. goopg's
`correlation=1.0` is a FAITHFUL measurement of a physically clustered corpus,
not an analyzer artifact: the HammerDB load inserts in key order, so goopg's
heap lands in near-sorted physical order while PG's reference heap (loaded
through a different path/history) is fragmented.

Consequence inside `cost_index`: `csquared=1` pins the heap-fetch blend at
`min_IO_cost` (sequential read of `ceil(sel·pages)` pages) instead of the
uncorrelated `max_IO_cost`. On the loop arm the entire probe becomes
`≈ mult · (index pages + minIO pages) · random_page_cost / loopCount` — i.e.
the probe cost is ~linear in the multiplier precisely because correlation
collapsed everything else.

## Result 3 — loopCount inherits a 2× selectivity divergence

`get_loop_count` port is verbatim (`pathparamindex.go:524` vs
`indxpath.c:2328`: min over required-outer rels' filtered `rows`). The inputs
differ: `part WHERE p_name LIKE '%green%'` estimates 12121 (goopg) vs 6061
(PG), actual ≈ 10,650 — goopg's `patternClauseSelectivity` lands 2× PG's, on
the other side of the truth (PG under-, goopg over-estimates). This feeds
`loopCount` directly and accounts for ~24% of the Q9 probe gap.

## Result 4 — EXPLAIN's inner-probe cost is display-only for fused NLI

The parameterized path the search costed (DPPATH): `index.parameterised
relids={lineitem} reqouter={orders}` → `startup=0.375 total=4.59`. The chosen
plan's EXPLAIN prints `cost=0.00..0.18` — that is `DeriveLegacyDisplayCost`
on the *fused* `NestedLoopIndexJoin.Inner` `*IndexScan` node
(`nl_index_join.go:666` builds the node bare; `stampPlanCost` never sees it),
NOT the costed path. Real contest for {orders,lineitem} at mult=1 (DPPATH):
partial-NL **95,348** vs partial-hash **351,384** → NL wins. PG's equivalent
probe cost 11.09 (earlier forced-NL capture) → PG's NL loses to hash.

## Result 5 — analyzer correlation can exceed 1.0 on nullable columns

TPC-DS SF0.25 clone (`:5533`): `catalog_sales.cs_catalog_page_sk` correlation
= **1.0019597** — impossible for a Pearson coefficient. Cause: `corrPairs`
records `pos` = raw index into `sample` (`operators_analyze.go:1294`), which is
SPARSE when nulls are skipped, while the closed form assumes x and y are both
permutations of `0..nonNull-1`. PG assigns `values[values_cnt].tupno =
values_cnt` — a contiguous non-null index (`analyze.c:2495`), keeping the
closed form valid. Bug is bounded to nullable columns; NOT the cause of the
TPC-H `1.0` readings (those columns are NOT NULL and the heap is really
ordered).

## Why mult=2 "works" on TPC-H and hurts TPC-DS

TPC-H: every probe-relevant key column is perfectly clustered (corr=1.0) → all
probes collapse to the minIO arm → multiplying random-page terms by 2 re-adds
roughly the heap-fetch cost PG charges for its fragmented heap. TPC-DS:
correlations are honestly fractional (0.01–0.66) → the blend already prices
uncorrelated fetches → mult=2 double-charges → mult=1 measured better parity
(scan-type 59→51, join-order 91→88, qual-placement 20→24).

**Conclusion:** the scalar masks an input-parity gap (corpus physical layout +
selectivity inputs), not a missing formula term. Retiring it requires either
corpus parity (physically re-order the TPC-H heap like PG's reference) or
accepting the divergence; neither is a `cost_index` patch.

## Children filed

- M0142-0005d (impl): ANALYZE correlation contiguous-tupno + clamp [-1,1].
- M0142-0005e (impl): stamp real path cost on fused-NLI inner IndexScan.
- M0142-0005f (recon): corpus physical-layout parity decision (rebuild vs accept).
- M0142-0005g (recon): patternsel 2× divergence feeding loopCount.
