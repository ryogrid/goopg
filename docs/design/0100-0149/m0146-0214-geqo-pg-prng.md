# M0146-0214 — GEQO runs on PG's pg_prng and pg_qsort

Status: slice 1 landed 2026-10-16. GEQO's random walk now reproduces PG 18.3's draw for draw. TPC-DS Q64, the
GEQO-RNG record, still diverges: from `sort_pool` on, the walk follows tour fitness (path cost). Slice 2 (§6)
attributed the remaining divergence to physical inputs, the visibility map and index page counts, which
M0146-0216 owns. The task is gated `blocked-on M0146-0216`.

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
