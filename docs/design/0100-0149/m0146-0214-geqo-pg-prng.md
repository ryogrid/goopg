# M0146-0214 — GEQO runs on PG's pg_prng and pg_qsort

Status: slices 1 (2026-10-16), 3 and 4 (2026-10-19) landed; open. GEQO's random walk reproduces PG 18.3's draw for
draw.

- **SF1 Q64 MATCHes PG since slice 4.** SF0.25 Q64 still diverges: from `sort_pool` on, the walk follows tour fitness
  (path cost).
- **Slice 2 (§6)** attributed that to physical inputs. The owner's 2026-10-19 reload cleared them to within an index
  page or two.
- **Slices 3 (§7) and 4 (§8)** compared tours against PG and fixed four costing and estimating classes:
  - the subquery-leaf cost;
  - the eqjoinsel MCV arm (M0146-0230);
  - merge clauses matched to outer pathkeys through equivalence classes;
  - hash\_inner\_and\_outer's cheapest-startup outer.
- **Remaining classes:** Gather Merge over partial paths on GEQO joinrels for cartesian tours, and the last index
  pages (M0146-0216).

## 1. The record

TPC-DS Q64 is the only TPC-H/TPC-DS query whose join list crosses `geqo_threshold = 12`. It diverged at both
scales (`join-order`, SHAPE-DIFF), classed GEQO-RNG and waived until the owner's 2026-10-16 directive un-waived it.
PG's `geqo_seed = 0` is deterministic (`geqo_set_seed` → `pg_prng_fseed`), so a planner that makes PG's draws in
PG's order, against PG's fitness values, picks PG's tour.

## 2. What differed

goopg's GEQO (`internal/optimizer/geqo.go`) was a structural port of `geqo_main.c`, `geqo_pool.c`,
`geqo_selection.c`, `geqo_erx.c` and `geqo_recombination.c`. Its randomness and one sort were its own:

| site | goopg before | PG 18.3 |
|---|---|---|
| generator | Knuth 64-bit LCG, state 1 for seed 0 | `pg_prng`: splitmix64-seeded xoroshiro128** (`pg_prng.c`) |
| seed | `geqoSeedState`: seed × MaxInt64/2 | `pg_prng_fseed`: seed × (2^52 − 1) |
| `geqo_rand` | top 31 bits / 2^31 | `pg_prng_double`: ldexp(v >> 12, −52) |
| `geqo_randint` | `lower + int(rand × width)`; always draws | `pg_prng_uint64_range`: bitmask rejection; a one-value range draws nothing |
| `init_tour` | loop from i = 0, a draw at i = 0 | `tour[0] = 1`, loop from 1 |
| `linear_rand` | `(bias − √) / (2(bias − 1)) × max` | `max × (bias − √) / 2.0 / (bias − 1.0)`; √ only when > 0 |
| `sort_pool` | stable insertion sort | `qsort` (`pg_qsort`, not stable) |
| `gimme_edge` | matched only a positive entry | matches `abs(edge)` |
| `gimme_pool_size` override | taken when > 0 | taken when ≥ 2 |

The sort matters as much as the generator. Different tours often build the same join tree, so equal worths are
common. `geqo_selection` then draws pool positions, so the order `pg_qsort` leaves the ties in decides which tours
breed.

## 3. Landed

- `internal/utils/pgprng`: `src/common/pg_prng.c`. It has `Seed`, `FSeed`, `Uint64`, `Uint64Range` and `Double`.
  The ANALYZE block and row samplers (`analyze_block_sampler.go`, M0138-0002) already carried a private copy; they
  now use this package, so there is one implementation (pattern: sibling paths agree).
- `internal/utils/pgqsort`: `src/port/qsort.c`, the `pg_qsort` instantiation of `sort_template.h`. It is
  Bentley-McIlroy with med3/ninther pivots, insertion sort below 7 and the presorted exit; generic over the element.
- `geqo.go`:
  - `geqoRNG` wraps `pgprng.State`;
  - `initTour`, `linearRand`, `gimmeEdge` and the pool-size rule are PG's statement for statement;
  - `sortPool` is `pgqsort.Sort` with `geqo_pool.c`'s `compare()`.
  - The draw sites were audited one by one: `init_tour`, `linear_rand` ×2 + retries in `geqo_selection`,
    `gimme_tour`'s start, `gimme_gene`, and `edge_failure`'s two arms. Each passes PG's (lower, upper). Note that
    PG's `geqo_randint(root, upper, lower)` takes upper first.
- The dispatch audit (`relfromjoinlist.go`) is equivalent: `len(items) >= geqo_threshold` is PG's
  `levels_needed >= geqo_threshold`.
- Stale "GEQO is a no-op stub" comments were corrected in `defaults.go`, `postgresql.conf.sample` and
  `geqo_guc_stubs_test.go`.

## 4. Oracles (PG's own code, no server)

- `pgprng/testdata/prng_harness.c` links `libpgcommon.a`. `TestMatchesPGPrng` reproduces its streams exactly:
  fseed 0, 0.5, −0.25, 1, 0.123456789; seed 0 and 12345; every draw kind; the no-draw ranges.
- `pgqsort/testdata/qsort_harness.c` runs `pg_qsort` (libpgport) with GEQO's comparator over 75 arrays. The arrays
  are heavy-tie, light-tie and tie-free random, plus presorted and reversed, at sizes around 7 and 40.
  `TestMatchesPGQsort` reproduces every permutation.
- `optimizer/testdata/geqo_draws_harness.c` compiles PG's own `geqo_random.c`, `geqo_recombination.c`,
  `geqo_selection.c`, `geqo_copy.c` and `geqo_erx.c`; only `palloc` and `elog` are stubbed. It prints, per seed and
  size, the `random_init_pool` tours, twelve `geqo_selection` pairs and the ERX children (890 lines).
  `TestGEQODrawsMatchPG` reproduces them line for line.
- Mutations fail: a shifted insertion threshold, a wrong rejection shift, the old generator. One mutation did not
  fail: the old `linear_rand` association happens to truncate to the same indices on the recorded stream. It is
  ported anyway, because rounding can differ elsewhere.

## 5. Measured, and what remains

Fresh census (`analysis/m0146/m0146-0214/census`): TPC-H 22/22, SF0.25 86/99, SF1 74/99, all unchanged. Q64
remains SHAPE-DIFF at both scales. At SF1 its first divergence moved from depth 6 to depth 5, and SF1
`parameterisation` 7 → 6 (inside the ±3 noise band). The sf025 sweep's only plan change is Q64, with values
identical. The fire set executed Q64 at SF1 on both engines with byte-identical results.

Why Q64 still diverges: `random_init_pool`'s tours depend only on the draws and on join validity, so they are now
PG's. From `sort_pool` on, the walk is steered by worth. goopg's Q64 costs differ from PG's (top plan
13866.65 vs 13874.27; other queries differ similarly), so the sorted pool, the selections and the survivors
diverge. **Next step:** attribute the first fitness divergence.

1. Add a `GOOPG_GEQO_TRACE` dump of the initial pool (tour, worth), mirroring `GEQO_DEBUG`'s `print_pool`.
2. Build the PG oracle with `GEQO_DEBUG` (the `tmp/pgdbg` recipe).
3. Compare the worths tour by tour. The first tour whose worth differs names the costing gap, which then routes
   like any other record.

## 6. Slice 2 (2026-10-16): the fitness gap is physical

Before instrumenting pool worths, the slice checked the cost inputs of Q64's leaves. At SF0.25, row estimates and
seq-scan costs are identical in both captures, but index scans differ: `store_returns_pkey` costs 4.38 vs 5.52, and
at SF1 4.74 vs 5.10 and `item_pkey` 7.41 vs 7.38. `pg_class` on a private clone of goopg's corpus against
`:65438 tpcds025` (`analysis/m0146/m0146-0214/physical/`) shows:

- heap `relpages` and `reltuples`: identical on every Q64 relation;
- `relallvisible`: goopg is fully all-visible after the owner VACUUM, while PG keeps non-visible tails
  (`store_returns` 1196/1232; `promotion`, `store` and `income_band` 0);
- index pages: `store_returns_pkey` 277 vs 280, `store_sales_pkey` 2481 vs 2569.

**Control experiment.** PG 18.3 on a scratch cluster restored with `pg_dump --statistics` from the reference had
the reference's exact column and relation statistics, but freshly bulk-built indexes (198 and 1976 pages). It chose
a third Q64 plan (cost 14022.76; the reference is 13874.27 and goopg 13866.65). PG reads index pages, tree height
and heap block counts from the physical relations (`plancat.c` :475, :493, :1097); only `allvisfrac` comes from the
statistic (:1112-1169). Identical statistics are therefore not enough: GEQO amplifies the physical deltas into a
different tour, in PG itself.

**Consequence.** Q64 needs physical parity first: VM all-visible state and index page counts at plan-identical
precision. That is M0146-0216's scope (S9(5)(c)). After that, the tour-by-tour worth comparison of §5 becomes
meaningful; before it, every tour's worth differs for a known reason. M0146-0214 is gated
`blocked-on M0146-0216`.

## 7. Slice 3 (2026-10-19): tour-by-tour worths against PG

On the reloaded corpora (PG's statistics, index pages within ±2), both engines were traced over SF0.25 Q64.

- **PG oracle:** the `tmp/pgdbg` build on the exact replica `tmp/m21-refclone`, with a probe in geqo\_eval.c and
  geqo\_main.c. It logs every evaluated tour and its worth, the initial rels in gene order with their cheapest paths,
  and, for one chosen evaluation (`GEQO_TRACE_EVAL=<n>`), every join rel merge\_clump builds. The patch is
  `analysis/m0146/m0146-0214/slice3/trace/pg-geqo-probe.patch`.
- **goopg:** `GOOPG_GEQO_TRACE=1` (and `GOOPG_GEQO_TRACE_EVAL=<n>`) prints the same lines in the same format
  (geqo.go; registered as diagnostic-only in flagProvenanceExempt). The comparison scripts are
  `analysis/m0146/m0146-0214/slice3/trace/joincmp.{sh,py}`.

Findings:

- **The walk is PG's.** Both engines evaluate 500 tours, and the first 252 are the same tours in the same order. The
  initial rels' gene order, rows and cheapest-path costs are identical except gene 3.
- **No tour's worth matched PG before this slice.** The commonest difference was +0.1. Gene 3 is `cs_ui`, an inlined
  CTE over a grouped query with a HAVING, which reaches the search as CTEScan → Project → Filter → Aggregate.
  - PG prices it with cost\_subqueryscan over the Agg path, its HAVING priced inside cost\_agg ("If there are quals
    (HAVING quals), account for their cost", costsize.c:2845): 13815.025975 + 7 × cpu\_tuple\_cost.
  - goopg priced the Aggregate without the HAVING, then charged a cpu\_tuple\_cost per row at the Filter, the Project
    and the CTEScan as well as the leaf's own: 0.1 high at SF0.25.
- **Fixed:**
  - DeriveLegacyDisplayCost prices a Filter over an Aggregate as cost\_agg's HAVING: the quals' startup plus their
    per-tuple cost over the groups the Agg emits, and no cpu\_tuple\_cost.
  - subplanLeafSubpathCost peels an inlined CTEScan, the subquery PG plans in its place.
  - projectionCapableNode treats a Filter as capable when its child is, since PG keeps those quals on the node.
  - Pins: TestHavingIsPricedInsideTheAggregate and TestInlinedCTELeafPricesCostSubqueryscanOnce, mutation-checked.
    TestExplainLimitClampsToInputRows now pins PG's own Limit lines (43.90..46.90; 44.58..45.95), where it had pinned
    goopg's HAVING-less 46.12.
- **After the fix,** 45 of the first 252 tours match PG to the last printed digit. The remaining differences route
  as follows:
  - +0.0103 class (tour 5) and the tiny ones: a nested loop probing customer\_demographics\_pkey, which has 5269
    pages in goopg against PG's 5268. store\_sales\_pkey is 2567 against 2569. This is the physical residual of
    M0146-0216.
  - The large classes (tour 2, −322): the first large gap is a row estimate. Joining customer\_demographics cd2
    under `cd1.cd_marital_status <> cd2.cd_marital_status` gives 1536640 rows (1 − 1/5) against PG's 1536621.
    PG's neqjoinsel is 1 − eqjoinsel, and eqjoinsel\_inner matches the two columns' MCV lists (selfuncs.c). goopg
    ports only its no-MCV branch (ledger row 2026-08-04, M0127-P5.6-a). The imported statistics carry the MCV
    lists, so this is now the dominant worth gap; it is filed as M0146-0230.
- Q64 still diverges at depth 5 (join-order) at SF0.25. Its total moved 13889.79 → 13889.69; PG's is 13874.27.
- Gates: units, TestPort\_RegressSuite, tpch-spotcheck, the acceptance arm (values identical), and the sf025 sweep
  (PASS=99, 14 plans changed text) all pass. tpcds-fireset passes: SF0.25 97/99 and SF1 94/99 unchanged, and all 29
  fired results are byte-identical.

## 8. Slice 4 (2026-10-19): merge clauses by equivalence class, and the cheapest-startup outer for hash joins

Tour-by-tour against PG again, after M0146-0230 put the join estimates on eqjoinsel's MCV branch. The tooling is §7's;
the PG probe now also prints each join's input paths and MergePath's sort and materialize flags. Evidence:
`analysis/m0146/m0146-0214/slice4/`.

- **Merge clauses match outer pathkeys by class.** In tour 2 the first gap was a merge join with `item` at equal
  rows. PG's inner is the presorted index scan on item\_pkey: 18896.82..106969.50, no sort.
  - goopg never built that candidate, so it sorted a seq scan instead (20675.72..106647.34). Its
    findMergeClausesForOuterPathkeys matched the outer's pathkeys to the clauses by expression.
  - The outer nested loop is ordered on `cs_ui.cs_item_sk`, while the clause reads `ss_item_sk = i_item_sk`. PG
    compares EquivalenceClasses (`pk_eclass` against the clause's EC), and both columns are members of one class that
    the outer rel enforces.
  - Fix: after the exact expression match, indexOfEquivalentOuterKey asks the outer path's rel (`equivalentWithin`).
    Both callers change: the serial arm and the partial twin.
  - Tour 2 now matches PG at every join, to within the one-page index residual (6 × 10⁻⁶).
- **hash\_inner\_and\_outer tries the cheapest-startup outer.** In tour 12, PG's hash join (19412.99..109340.49) is
  over the outer rel's cheapest-STARTUP path, a fast-start nested loop.
  - PG tries `cheapest_startup_outer × cheapest_total_inner` first, then the cheapest-total pairs (joinpath.c). When
    the totals tie within add\_path's fuzz, the fast-start join survives.
  - goopg built the total-total pair only. addHashJoinPath now files PG's candidate set in PG's order:
    - a unique-ified outer gets its cheapest-total only;
    - a unique-ified inner gets the cheapest-total outer, then the cheapest-startup outer;
    - otherwise the cheapest-startup outer goes first, then the cheapest-total pair unless it is the same.
  - Tour 12 now matches at every join.
- **Pins:** TestFindMergeClausesForOuterPathkeys\_MatchesThroughTheOuterClass and
  TestAddHashJoinPath\_TriesTheCheapestStartupOuter, both mutation-checked.
- **Result:**
  - The first 252 tours: 155 within 10⁻⁶ relative of PG's worth, 26 beyond 10⁻⁴.
  - SF1 Q64 MATCHes PG: SF1 PLAN-PARITY 94 → 95. CATEGORIES-EXCL-MATCH SF1 join-order 4 → 3, scan-type 4 → 3,
    qual-placement 2 → 1.
  - SF0.25 Q64 still diverges at depth 5.
- **Remaining classes in the SF0.25 tours:**
  - Gather Merge over a partial path on an intermediate GEQO joinrel. In tour 14's 397M-row cartesian join, PG's
    cheapest path is a Gather Merge (1003.38..6.42 × 10⁹) where goopg has a serial nested loop (1.98 × 10¹⁰). It
    comes from merge\_clump's generate\_useful\_gather\_paths. These tours have worths around 10¹⁰, but the pool's
    ordering among them still steers selection.
  - customer\_demographics\_pkey's one extra index page (5269 against PG's 5268), amplified by parameterised probes
    (tour 21: +0.31 over 540607 loops). That is M0146-0216.
- **Gates:**
  - units, TestPort\_RegressSuite, tpch-spotcheck and the acceptance arm (values identical) pass.
  - The sf025 sweep is PASS=99, with one plan changed.
  - tpcds-fireset passes: Q64 fired at both scales and its results are byte-identical.
