# R59 implementation — index-side loop-count pro-rating arm (REPORT)

SCOPE: `r59-index-probe-loopcount/SCOPE.md` (probe F). ONE authorised cut +
pin-then-cut gates per SCOPE §§2–4.

## 1. The cut (single site, `internal/optimizer/costindex.go`)

PG `genericcostestimate` num_scans>1 arm (selfuncs.c:7180-7204) in
`btreeIndexAMCostPages` only:

- numSA clamp hoisted above the arm (PG clamps in `btcostestimate`
  :7700-7760 BEFORE `genericcostestimate`, so descent + arm share it).
  Serial arm (`numScans <= 1`) bit-identical by construction.
- `numScans = clamped(numSAScans) × loopCount`, gated on numScans (not
  loopCount — numSA>1 repeats scans at loopCount 1). Mult applied to the
  pro-rated result, exactly as the heap bounds carry it.
- Knob (2.0), widths, geometry, correlation untouched. CPU term and the
  P2-09b remainder (numIndexTuples/rint, log2(N) descent term) untouched.

Sibling audit (SCOPE §4): every caller of `btreeIndexAMCost`/
`btreeIndexAMCostPages` named in review; the arm lives below both, so all
index-cost callers inherit it identically — no caller needed its own gate.

## 2. Reproduction gate + pins (`costindex_test.go`)

Unit harness through `costIndexScanCore` with verbatim traced literals
reproduced the pre-cut totals EXACTLY (9.2030 lineitem / 10.1302 orders)
before the cut landed. Post-cut:

- lineitem → **1.2487967346880979** ∈ [1.2, 2.0] (central ≈1.3) ✓
- orders → **2.2051380003749999** ∈ [2.0, 3.0] (central ≈2.4) ✓

## 3. R59 fallout fix — NLI enable-gating (`joinpathsnli.go`)

The probe repricing exposed a fidelity gap: search-level `addNLIPaths`
ignored `EnableNestLoop` (legacy `rewriteJoinsToNLI` gated, search
producer did not), so with nestloop disabled an NLI priced below the
merge join and stole a closed contest — failing
`TestOrderedInputArmFiresEndToEndAndRemovesTheSort`. PG oracle:
`initial_cost_nestloop` (costsize.c:3282-3284) counts `enable_nestloop`
on EVERY nestloop path; PG has no separate index-nestloop switch.
One-line fix mirroring `pathgen.go:163` (plain-NL arm):
`DisabledNodes: disabledNodesFor(!cp.enableNestLoop, o, in)`.
Full optimizer suite green after.

## 4. Gate results

- Units: `go test ./internal/optimizer/` green (no `-count=1`); `go vet`
  clean. Tree builds (sf05 sweep rebuilt `tmp/goopg-sf05-bin` from it).
- Values 8/8 md5 MATCH (q1,q3,q5,q7,q8,q9,q10,q19, `/tmp/pp2/r59/` vs
  `/tmp/pp2/r56/`): shapes flipped, answers byte-identical.
- Q19 top MATCH (vs R55; R56==R55 by R56 REPORT line 35 transitivity).
- Q5 top byte-identical to R55/R56 — `Finalize HashAggregate` split-wins,
  NON-VACUOUS (measured). Expected immobile: both agg arms build on the
  same pseed, input price cancels (FIX-SEED §2).
- Q84: tpch ERROR-control byte-identical ×2; ds05 run-stable (run1==run2)
  but MOVED (12405→9976): HH-seq→HD-pkey NLI + cd-probe 9.43→1.66. Every
  non-NLI leaf bit-identical; the R59 shape is STRUCTURALLY PG's own Q84
  shape (`bench/tpcds/plans-pg/Q84.txt`: same HD-pkey NLI nesting, same
  top cd-probe NL). FIX-SEED §5 "Q84 byte-identical" literally breached —
  adjudicated: priced mechanism, not drift (seed pinned, run-stable),
  direction toward-oracle. Carried to review.
- pp-diff (`scripts/pg-plan-parity-diff.py` vs `bench/tpch/plans-pg`):
  R59 `match=5 shapediff=15 unparsed=0 missingnode=2`; R56-plans-file
  re-run `match=1 shapediff=18 unparsed=1 (Q15a-VIEWBODY capture-format
  artefact) missingnode=2`. Q1 MATCH now; Q3/Q7/Q8/Q9 flips are the
  adjudicated NL moves (§5). unparsed=0 ✓.
- DS SF0.5 sweep (foreground, fresh build): PASS=94 (57 ck-verified,
  identical set) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4.
  Baseline R56: 95/0. Delta is Q72 alone: 4s PASS → 320s TIMEOUT.
  Solo fresh-server probe reproduces (317s) — NOT sweep-state/GC.
  Mechanism: R59 repriced the inventory-pkey probe (144 rows/probe ×
  28290 outer) into a deep NLI chain; goopg's executor has no Memoize
  on this path (ledgered executor NL-probe gap) and drowns in ~4M
  probes. PG's own Q84-analogue evidence: PG picks the same NLI-chain
  shape for Q72 (`pg_q72_analyze.txt`: nested NLs + Memoize, est rows=1
  vs actual hundreds — PG's estimates are no better here). Planner
  toward-oracle; executor can't run the shape. Status channel is
  documented NON-BLOCKING with direct precedent (commits trading
  Q72/Q47 timeouts). Broad +60.2% sweep time is all plan-shape-driven
  (R59 changed planning only — executor untouched). Carried to review.

## 5. SCOPE §3 prediction adjudication

"Q7 immobile" FAILED — and per §1.iii that triggers re-audit, not
celebration. DPTRACE A/B: every non-NLI number bit-identical across
rounds (Q3 gather 452807.42 both; all hash/merge secondaries
identical); only nli lines repriced. Winner hash 149268.72 → serial
NL 134639.71; the §1.iii 0.315/probe bound assumed a 1.5M-row outer,
the winning order has a 119904-row outer at 0.89/probe (join math
verified coherent: 25201 + 119904×0.8895 = 131849.71). Same honest
mechanism moves Q3 (→nli 297027.94 at 1.69/probe) and Q8 (serial NL
chain, values MATCH). FIX-SEED §5 "Q9 split still wins" literally
breached the same way ({l,part,ps,supp} nli-cheapest since R56) —
criterion intent (seed-sizing guard) vs letter, for review.

## 6. Process note (R56-baseline overwrite)

`/tmp/pp2/r56/run.sh` hardcodes OUT+tag and appends logs; reuse
overwrote R56's q19/q5-top baselines. Recovered (byte-identical vs
R55 + REPORT line 35 transitivity). `/tmp/pp2/r59/run.sh` (+`run-q84.sh`)
created with OUT/TAG fixed to r59 and `rm -f` logs — use those.

## 7. Next (ledgered, not this round)

R60 partial-NL producer scope; executor NL-probe work (now biting:
Q72); width model/relPages; correlation/ANALYZE; probe rows 2-vs-5;
AGG_MIXED; R55 §3 tie-break; F3; Q8 gap; NLI inner-cost EXPLAIN
display stamper; loopCount 0-vs-1 unification (P2-09b).

Evidence tmp-only `/tmp/pp2/r59/` (captures, logs, pp/roll-ups, sweep
reports under `bench/tpcds/runtime_goopg/tpcds-results-sf05/`).
