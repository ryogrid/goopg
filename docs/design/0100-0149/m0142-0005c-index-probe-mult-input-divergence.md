# M0142-0005c — indexProbeCostMultiplier: input divergence, not a missing term

Kind: recon. Parent: M0142-0005.

Status: recon complete, no production change (DONE 2026-09-19).

M0142-0005b streamed the index-probe executor and re-measured: plan captures
came back byte-identical, so executor laziness was not what kept
`indexProbeCostMultiplier=2.0` load-bearing (`GOOPG_INDEX_PROBE_MULT=1` still
flips TPC-H Q9/Q10/Q14 to NL+index shapes PG rejects, and still *improves*
TPC-DS parity). This recon asked the next question: which `cost_index` term
does goopg's formula omit or mis-weight?

**Answer: none. The `cost_index` port is faithful; the multiplier masks a
divergence in the *inputs* the formula is fed — above all the physical heap
correlation of the benchmark corpus itself.**

Evidence and full numerics: `analysis/m0142/m0142-0005c-probe-cost-attribution.md`.

## What is already faithful

`internal/optimizer/costindex.go` reproduces `cost_index`
(`postgres/src/backend/optimizer/path/costsize.c:560`) end to end:

- `index_pages_fetched` (Mackert-Lohman, `costsize.c:908`) — both the
  tuple-grain and page-grain calls, the `T <= b` / `T > b` regimes and the
  `effective_cache_size` pro-ration over `total_table_pages` (262144 pages =
  2 GB on both engines).
- The `loop_count > 1` repeated-scan arm (`costsize.c:598-640`): total-fetch
  Mackert-Lohman pro-rated back by `loop_count` — the amortisation that makes
  the Nth probe cheaper than the first.
- The correlation blend `maxIO + corr²·(minIO − maxIO)` (`costsize.c:795-797`).
- `genericcostestimate` + `btcostestimate` for the index side
  (`selfuncs.c:7051`, descent `tree_height+1` pages × `pageCPUMultiplier` =
  50 `cpu_operator_cost`).
- `get_loop_count` (`indxpath.c:2328`): smallest filtered row estimate among
  the required-outer rels — `loopCountFor` (`pathparamindex.go:524`) ports it
  verbatim.

A Python replay of the port reproduces *both* engines' measured probe costs to
within ~1% when fed each engine's own inputs (goopg 3.8248 sim vs 3.8249
DPPATH; PG-input 7.07 vs 7.13 measured on the same logical probe).

## The actual divergence — inputs, all pointing the same way

For the Q9 `part ⋈ partsupp` probe (the plan-flip site):

| input | goopg | PG 18.3 | Δ probe cost |
|---|---|---|---|
| correlation (leading key) | **1.0** | 0.845 | +0.92 |
| loopCount (outer filtered rows) | 12121 | 6061 | +0.78 |
| index real pages | 1092 | 2198 | +0.37 |
| relPages | 18916 | 18047 | −0.04 |
| interaction | | | +1.24 |
| **total** | **3.82** | **7.13** | |

### 1. Corpus physical clustering (dominant)

Every probe-relevant key column measures `correlation = 1.0` on goopg's TPC-H
heap vs PG's 0.19–0.85. Verified physical, not a stats artifact: goopg's
`partsupp` heap has **zero** block-boundary key inversions (perfectly clustered
on `ps_partkey`); PG's reference heap has ~12K. HammerDB's generator emits rows
in key order and goopg's load path preserves that into the heap; PG's reference
cluster carries a fragmented layout. With `corr=1.0`, `csquared=1` pins the
heap-fetch blend at `min_IO_cost`, so a probe is priced like a sequential read
of `ceil(sel·pages)` pages — exactly the case `indexProbeCostMultiplier`
multiplies up.

This is why the scalar is *linearly* load-bearing on TPC-H and why it *hurts*
TPC-DS, whose columns measure honest fractional correlations (0.01–0.66) —
there the blend already charges uncorrelated fetches and the 2× double-counts.

### 2. loopCount via selectivity inputs

`part WHERE p_name LIKE '%green%'`: goopg 12121, PG 6061, actual ≈10650 —
a 2× `patternClauseSelectivity` divergence flowing into `loopCount`
(`get_loop_count` reads the outer rel's filtered estimate). ~24% of the Q9
probe gap.

### 3. Index geometry

Real `relpages` differ ~2× (goopg B-tree packs denser: 1092 vs 2198 pages for
`partsupp_part_fkidx`). `estimateIndexGeometry` already reads
`catalog.IndexRealPages`; the divergence is the storage engines' real sizes,
not the estimator.

## Secondary findings (filed as children)

- **ANALYZE correlation exceeds 1.0 on nullable columns** — `cs_catalog_page_sk`
  measures 1.0019597. `corrPairs` records the raw sample `pos`
  (`operators_analyze.go:1294`), which is *sparse* when nulls are skipped,
  while the closed form (`n·Σxy − Σx² / n·Σx² − Σx²`) requires both axes to be
  permutations of `0..nonNull−1`. PG assigns `tupno = values_cnt` — a
  *contiguous* non-null index (`analyze.c:2495`). → **M0142-0005d** (impl).
- **EXPLAIN prints `DeriveLegacyDisplayCost` for fused-NLI inner scans** — the
  `*IndexScan` built at `nl_index_join.go:666` never passes `stampPlanCost`,
  so EXPLAIN shows `cost=0.00..0.18` while the search costed the probe at
  4.59. Display-only defect; it misleads plan-diff attribution.
  → **M0142-0005e** (impl).
- **Corpus-layout parity decision** — whether to rebuild the TPC-H bench heap
  in PG-reference physical order or accept the divergence and keep the scalar.
  → **M0142-0005f** (recon).
- **patternsel 2× divergence** feeding `loopCount`. → **M0142-0005g** (recon).

## Verdict

`indexProbeCostMultiplier=2.0` stays. It is a corpus-wide approximation of the
heap-fetch cost PG charges for a fragmented heap — crude but directionally
correct on TPC-H, harmful on TPC-DS. There is no PG term to port and no better
scalar: the honest fixes are the input-parity children above. The flat
`indexProbeCost()` helper (`cost_funcs.go:1104`) is test-only; all production
probes flow through `costIndexScanCore`, where the multiplier scales every
`random_page_cost` term (`costindex.go:256,264,270,275,382,385`).
