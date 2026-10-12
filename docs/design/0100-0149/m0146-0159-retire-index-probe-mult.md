# M0146-0159 — retire `indexProbeCostMultiplier` (Option A)

Status: in progress 2026-10-13 — code staged and gated, NOT committed: blocked by M0146-0169
(landed) and M0146-0170 (open), see "Blockers".
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

Raw artefacts: `analysis/m0146/m0146-0159/`.

### Plan shapes

Fire-set gate, HEAD `bb0a8f154` (x2) vs the staged retirement; captures
from private clones (`categories-raw.txt`).

| corpus | MATCH | CATEGORIES-EXCL-MATCH (x2 → retired) |
|---|---|---|
| TPC-DS SF0.25 | 56 → **70** | join-order 37→22, join-method 16→12, scan-type 22→9, parameterisation 17→14, parallelism 17→14, sort 15→14, qual-placement 4→3 |
| TPC-DS SF1 | 42 → **45** | join-order 47→44, scan-type 32→22, parameterisation 27→26, qual-placement 10→9; join-method 18→19, parallelism 33→37, sort 20→24, aggregation 8→9 |
| TPC-H | 11 → **13** | join-order 9→7, scan-type 9→5; parameterisation 1→2, parallelism 4→5, qual-placement 1→2 |

M0146-0068 predicted about +10, +1 and +2; today's HEAD gives +14, +3 and
+2. The SF1 parallelism and sort losses recur, as 0068 saw.

### Wall-clock

- **Single run, all fires** (`single-run-times.txt`):
  - SF0.25: 164.1s → 161.9s (×0.99).
  - SF1: 1207.5s → 1144.6s (×0.95).
  - TPC-H: 27.5s → 27.8s.
- **Repeats.** Every mover beyond ±10%, plus 0068's three confirmed
  regressions, was re-run three times per arm, alternating arms, on fresh
  clones (`repeated-runs.txt`):
  - SF0.25 Q61 and Q21 (single-run ×6) are ×0.97 and ×0.99. Q17 is ×1.13,
    down from 0068's ×1.56 now that M0146-0088's leaf binary search has
    landed.
  - SF1 Q5, Q6, Q7, Q16, Q35, Q47, Q54 and Q71 are all within ×0.93–1.03.
    Q54 (0068: ×2.22) and Q71 (×1.27) no longer regress. Q67 is ×0.84.
  - TPC-H Q9 is ×0.95 and Q11 is within noise.
- **No confirmed wall-clock regression** in any corpus.

### Values and gates

- **Values.** The TPC-H arm values are identical in both arms, and every
  TPC-DS fire PASSes its status in both arms.
- **Gates.**
  - Units: PASS once M0146-0169 landed.
  - tpch-spotcheck and the acceptance arm: PASS.
  - Fire-set gate: PASS.
  - **sf025 sweep: CKMISMATCH on Q32** (goopg returns NULL, PG returns
    2444.35). See Blockers.

## Blockers

Retiring the x2 changes elections, and twice that exposed a pre-existing
wrong-results bug. Neither bug is caused by the cost change; each is
reproducible on HEAD with the shape forced.

1. **M0146-0169 (landed).** A MERGE UPDATE filed no index entry for the new
   row version. `TestDMLTriggersFireInPGOrder`'s final ordered read became an
   Index Scan and lost a row. Fixed in `7958cf698` before this task's units
   gate could pass.
2. **M0146-0170 (open).** A correlated SubPlan whose body is a parameterised
   nested loop returns wrong rows when the SubPlan is evaluated in a qual of a
   join whose other side supplies the correlation value.
   - Unconfirmed hypothesis: the nested loop's inner probe parameter
     (`OuterColumnRef{Level: 1}`, `outerParamKey`) is resolved against the
     wrong `ctx.OuterRows` entry. A related symptom: EXPLAIN prints the inner
     key as `cs.sold` even when the subquery aliases the table `x`.
   - TPC-DS Q32's SubPlan becomes that shape under the retired costs, so Q32
     returns NULL.
   - Repro on HEAD with nested loops forced:
     `analysis/m0146/m0146-0159/q32-subplan-param-repro.sql` gives NULL;
     PG gives 26457.
   - Narrowing: the same subquery in a target list, or in a qual over a
     single relation, is correct.

The staged code stays uncommitted until M0146-0170 lands and the sf025 sweep
is clean (AGENT.md G5/G6).
