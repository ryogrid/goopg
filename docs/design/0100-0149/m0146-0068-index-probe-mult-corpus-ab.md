# M0146-0068 — `indexProbeCostMultiplier` corpus A/B (recon)

Status: done 2026-10-07 (recon; no code change). Evidence:
`analysis/m0146/m0146-0068/`.

## Question

`indexProbeCostMultiplier = 2` (optimizer/cost_funcs.go) doubles a
parameterised index probe's random-page cost. It was calibrated because
goopg's index probes measured about 2x PG's runtime. Retiring it has been
owner-parked since 2026-09-24: point witnesses showed mult=1 electing
PG-shaped nested loops that ran 2–3x slower.

The owner commissioned a corpus measurement (OWNER DECISIONS 2026-10-06):

- If no witness regresses more than 10% in wall-clock at mult=1 while
  shapes move toward PG, retirement is pre-authorised.
- Otherwise, write a narrowed-window proposal.

## Method

- One engine (`a914aa2a7136`, HEAD `b7113b464`) in both arms, through the
  fire-set gate's env-file A/B:
  `BASELINE_REV=worktree CANDIDATE_ENV_FILE=<export GOOPG_INDEX_PROBE_MULT=1>`.
  The candidate captures record `GOOPG_INDEX_PROBE_MULT=1` in their
  planner-flags header.
- Corpora: TPC-DS SF0.25, TPC-DS SF1, and TPC-H (through the acceptance arm).
- Each arm runs its fired queries serially on a fresh private clone.
- **Timing source.** Per-query times are result-file mtime deltas; TPC-H
  uses the arm's own `elapsed=`. Single runs proved noisy: queries whose
  plan skeleton did not change at all still moved up to 3x. So:
  - every fire was split into skeleton-changed vs skeleton-identical
    (`skeleton-split.txt`);
  - every skeleton-changed mover beyond ±10% was re-measured three times
    per arm, alternating arms, on fresh clones (`repeated-runs.txt`).

## Results

### Plan shapes (`*-categories.txt`)

| corpus | fires | match mult=2 → 1 | category moves (EXCL-MATCH) |
|---|---|---|---|
| TPC-DS SF0.25 | 83/99 | 42 → **52** | join-order 49→38, scan-type 29→18, join-method 25→21, parameterisation 26→23, parallelism 29→27 |
| TPC-DS SF1 | 80/99 | 33 → **34** | scan-type 36→28, parameterisation 32→31; join-method 21→22, parallelism 42→44, sort 28→30, aggregation 15→16 |
| TPC-H | 16/22 | 12 → **14** | join-order 8→6, scan-type 9→6; join-method 3→4, parallelism 2→3 |

Shapes move toward PG on every corpus, strongly at SF0.25. At SF1 the
scan-type gain is partly offset by parallelism and sort losses.

### Wall-clock

- **Totals over the fired queries** (single run):
  - SF0.25: 334.9s → 335.7s (×1.00).
  - SF1: 1374s → 1402s (×1.02).
  - TPC-H: 27.2s → 25.5s (×0.94).
- **Single-run noise.** Among skeleton-identical fires, as many moved
  beyond ±10% as among skeleton-changed ones, in both directions. Q89 at
  SF1 had an identical plan and ran 4.1s vs 1.3s.
- **Values.** TPC-H value digests are identical in both arms; every TPC-DS
  fire PASSes in both.

Confirmed after three repetitions (median):

| witness | mult=2 | mult=1 | ratio | what mult=1 changes | PG's plan |
|---|---|---|---|---|---|
| SF0.25 Q17 | 0.63s | 0.99s | ×1.56 | the catalog_sales index probe moves out of the Gather Merge into a serial NL above it | **the same as mult=1** |
| SF1 Q54 | 2.38s | 5.29s | ×2.22 | the Gather disappears: a serial store_sales Seq Scan with Materialize | keeps a Gather (different, parallel item-driven NL) |
| SF1 Q71 | 1.80s | 2.28s | ×1.27 | Hash Join + Seq Scan(item) becomes NL + Index Scan(item_pkey) | **the same as mult=1** |
| SF1 Q18 | 5.10s | 3.39s | ×0.66 (noisy) | NL probe order over customer / address / demographics | — |

Single-run "regressions" that did not hold up: SF1 Q32 (4.4x → ×0.94),
Q68 (2.6x → ×1.05), and SF0.25 Q81 (×1.10, within the noise band).
TPC-H Q17 (×1.15) is a single run inside the noise band. Q14, the
standing counter-example, does not move at either scale (38s / 218s in
both arms): its probe choice is no longer driven by the multiplier here.

## Verdict

The pre-authorised retirement condition fails: three witnesses regress more
than 10% at mult=1. They cluster in two classes:

1. **The PG-shaped parameterised probe runs slower in goopg (Q17, Q71).**
   Under mult=1 goopg elects exactly PG's plan, and executes it 1.3–1.6x
   slower than the parallel/hash shape mult=2 picks. This is the
   executor-side probe cost the multiplier was calibrated for. The fix
   belongs in the executor's per-probe cost (index descent / heap fetch
   per loop). Measured earlier: 0.067 ms vs PG 0.041 ms per probe
   (M0145-0008af).
2. **Parallelism is lost when probes get cheaper (Q54).** mult=1 makes a
   serial NL plan cheaper than every parallel plan. PG keeps a Gather over
   a different, item-driven NL that goopg cannot build yet: a partial
   outer driving a parameterised Append, which is the open M0146-0049e. The
   SF1 parallelism category's +2 is this class.

## Narrowed-window proposal (for the owner)

- **Option A — retire the multiplier (mult=1), file the substrate.**
  - Plan parity gains match +10 (SF0.25), +1 (SF1), +2 (TPC-H); runtime
    totals are flat.
  - Cost: Q17 +0.36s, Q71 +0.48s and Q54 +2.9s at their scales, until
    (1) goopg's per-probe executor cost reaches PG's (new impl task) and
    (2) M0146-0049e lands.
  - Recommended. The regressions are executor-substrate debts the PG
    shape exposes, not wrong elections. Keeping the multiplier hides them
    while costing 13 plan matches.
- **Option B — keep 2 only for serial-side probes.** Apply the multiplier
  only to a parameterised index probe whose nested loop is not under a
  Gather (the Q17/Q71 position), and use 1 elsewhere.
  - This recovers Q17/Q71's runtime but forfeits their PG shape.
  - It does not address Q54, which needs 0049e either way.
  - It is a goopg-only heuristic in cost_index, so it would need its own
    corpus A/B before landing.
- **Option C — keep the park.** Leaves the 13 matches on the table.
  M0145-0008ag, M0146-0009p and M0146-0060 stay blocked.

## Gaps

- The timing source is mtime deltas, not server-side timing. The repeated
  runs bound the noise but do not remove cache-state effects between
  fresh clones.
- The SF1 and SF0.25 arms ran once each over the full fire set. Only
  skeleton-changed movers beyond ±10% were repeated, so small (<10%)
  systematic shifts across the corpus are not resolved.
