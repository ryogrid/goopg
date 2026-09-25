# R81 SCOPE: Q4 ordered-election re-measurement (post-R77 seed)

Lineage: R72 STEP-0 measured grouping electing hashed 4.26x
(1426.71 vs 6077.69) — with the PRE-R77 seed (~1k). R77 has
since repriced the semi to 490456.34, and NOBODY re-measured
the tournament since. Scratch probe of `costAgg` at the new
seed: hashed 490741.73 vs sorted 495392.71, ratio **1.0095 <
1.01** — inside the fuzz band. The election may have moved;
this Step-0 measures it. (R80 reserved per R79-verdict for the
ndistinct estimator program.)

## Step-0 — measure, no code changes

On private clone `:5556` (HEAD binary, canonical stats —
pre-measurement check: re-verify the 490456.34 seed and
default statistics targets before harvesting),
`GOOPG_PGSHAPED_DP_TRACE=1` in the SERVER environment
(`dpTrace` reads once at process start; DPPATH goes to server
stderr — harvest there, not psql output), EXPLAIN Q4,
harvest DPPATH lines (expect producers
`upper.groupagg.hashed/sort`, `upper.ordered.input/sort`):

1. Grouping survivors: which PathAgg candidates
   (hashed/sorted survivors; record any PathFinalizeAgg as a
   decline gate, not a contestant) survive `add_path` on
   GROUP_AGG, with verdicts + costs. The loop needs ≥2
   PathAgg + ≥1 translatable + 0 Finalize — soften "both
   survive" accordingly.
2. Ordered candidates: what the R47 slice-2 loop offers per
   survivor (no-sort vs Sort-over-X), with verdicts.
3. Final pick + the exact comparator arm that decided
   (`comparePathCostsFuzzily` startup vs M0129-S1 tie-break).

## Predictions (recorded before measuring)

- P1: hashed + sorted both survive grouping (ratio 1.0095).
- P2: ordered offers no-sort (sorted, translated pathkeys)
  and Sort-over-hashed.
- P3: election outcome UNKNOWN — that is the measurement.
  If hashed+Sort wins, the verdict path (startup arm vs
  tie-break vs pathkeys) names the precise next step.

## STOP / branch rules

- If sorted never reaches ordered (translation fails on live
  Q4): blocker = translation helper; round becomes the
  translation fix with this SCOPE as Step-0.
- If ordered picks hashed+Sort: implement NOTHING yet —
  record the exact verdict arm; scope the minimal fix as
  R81-slice-2 or a new round.
- If Q4 flips to GroupAggregate-no-sort: stretch achieved;
  proceed to full gates immediately.
- Mechanism-only landing without a named next step is NOT
  an acceptable outcome (program acceptance rule).

## Explicitly out

Probe-execution pricing (R77 landed), semi selectivity
(R78 BLOCKED, R79 verdict-b), widths (R70 CLOSED — still
blocked on DatumBytes/pushdown), qual-placement + stray
Filter (R48 landed), join-method costing, K100,
rescan-discount/Memoize refinements, TPC-DS.

## Gates for any implementation

Units, suites, TPC-H spotcheck, SF0.25 sweep, byte-guard A/B
both corpora + per-query census with ZERO EXTRA flips,
shape-delta alongside. `match` count is NOT a criterion.
