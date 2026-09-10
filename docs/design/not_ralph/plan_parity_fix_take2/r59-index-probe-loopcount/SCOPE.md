# R59 SCOPE — index-probe loop-count pro-rating: the missing index-side arm (2026-09-11)

Follows R58 LANDED (`r58-worker-count-sizing/SCOPE.md`): the
4-vs-2 closed as a shape consequence; the residual re-owned to
index-probe pricing (lineitem probe 9.20 vs PG 1.20, orders
10.13 vs 1.54 — 11.5×/8.8× run-cost) with the C-20d tension
quoted. This round decomposes that gap term-by-term and
authorizes ONE bounded fidelity fix. Read-only probes so far
(oracle source + goopg source + R56 captures); the
implementation round runs the gates.

## 1. Probe F verdicts

### (i) The gap decomposes: index-side ~8.0 of goopg's ~8.8 run

Lineitem probe (`reqouter={2}`, rows=2, startup=0.38,
total=9.20 → run 8.82). The decomposition is exact by
subtraction given `numIndexPages = 1` (in-round pin
recomputes through `costIndexScanCore`; the §3
reproduction gate holds it to the cent):

- Cost selectivity is the probed-prefix only
  (`parameterizedIndexSelectivity(tbl, idx, probed, …)`,
  `pathparamindex.go:353` — PG's own indexSelectivity-vs-ppi_rows
  split, `:349-352` comment): l_orderkey=X → ~6.67e-7
  (1/ndistinct, ndistinct=1.5M). tuplesFetched ≈ 4.
  loopCount = 1.5M (`loopCountFor`, smallest outer rows).
- Heap-side (loop-count arm, `costindex.go:235-258`): by
  subtraction 8.82 − 8.02 (index-run: 8.0 pages + 4×0.005
  cpuIndex) = 0.80 for heap+CPU; CPU ≈ 0.05 over 4 fetched
  tuples → heap ≈ 0.75. Mechanism: Mackert-Lohman over
  4×1.5M fetches saturates at the table size (cap-T
  branch), so heap = T×4.0×2.0/1.5M with T≈140k goopg
  pages (pin verifies the geometry; an earlier draft's
  T≈400k/2.1 did not sum to 9.20 and is withdrawn).
- **Index-side (`btreeIndexAMCostPages`, `costindex.go:339-348`):
  `numIndexPages = ceil(4 × indexPages/6M) = 1` →
  `total = 1 × 4.0 × 2.0 = 8.0`, charged IN FULL every probe.
  `loopCount` is never referenced in the function (lines
  321-376 read clean — the field sits unused in `in`).**
- CPU: tuples×(0.01+0.0025×quals) ≈ 0.05 both sides.

PG's side for the identical probe: `genericcostestimate`
(`selfuncs.c:7157-7204`) runs Mackert-Lohman over
`numIndexPages × num_scans` (num_scans = loop_count = 1.5M
here — "we take N = T = index size, as if there were one
tuple per page") and PRO-RATES: `indexTotalCost =
pages_fetched × random / num_outer_scans`. The whole index
caches across 1.5M probes → ≈ indexPages×4.0/1.5M ≈ 0.05
unscaled (≈0.1 at mult 2.0 — convention stated once here
and used throughout §3). goopg charges 8.0 where PG
charges ~0.05: **~150× on the index-side term, ~8.0
absolute — the entire gap and then some** (8.0 of 8.82
run; the heap-side 0.75-vs-0.6 is nearly closed — §1.ii).

Descent is EXONERATED (goopg startup 0.38 vs PG 0.43 — both
charge the per-scan descent un-pro-rated; PG's
`indexStartupCost` likewise survives division).
0.38 = 3×50×0.0025 pins treeHeight 2 on both probes. The
0.05 startup gap (PG 0.43 − goopg 0.38) is exactly the
omitted P2-09b `log2(N)` descent term (log2(6M)×0.0025 ≈
0.056) — named so nobody re-opens it.

Orders probe (`reqouter={3}`, rows=16, startup=0.38,
total=10.13 → run 9.75), same subtraction: sel
o_custkey=X ≈ 6.67e-6 (1/150k) → numIndexTuples 10,
numIndexPages 1 → index-run 8.05 (8.0 + 10×0.005);
heap+CPU = 1.70 → heap ≈ 1.6, CPU ≈ 0.1. Post-fix
index-side: ML caps at the index size over 150k scans
(§2 arm) → ≈0.17 unscaled, ≈0.35 at mult 2.0 →
predicted total ≈ 2.4; the residual vs PG 1.54 is the
knob (2.0, deliberate) + width-inflated `relPages`
(ledgered) — by design, not a second gap.

`loopCountFor` MATCHES `get_loop_count`: smallest outer
BASE-rel rows — PG's `simple_rel_array[relid]->rows`
(`indxpath.c:2328-2369`, semijoin adjustment aside; no
semijoins here), goopg's smallest level-1 rows
(`pathparamindex.go:524-539`). Orders loopCount =
customer base rows = 150000 (no local filter; trace
prebuilt `relids={3} rows=150000`), NOT the 11990
join-output rows. Heap-side loopCount inputs are
exonerated on both probes; the bug is index-side-only.

### (ii) What the fix is NOT (ledgered contributors, explicitly out)

- The 2.0 multiplier stays: it scales the (now pro-rated)
  index cost exactly as it scales the heap bounds today.
  Knob untouched — R58 §2's quota.
- Heap-side gap, re-derived from the corrected inputs (an
  earlier draft's 2.1-vs-1.1 did not sum to the traced
  totals and is withdrawn): lineitem 0.75 vs PG ≈0.6
  (run 0.77 − index ≈0.05 − CPU) — nearly closed, was
  entirely the missing arm; orders 1.6 vs PG ≈0.9 ≈ the
  2.0 knob alone (1.6/2 = 0.8, deliberate) with
  width-inflated `relPages` (HammerDB NUMERIC keys price
  32 bytes — the known width-model gap, ledgered as its own
  round) second. NOT authorized here; the fix must not touch
  `estimateIndexGeometry`, `relPages`, or any width.
- Probe rows 2 vs 5 (shipdate placement): cheaper direction,
  untouched.

### (iii) Endgame honesty: pricing alone does NOT flip Q7

Even at the predicted ~1.3/probe, the serial NL chain
through `{1,2}` (≈43k + 1.5M×~0.95-run ≈ 1.5M —
`nestloopCost` multiplies inner RUN cost,
`cost_funcs.go:723-747`, rescan arm `:735-742`) still loses
to the `{1,2}` partial-hash rival (516k, R58 §1.iii) — the
chain dies at the lineitem extension however priced above
~0.3/probe ((516k−43k)/1.5M = 0.315). The lineitem-last
order is no escape either (analysis of an ungenerated
order — no `reqouter={2,3,4,5}` probe in the trace): at
that parameterisation the probe's loopCount is the min
level-1 rows in req = 25 (nations), so neither engine
pro-rates deeply there — and a serial NL can never go
under a Gather anyway (`gatherSubpathIsRunnable` refuses
`ParallelWorkers <= 0`, `gatherpaths.go:280-284`;
`generateGatherPaths` wraps `PartialPathlist` only). Q7 plan
parity additionally needs the partial-NL producer (R58 §1.iii
ledgered surface) AND ultimately the executor NL-probe work
(R58 §2). This round narrows the price gap and fixes a real
fidelity bug; it does not promise the shape.

## 2. The authorised cut (one site, one arm)

In `btreeIndexAMCostPages` (`costindex.go:321-376`), add
PG's `num_scans > 1` arm from `genericcostestimate`
(`selfuncs.c:7180-7204`): when `num_scans =
numSAScans × loopCount > 1`, run `indexPagesFetched` over
`numIndexPages × num_scans` (N = T = index size, per the
oracle comment) and pro-rate the index-page cost by
`num_outer_scans = loopCount` — with `indexProbeCostMultiplier`
applied to the pro-rated result, exactly as the heap bounds
apply it today. Single site: every caller of the AM-cost
pair inherits it; the serial (`num_scans ≤ 1`) arm is
bit-identical (the `else` branch is today's arithmetic —
gated on `num_scans`, not `loopCount`, since numSA > 1
can repeat scans at loopCount 1).
No new constant, no knob change, no width change. The SAOP
descent charge (`numSA × descent`) is startup/rerun
per-scan on both sides (PG startup 0.43 proves it) —
unchanged, but the implementation round re-reads
`btcostestimate` (`selfuncs.c:7762-7782`) to confirm before
landing.

Out of scope (hard, ledgered): partial-NL producer (R60
candidate — needs its own scope round first); width model /
`relPages` / `estimateIndexGeometry`; the 2.0 knob;
correlation stats (ANALYZE side); probe rows 2-vs-5;
AGG_MIXED; R55 SCOPE §3 tie-break; F3; Q8 gap.

## 3. Prediction (falsifiable; flip NOT promised)

At the traced inputs (lineitem probe rows=2, loopCount=1.5M;
orders probe rows=16, loopCount=150000):

- Lineitem probe total: 9.20 → **[1.2, 2.0]** (≈0.38 descent
  + ≈0.05 pro-rated index-side unscaled, ≈0.1 at mult 2.0
  + ≈0.75 heap unchanged + CPU ≈0.05; central ≈1.3; low
  1.2 sits at the component floor 0.38+0.75+0.05 = 1.18).
  Above 2.0 = the arm under-delivers (FAIL); below 1.2 =
  overshoot past the knob-implied floor (re-audit, not
  celebration).
- Orders probe total: 10.13 → **[2.0, 3.0]** (same arm,
  shallower pro-rating over 150k scans: ≈0.38 + ≈0.35 +
  ≈1.6 heap unchanged + CPU ≈0.1; central ≈2.4; low 2.0
  sits at the component floor 0.38+1.6+0.1 = 2.08;
  residual vs PG 1.54 is knob+width by design — in-round
  pin recomputes the exact band from traced inputs BEFORE
  the cut lands; a band the pin cannot reproduce FAILS the
  round before any code changes).
- `{1,2}` NL extension drops 13.3M → ~1.5M, still dominated
  by the `{1,2}` partial hash (516k): **Q7 winner
  immobile, Q7 top plan byte-identical** (R56-pattern
  outcome). A Q7 flip triggers re-audit per §1.iii, not
  celebration.
- Reproduction gate (first test, before the cut): a unit
  harness reconstructing the traced inputs must reproduce
  9.20 to the cent through `costIndexScanCore` — else the
  inputs are mis-reconstructed and the round stops. Pin
  checklist (assumed, must be traced): `relTuples` 6M/1.5M,
  T≈140k (140625×8/1.5M = 0.75 ✓), `indexPages` (margin
  enormous — any P ≤ 1.5M pages keeps numIndexPages 1),
  ndistinct 1.5M/150k, `treeHeight` 2 both probes
  (0.375→0.38 ✓), `numQualOps` 1 lineitem, cap-T branch +
  `correlation` 0, `effectiveCacheSize`/pro-rated share.

## 4. Must-holds

- Fix-seed §5 + structural Q19 MATCH + Q5-split-wins
  non-vacuous (R56 §4 carried forward).
- TPC-H digest MATCH (Q7 top byte-identical ⇒ digest moves
  only if another query's probes flip it — any unadjudicated
  move FAILS); TPC-H values 8/8; DS SF0.5 sweep PASS=95
  all-zero verdicts (plan-channel moves toward-PG only,
  adjudicated like R56 §4); `pg-plan-parity-diff` unparsed=0.
- Sibling audit in-loop: the new arm vs `genericcostestimate`
  line-by-line (`:7180-7204`), and every caller of
  `btreeIndexAMCost`/`btreeIndexAMCostPages` named.
- The knob stays 2.0; widths, geometry, correlation untouched.

## 5. Status

R59 = this audit (probe F, §1) + §2 implementation in the
next task + review. Nothing here authorises a knob change,
a width change, partial-NL, or §3 calibration.
