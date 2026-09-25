# R69 REPORT — NLI rescan-startup term landed (2026-09-11)

SCOPE: `r69-nli-probe-audit/SCOPE.md` rev 2 (reviewed
APPROVE-WITH-NOTES, notes applied; committed `65ccd8c`). Implements
slice (a) from R68 STEP-0 with the P0 table completed in-round.

## 1. Result: one missing PG term added; values green; moves adjudicated

`nestloopCost` gains PG's `(outer−1) × rescan_startup`
(`costsize.c:3299-3302`), wired only at the NLI site via
`nestLoopInnerRescanStartup` (parameterised → descent; Memoize →
modeled rescan startup; plain/materialised → 0, i.e. today's literal
— unchanged by construction). Q9 L6: 117342.97 → 245962.63 measured
(not the naive +115k → ~232k: the L5 input itself repriced
96778.63→111738.79 through the same term one level down). Hash still
539k — numbers move, shapes stay, the R1 pattern; the flip belongs to
the width program.

- `internal/optimizer/cost_funcs.go` (+11/-2): the new term with the
  oracle citation and the double-subtraction guard note.
- `internal/optimizer/joinpathsmemoize.go` (+21): the helper.
- `internal/optimizer/joinpathsnli.go` (+8/-1): the call site (Total
  passed with startup included so the run part is untouched).
- Pins: `nli_rescan_startup_test.go` (4 arms).
- No planner/executor/stats change; no constant moved.

## 2. P0 table (completed — the SCOPE's central deliverable)

Q9 L6 (`outer={0,1,2,3,5} inner={4}`, 303093 probes), goopg inputs
from temp logging (reverted; `r69attr.txt`, `R69IDX` lines) vs PG
`costsize.c` with live cluster inputs (orders 27814 pages/1.5M rows,
o_orderkey correlation 0.20440924, effective_cache_size 2GB both
engines, `pg-q9.txt`, `show-*.txt`):

| PG term | PG value | goopg counterpart | goopg value | verdict |
|---|---|---|---|---|
| outer run/startup | outer scan | same path | same | MATCH |
| inner run (once) | ML-discounted ~0.06 | `innerRun` | 0.06 | MATCH |
| (outer−1)×rescan_start | 303092×~0.38 ≈ 115k | literal `0` (all 3 sites) | 0 | **MIS-TRANSCRIBED → fixed** |
| (outer−1)×rescan_run | 303092×~0.06 ≈ 18k | `(outer−1)×matRescan` | ~18k | MATCH |
| cpuTuple×ntuples | same formula | same (P2-07) | same | MATCH |
| residual qual | same formula | `qualEvalCost` | same | MATCH (R1) |
| enable term | counts always | `DisabledNodes` | same | MATCH (R59) |
| SEMI/ANTI early-stop | :3388+ | absent | — | NON-OWNER (recorded) |
| qual startup | :3507 | folded in Total | — | NON-OWNER (recorded) |
| tlist per-output | :3512-13 | absent | — | NON-OWNER (recorded) |

Join-added: goopg 20564.34 (0.068/probe) vs PG ≈ 133k. The gap is
essentially all rescan-startup. NLI probe pricing otherwise matches
within the deliberate 2.0 probe calibration (goopg heap 0.038 vs PG
0.0185 — the knob, R58-owned, wall-clock-justified; loopCount 6M both
engines — lineitem unrestricted base rows, R59 parity holds here).
`nestLoopInnerRescanStartup` unit-pinned per arm.

## 3. Gate ledger (SCOPE §5; long arms all FOREGROUND)

1. Suites + vet green (incl. the new pin; two fixture tests failed
   mid-round on the FIRST (buggy) cut — see §4.2 — and pass on the
   landed one).
2. Spotcheck DEFERRED (fourth round running; `:65433` peer-held;
   renderer-n/a — cost round with full values gates instead).
3. Values 24/24 MATCH (TPC-H digest) + DS SF0.25 sweep PASS=96
   MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3, engine-sha
   fingerprinted. P2 holds.
4. Explain A/B: TPC-H exactly Q2/Q5/Q7/Q9/Q18 move (Sort/Join/Filter
   lines from NLI→hash flips; plus verdict-neutral cost-only estimate
   repricing on Q3/Q8/Q11/Q17 from the same repricing — values green,
   verdicts held); pp
   6/14/0/2 both refs with verdict deltas exactly Q5/Q7/Q9/Q18
   (join-method −1, parameterisation −4, scan-type +1, qual-placement
   +1; rendering {Q10} held; Q6/Q11 MATCH held). Per-query
   adjudication (§5): Q5 strictly fewer (top NL→hash = PG's method;
   probe index→seq is join-order-downstream, owned); Q9 strictly
   fewer (Memoize gone = PG's plain probe); Q7 trade (method toward,
   cond/qual join-order-downstream, owned); Q2/Q18 sideways (same
   verdicts, NL→hash inside divergent regions). DS plans-channel:
   24 NL→hash moves, all containing lose-NL+gain-HJ (whole-file
   SequenceMatcher diff); method-count distance to live-PG
   (`:65438`, serial AND parallel captures) moves toward on 14,
   neutral on 4; 6 residuals (Q1/Q17/Q27/Q35/Q69/Q77) examined —
   Q17/Q27 are tie-break flips at Memoize sites with goopg-measured
   ndistinct (1983/1986-class, cache justified both sides) and stay
   explained by constants, not model (§5.3); Q1/Q35/Q69/Q77 analogous
   small-shape hash-vs-NL ties pending the width program's reorder.
   Zero unexplained away-moves (R10-§7).
5. `make plan-gate`: 20/22 DIFFER both binaries
   (`/tmp/pp2/r66/plangate-s2cpost.txt` pre, `/tmp/pp2/r69/
   plangate-r69bpost.txt` post — verdict sets byte-identical);
   full-file pre/post diff confined to NLI/hash/memoize/index shape
   lines (cost round, not renderer round — the gate cannot bless
   moves, pp does). Opt-out with rationale (pre-existing staleness).

## 4. Deviations and findings (ledgered)

1. **Mid-round adjudication reversal (K22-guard working as designed).**
   DS Q44/Q54 first read as chase away-moves; PG-oracle + Sort-anchored
   dumps re-placed them (Q44: PG keeps SubqueryScan → boundary rule;
   Q54: PG flattened → CTE rule). Neither shipped unadjudicated.
2. **First-cut double-subtraction, caught by the suite (not by
   review).** Passing only the startup into `nestloopCost` subtracted
   the descent from the already descent-free run part. Two fixture
   tests failed (`TestNLIArmOffersBothCandidates`,
   `TestPGShapedSearchPicksNLIOnCost`); diagnosis showed the run part
   must pass through untouched with startup ADDED separately — the
   landed shape. Suites-before-measurement earns its keep.
3. **Census discipline (two entries).** (i) Section-split plan diffs
   miss multi-statement queries — whole-file SequenceMatcher +
   raw-grep counts from here on (R50-trap family). (ii) Engine-sha
   fingerprint exonerated an "unattributed" sweep as my own binary —
   trust fingerprints over timestamps.
4. **Reference oscillation (standing).** Live-PG Q8 flips
   MISSING-NODE↔SHAPE-DIFF between same-day captures (PG-side stats
   drift); rendering verdicts identical on all refs. Fixtures binding.
5. **Carry-forwards (not this round):** hash-key selection width
   (Q31 county keys in Hash Cond vs PG Join Filter — R50 family);
   plain-NL `pathgen.go:157` keeps literal 0 (correct today:
   materialised inners; revisit if parameterised inners ever reach
   it); partial-NL `:492` untouched (E-20 deferred execution);
   knob-2.0 interplay on tie-breaks (R58 owns); width program owns
   the Q9 flip (slice (b) remains due).

## 5. Per-query adjudication ledger (R10-§7 evidence)

- TPC-H Q2: sideways (verdict unchanged; NL→hash inside
  join-order-divergent region; values identical).
- TPC-H Q5: strictly fewer (5→4 cats; top NL→Hash Join = PG's method
  at top; supplier Memoize gone = PG's plain scans; lineitem
  index→seq probe is the new orientation's consequence +
  join-order-downstream — owned, pending width reorder).
- TPC-H Q7: trade (5→5; method toward at top; new qual-placement +
  cond-signature lines are join-order-downstream — owned).
- TPC-H Q9: strictly fewer (5→4; Memoize gone; top stays NL on price
  — 245962.63 vs hash 539118.03 — pending width).
- TPC-H Q18: same verdict (NL→hash convergence inside + Memoize
  gone; residual same-category lines persist).
- DS 24: all lose-NL+gain-HJ with probe consequences; 14 converge
  method counts toward live PG (`:65438` transcripts ×24 in
  `ds-pg-adj/` serial AND `-par/` parallel regimes), 4 neutral, 6
  residuals examined — Q17/Q27 tie-breaks at Memoize sites
  (goopg-measured ndistinct 1983 vs PG 1986, cache justified both
  sides; constants decide), Q1/Q35/Q69/Q77 analogous small-shape
  ties pending width reorder. DS pre-baseline is the Slice-2 sweep
  plans (`/tmp/pp2/r66/ds-sweep-s2c/plans-20260911-223012.txt`); the
  movement census was recomputed whole-file after a section-splitter
  blind spot missed Q44/Q54 (R50-trap family). Q44+Q54 reverted by
  the boundary rule, Q49 by the table-0 rule, Q51 CASE kept as
  structure-faithful — each PG-adjudicated (§3 items 1–3 above).

## 6. Sequencing

Slice (b) width/footprint (conditional follow-up — now the load-
bearing half of Q9: hash 539k→~60k crosses NLI-true ~246k); R51
items 2–3; R52 §4.2; R54 follow-ups; #6/R61-#4/(b) watches. Join-order
count re-measured as a side effect (TPC-H 14) — judged by §2–§5.

Evidence tmp-only `/tmp/pp2/r69/` (binaries `goopg-r69attr`/
`goopg-r69b` md5 `7eceb60c…`/`7c7e28b…` (`goopg-r69attr2` was a
same-code rebuild for the extended log; its md5 is not cited),
TPC-H arms +
captures + pp verdicts incl. fresh live-PG, `ds-sweep-r69b/`
fingerprinted, `ds-pg-adj/` + `ds-pg-adj-par/` live-`:65438`
transcripts ×24×2 GUC regimes, `gate0/` oracle transcripts,
`plangate-*` pairs, server logs, `launch.sh`); pre-cut worktree
`/tmp/pp2/r66-pre-src` (HEAD).
