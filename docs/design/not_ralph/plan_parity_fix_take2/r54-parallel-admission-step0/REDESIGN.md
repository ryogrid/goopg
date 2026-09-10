# R54 Redesign SCOPE — re-land totals-sourcing + Q7 selectivity + split audit (2026-09-10, rev 2)

Follows the failed fix round (REPORT-fix.md §9: Q5✓/Q9✓/Q19✓ but
Q7/Q8 sorted→split away from PG in both modes ⇒ FAIL BACK TO
DESIGN; code cut reverted, HEAD clean). Rev 1 of this scope drew the
wrong prescription from the §0 correction (divide the sourced seed by
the parallel divisor); scope review REJECTED it 2026-09-10 — the
tournament is already single-divisor-disciplined, so the division
would double-divide the split, corrupt serial arms through the shared
seed, and cannot even name its divisor at the sourcing site. This rev
2 keeps rev 1's §0 correction (which the review accepts as sound) and
rebuilds the assignments on the verified accounting below.

## 0. Correction: there is no 4× search-side inflation

REPORT-fix §4 defect (a) compared goopg search totals against PG
EXPLAIN figures and read 2.5–4× "overshoot". That comparison was
cross-convention and is WITHDRAWN as a defect characterisation; the
4.000× factor table itself (search = plan × 4 exactly) stands and is
reinterpreted below.

PG displays PER-WORKER rows below a Gather — proven on the live
:65432 oracle 2026-09-10: `EXPLAIN SELECT count(*) FROM lineitem`
with 4 workers planned shows `Parallel Seq Scan rows=1499818`
(1499818×4 = 5,999,272 ≈ 6M SF1 lineitem). So PG's Q5 Hash Join
rows=2929 and goopg's plan rows=1834 are BOTH per-worker figures,
directly comparable: goopg's per-worker join estimate is 0.63× PG's
(a modest estimator gap, not a convention bug). Totals-vs-totals:
goopg search 7335 (= 1834×4, Workers Planned: 4 on the fixed plan)
vs PG 2929×divisor(2 workers) = 5858–8787 — same ballpark. Q9:
plan 75773 ≈ PG 75748 to 1.0003× per-worker; search 303093 in range
of PG totals. The search rel rows are CORRECT PG-style serial
totals (`joinrel.Rows`; partial paths divide by the divisor for
per-worker pricing per `final_cost_hashjoin`, `joinpathsparallel.go:
186-187`, undone by `computeGatherRows`, cost_funcs.go:~857-863 —
with leader participation off in this config (`mpwg=4`, divisor 4
per the `upperSplitDetail` trace; participation-on would give 5 and
the exact-4× table would not hold)).

The accounting the review verified (all cites HEAD, post-revert)
is that the upper tournament is ALREADY single-divisor-disciplined
and totals-sourcing is convention-correct — no seed-driven term is
overpriced:

- Serial arms price totals: `addGroupingPaths` takes
  `inputRows := seed.Rows` (groupingpaths.go:310) straight into
  `costAgg` trans/cmp terms (ibid. :335-341, :352-359, :392-398).
- The split divides EXACTLY ONCE: `perWorkerRows := inputRows / d`
  (partialaggupper.go:286); the partial arm prices `perWorkerRows`
  (:467), group counts derive from it (:291); `parallelSeedCost`
  divides only the input-price RUN component (:314 + :523-531).
- The finalize prices `crossedRows = partialGroups × d` (:463,
  :499-503) — group-states that reach the leader, re-multiplied, not
  per-worker rows leaking upward.
- BOTH gather crossings take totals: the no-split `gatherCost(cp,
  pseed.Cost, inputRows)` (:357-359) and the above-partial
  `gatherCost(cp, partialCost, crossedRows)` (:486-488) — the latter
  is `compute_gather_rows` of the partial path, the PG-correct row
  argument per the :479-485 comment.

So there is nothing to divide: the failed round's totals-sourcing
was the convention-correct cut, and rev 1's ÷4 would have
double-divided the split (perWorkerRows ÷ d again) while corrupting
every serial arm through the SHARED seed pointer (`createGroupingPaths`
hands one `seed` to both `addGroupingPaths` and
`addPartialAggSplitPath`, groupingpaths.go:81/88 — including the
Q19 plain arm, :335-341). It is also unimplementable at the proposed
site: the divisor is ill-defined there — serial means 1, the split
means `d` from `upperSplitWorkers` (partialaggupper.go:271-280),
workers live on Paths not rels, and the pre-unwrap child at the
sourcing site differs from the child the split sizes on (:237-261).

The REAL defect inventory is therefore: (a) the LEGACY seed (~1.2
rows from `EstimateRows` over the finished child tree,
groupingpaths.go:68) was neither per-worker nor total — a collapse
that zeroed the serial contest and degenerated the split, which is
why totals-sourcing helped Q5/Q9 while the Q7/Q8 flips need
estimator-side owners; (b) Q7-class join selectivity is 91×
per-worker vs per-worker, convention-clean and genuinely bad (see
(ii)); (c) the split's per-arm pricing is unaudited — plan Partial =
plan join + dust while the traced search inputtotal is totals-scale
(see (iv)). Rev 1's margin predictions were arithmetically right but
attached to the wrong mechanism; §1(iii) restates them under the
correct one.

## 1. Assignments

(i) **Re-land totals-sourcing (mechanical, convention-correct).**
The reverted one-line assignment in `createGroupingPaths`
(`seed.Rows = sr.Rows`), re-derived per REPORT-fix §7 + fix-round
review notes, which all carry over: restricted accessor (pass-throughs
only), LockRows-cap gate, Memoize/CTEScan comments, ordering-not-
exactness test comment, capped-LockRows stop cases. NO division —
§0 shows the tournament already divides exactly once downstream, and
the seed is shared with the serial arms (including Q19's plain arm,
groupingpaths.go:335-341), so any divisor applied here would corrupt
them. Expected outcome is the failed round REPRODUCED to the decimal
(Q5 +750.20, Q9 29801.70-family) — that reproduction is the round's
convention-clean baseline, not its deliverable.

(ii) **Q7-class join selectivity (estimator work, IN this round).**
229626 vs PG 2520 is per-worker vs per-worker (91×), convention-clean
per §0 and genuinely bad. Prime suspect unchanged (n-side OR-clause
selectivity). Q8's joins join this scope iff the (v) harvest says so
— Q8 top's sorted gap (25146 ≈ 16%) is small enough that either arm
could own it, so the harvest decides rather than a prediction.

(iii) **Re-measure on re-landed seeds, predictions pre-stated:** the
re-land reproduces the failed round (Q5 split +750.20, Q9 split
29801.70-family, Q19 MATCH, Q1/Q84 identical, Q3/Q10 cost-only) —
any deviation from those figures is a regression of the re-land
itself and fails the round before (ii)/(iv) are read. Q7 STILL
SPLITS until (ii) lands (567199.26 gap is estimator-scale; no
convention term moves it). Q8 top: SPLIT at re-land, then MEASURED
after (ii)/(iv) — a continued split with the audit clean points at
estimator, a flip points at the audited arm. Serial Q7/Q8 flips are
pure estimator signal (no divisor exists in serial mode) and are
judged under (v), never "fixed" by tournament terms.

(iv) **Split per-arm audit (PROMOTED — co-equal with (ii)).** Decompose
the split into Partial vs Finalize+Gather per-arm costs and compare
against PG's per-worker/per-totals figures arm by arm: plan Partial =
plan join + dust while the traced search inputtotal is totals-scale
(REPORT-fix §9) — confirm no Partial-term double-count and name
which arm over/under-prices with a number. The measurement-protocol
note (trace-space winners vs plan-space lines) stands.

(v) **PG Q8 join-rows harvest + serial-Q8 judgment.** Serial has no
divisor, so serial flips are pure estimator signal: harvest PG's Q8
per-worker join rows and judge whether seed ~2370 is
estimator-justified: PG's figure within 2× of ~2370 counts as
estimator-justified (serial Q8's flip was correct-direction-on-
bad-legacy); outside 2×, Q8 joins (ii)'s scope.

(vi) **Step2-binary TPC-DS SF0.5 sweep** iff the round touches
DS-relevant tournaments (REPORT-fix §8: skipped last round as
verdict-irrelevant; same rule).

## 2. Exit criteria (mirror FIX-SEED §5)

Must-hold (per FIX-SEED §5, none vacuous): Q1/Q84 identical plans;
Q3/Q10 in the §4 EXPLAIN-diff measurement set — shapes kept, costs
recorded to the decimal (FIX-SEED §5 states no numeric tolerance for
them, so none is invented here); Q19 MATCH on the live-vs-live gate
(the plain arm prices the shared seed, so any seed regression shows
here first); Q5 split WINS (the +750.20 re-land reproduction — a
serial win here means the re-land itself regressed, not that the
estimator spoke); Q9 split kept; values arm 8/8 md5-identical against
the REPORT-fix §7 hashes; optimizer suite + vet green (no
`-count=1`); review APPROVE/APPROVE-WITH-NOTES with notes applied.
MEASURED, not prescribed: the Q8 winner (top + serial) with its
owning defect named with a number ((ii)-estimator vs (iv)-arm), and
the Q7 margin delta from (ii) — which is itself gated: (ii) fails if
its margin delta is ~0 or unattributed, even though the round does
not fail back on Q7's winner. Q7 sorted is NOT required of this round
(owned by (ii)); Q7 still-split is the predicted standing order, not
a failure. Anything that moves a must-hold winner fails back again —
and a Q5 serial win or Q19 DIFFER on convention-clean terms is a
failure of THIS round's re-land, not an acceptable "measured"
outcome.

## 3. Protocol (unchanged)

Drift-controlled clone `/tmp/pp2/clone-tpch` :5534,
`GOOPG_ANALYZE_SEED=20260905`, fresh server per phase, ×2
run-stable, top-mode + serial-mode (serial now first-class per (v)),
trace-top harvest + plan-gate live-vs-live + values md5 arm. Private
binaries under `/tmp/pp2/bin/` (never the shared bench bin).
