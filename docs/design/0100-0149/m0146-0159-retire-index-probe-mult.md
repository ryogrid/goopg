# M0146-0159 — retire `indexProbeCostMultiplier` (Option A)

Status: landed 2026-10-13 (`de45f8a6f`), after its two blockers M0146-0169 (`7958cf698`) and
M0146-0170 (`566d9721c`).
Parent: M0146-0068 (owner decision 2026-10-12, Option A).

## Problem

goopg priced every random-page term of an index access at
`random_page_cost × 2`: the parameterised probe (`indexProbeCost`) and the
scan model (`costIndexScanCore`, six sites in `costindex.go`). The x2 was
calibrated on 2026-09-05 (C-20d) because goopg's probes ran about 2x PG's.

M0146-0068's corpus A/B measured the multiplier electing non-PG shapes. At
mult=1 the plan match rose by +10 at TPC-DS SF0.25, +1 at SF1 and +2 at
TPC-H, with flat runtime totals. The owner chose Option A on 2026-10-12:
retire it. The executor-side probe cost it hid is M0146-0088's, and the
parallelism loss it exposes (Q54) is M0146-0049e's.

## PG behaviour

PG charges an index page fetch at `spc_random_page_cost` with no further
factor:

- `cost_index` (`./postgres/src/backend/optimizer/path/costsize.c`):
  - the max/min I/O bounds at :700 and :722 for `loop_count > 1`;
  - the bounds at :741 and :751 for a single scan.
- `genericcostestimate`'s index pages
  (`./postgres/src/backend/utils/adt/selfuncs.c:7201`, `:7210`).

## Change

- **`costindex.go`.** The six `* indexProbeCostMultiplier` factors are
  removed, so every random-page term is `random_page_cost`, as cited above.
- **`cost_funcs.go`.**
  - `indexProbeCost` is `2·random_page_cost + cpu_index_tuple_cost +
    cpu_tuple_cost + cpu_operator_cost`, with no factor.
  - The variable, its env resolver (`indexProbeMultFromEnv`), the calibrated
    constant and the test hook `SetIndexProbeCostMultiplier` are deleted.
- **Flag provenance.** `GOOPG_INDEX_PROBE_MULT` moves to
  `flagProvenanceRetired` ("M0146-0159"), and `scripts/planner-flags.env` is
  regenerated.
  - Older artefacts that carry `unset(2)` are the multiplied arm.
  - `1` reproduces this one.
- **Equivalence.** Multiplying by 1.0 is exact in IEEE arithmetic, so the
  retired code computes bit-identical costs to the old binary run with
  `GOOPG_INDEX_PROBE_MULT=1`.
  - The fire-set gate's HEAD-vs-staged A/B is therefore the same
    measurement as M0146-0068's mult=2-vs-1 env A/B, taken at today's HEAD.
  - The task's "reproduce the 0068 table first" step is that capture.
- **Tests.**
  - The formula pins (`TestIndexProbeCost`, `TestCostIndexScan*`) now use
    PG's arithmetic.
  - The knob test becomes `TestCostIndexScanRandomPagesAtRandomPageCost`:
    raising `random_page_cost` moves exactly the random-page terms in both
    models.
  - The R59 probe pins are re-derived without the x2: lineitem
    1.2488 → 0.8622, with the hand decomposition in the test; orders
    2.2051 → 1.4094.
  - The flag detector's helper-read witness becomes
    `GOOPG_INCREMENTAL_SORT`.
  - Eight executor tests no longer need to pin mult=1 to get PG's
    index-vs-bitmap election. The param-Append identity test drops its
    two-arm loop.

- **Tests whose premise was the old x2 election.** Each was checked against
  PG 18.3 on the same fixture and either follows PG's plan or asks for its
  subject explicitly, the way PG would need it:
  - `TestExplainIndentDeepNesting` and its ANALYZE twin: goopg now elects
    PG's exact plan (Sort / GroupAggregate / Index Scan). The indent pin moves
    to that 3-level shape.
  - `TestRangeScanExclusive*`: PG elects a Bitmap Heap Scan, so the IOS
    subject is planned with seq and bitmap scans off.
  - `TestGatherMergeOverParallelIndexScanIdentity` and
    `TestParallelIndexScanIdentityWithRealGather`: PG's Index Scan needs seq
    and bitmap scans off (verified).
  - `TestNLIBitmapProbeCompositeKeys`: PG probes with an Index Only Scan at
    default settings and takes the bitmap probe with `enable_indexscan` off.
  - `TestParallelNLIBitmapInnerIdentity`: PG probes with an Index Scan; the
    bitmap inner needs `enable_indexscan` off.
  - The initdb opclass-restart test: PG seq-scans the fixture, so its index
    probe plans with `enable_seqscan` off.

## Verification

Raw artefacts: `analysis/m0146/m0146-0159/`. The `*-final*` files are the
landed A/B; the unprefixed files are the first run, taken before M0146-0170.

### Plan shapes

Fire-set gate: HEAD `566d9721c` (x2) vs the retirement, with captures from
private clones (`categories-final.txt`).

| corpus | MATCH | CATEGORIES-EXCL-MATCH (x2 → retired) |
|---|---|---|
| TPC-DS SF0.25 | 56 → **68** | join-order 37→24, join-method 16→14, scan-type 22→11, parameterisation 17→15, parallelism 17→15, sort 15→14, qual-placement 4→3 |
| TPC-DS SF1 | 42 → **45** | join-order 47→44, scan-type 32→24, parameterisation 27→26, qual-placement 10→9; join-method 18→21, parallelism 33→39, sort 20→24, aggregation 8→9 |
| TPC-H | 11 → **13** | join-order 9→6, scan-type 9→5; parameterisation 1→2, qual-placement 1→2 |

- **Against M0146-0068's prediction.** 0068 predicted about +10, +1 and +2.
  The first run, before M0146-0170, measured +14 at SF0.25.
  - Two of those matches came from SubPlans that the buggy rebase had
    re-bound.
  - They now keep the stack path, which is the correct value path.
- **SF1 losses.** The parallelism and sort losses recur, as 0068 saw.
  - The parallelism class (Q54's item-driven partial NL) is M0146-0049e's.
  - The sort class is recorded in the deferral ledger.

### Wall-clock

- **Single run, all fires** (`single-run-times-final.txt`):
  - SF0.25: 168.2s → 161.7s (×0.96).
  - SF1: 1181.0s → 1188.6s (×1.01).
  - TPC-H: flat.
- **Repeats.** Every single-run mover beyond ±10% was re-run three times per
  arm, alternating arms, on fresh clones, in two rounds
  (`repeated-runs.txt`; the second round's raw output is in
  `tmp/m0159/rep2`).
  - **SF1: all noise.** Q5, Q6, Q7, Q10, Q16, Q35, Q47, Q54, Q56, Q71, Q75,
    Q83, Q90 (single-run ×22.9), Q94 and Q96 are within ×0.93–1.04. Q67 is
    ×0.84.
  - **SF0.25: three confirmed slowdowns**, all of which now match PG's plan:
    - Q17 0.80s → 1.01s (×1.26);
    - Q25 0.78s → 0.98s (×1.26);
    - Q83 2.01s → 2.41s (×1.20).

    These are the executor-side probe cost that Option A accepted, filed as
    M0146-0171. Q21, Q46, Q58 and Q61 are noise.
  - **TPC-H:** Q9 is ×0.95 and Q11 is noise.
  - 0068's confirmed regressions no longer reproduce: Q54 ×0.98 and Q71
    ×1.01.

### Values and gates

TPC-H values are identical in both arms. On the committed tree:

- units and postmaster PASS;
- tpch-spotcheck and the acceptance arm PASS;
- the sf025 sweep PASS=99 (CKMISMATCH=0);
- the TPC-DS fire-set gate PASS, with no introduced timeout.

## Blockers (resolved)

Retiring the x2 changed elections, and twice that exposed a pre-existing
wrong-results bug. Both were fixed before this landed.

1. **M0146-0169.** A MERGE UPDATE filed no index entry for the new row
   version, and skipped the unique check. `TestDMLTriggersFireInPGOrder`'s
   ordered read became an Index Scan and lost a row.
2. **M0146-0170.** The sublink pull-up post-pass re-bound a lateral nested
   loop's nestloop param, inside a correlated SubPlan, to the host row.
   TPC-DS Q32 returned NULL under the new plan.
