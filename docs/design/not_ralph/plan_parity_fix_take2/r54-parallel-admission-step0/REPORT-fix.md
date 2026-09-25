# R54 Fix REPORT — upper seed.Rows from search joinrel (2026-09-10)

Date: 2026-09-10. Fix binary: `/tmp/pp2/bin/goopg-r54fix`
(tree @ `822b574` + uncommitted fix cut: `searchedtree.go` restricted
accessor, `groupingpaths.go` one assignment, two unit tests; §7).
Instrument/control binary: `/tmp/pp2/bin/goopg-r54step2` (Step-2 cut).
Same capped clone and GUCs throughout (`/tmp/pp2/clone-tpch` :5534,
`work_mem=64MB`, `mpwg=4`, `GOOPG_ANALYZE_SEED=20260905`, fresh server
per phase, every query ×2 run-stable). Top-mode unless noted
(`GOOPG_PGSHAPED_DP_TRACE=1`, `GOOPG_GATHER_PATHS=top`). Evidence
(tmp-only, NOT committed): `/tmp/pp2/r54fix/` (drivers `run-fix.sh`,
`run-q78.sh`, `run-values.sh`; per-phase server logs `start-*.log`
carry the DPPATH trace; `q{5,9,1,3,10}-s2before/fixed`,
`q{7,8}-{serial,top}-s2before/fixed`, `q84` plans; `ea/` plan-gate
corpora; values digests). PG oracle: live read-only PG 18.3 TPC-H on
:65432 (TPC-H reference per CLAUDE.md).

## 1. Attribution verdict (the round's exit table)

| query | s2before (traced winner → plan) | fixed (traced winner → plan) | PG 18.3 | verdict |
|---|---|---|---|---|
| Q5 | gathered-no-split 98673.84, loses split by 0.32 → serial HashAgg 98673.82 | split 98693.70, wins by **750.20** → Finalize→Gather→Partial 215001.45 | splits upper (Finalize GroupAgg→Gather Merge→Partial) | **✓ toward PG** |
| Q9 | split 338876.10, wins by 2055.17 → Finalize 451497.01 | split 339368.64, wins by **29801.70** → Finalize (same shape) | splits upper | **✓ kept, margin ×14** |
| Q19 | plain `Aggregate` 235204.13 (rows 37427) | plain `Aggregate` 235209.47 (rows 37580) | — (gate verdict below) | **✓ plan-gate DIFFER→MATCH** |
| Q1 | HashAgg 237493.63 | HashAgg 237493.63, byte-identical | — | **✓ identical** |
| Q3 | GroupAgg 234119.65 | GroupAgg 234119.85 (+0.20, shape kept) | — | **✓ cost-only** |
| Q10 | HashAgg 107830.14 | HashAgg 107830.24 (+0.10, shape kept) | — | **✓ cost-only** |
| Q84 ds05 | — | byte-identical plan | — | **✓ identical** |
| Q7 top | gathered-no-split 285354.48 → GroupAgg 285777.96 | split 343243.37, wins by 42176 → Finalize 536388.54 | **sorts** (GroupAgg→Gather Merge→Sort) | **✗ AWAY from PG** |
| Q7 serial | GroupAgg 363370.48 | Finalize→Partial 942609.01 (no Gather) | sorts | **✗ AWAY, both modes** |
| Q8 top | gathered-no-split 159315.73, split loses by 0.34 → GroupAgg+Sort 159315.72 | split 159409.06, wins by 161.33 → Sort→Finalize→Gather→Partial 183162.58 | **sorts** (GroupAgg→Sort→Gather) | **✗ AWAY from PG** |
| Q8 serial | GroupAgg 162836.41 | Finalize→Partial 201482.14 | sorts | **✗ AWAY, both modes** |

Q8-s2before is a second filed-not-won case with Q5's exact shape
(split loses by 0.34 ≈ Q5's 0.32); the fix flips both — the right
direction for Q5, the wrong one for Q8. Per FIX-SEED §5 (anything
beyond the listed exits fails the round back to design), **this round
FAILS BACK TO DESIGN**: the fix lands as-is nowhere; §5–§6 scope the
redesign.

Plan-gate (live-vs-live, drift-controlled): step2 21/22 DIFFER,
fixed 21/22 DIFFER — the mass-DIFFER vs the committed pin is
pre-existing drift (baseline scan rows 2000418 vs live 5915197 +
const-fold display drift, established by the step2-binary control in
the scope round). The exactly-one live-vs-live move is Q19→MATCH.
No other query's gate verdict moved.

## 2. Traced census (fixed phase — all margins on the lines)

Q5 (rows=1, width=136): split 98693.70 accepted=WINNER (inputtotal
98693.19); gathered pk=0 99443.90 dominated (inputtotal 99407.21);
gathered pk=1 99933.16 accepted; sorted 217260.97; hashed serial
216771.70 accepted (inputtotal 216735.02 — byte-identical to
s2before: the serial arm does not consume the seed, §3).

Q9 (rows=5000, width=144): split 339368.64 accepted=WINNER
(inputtotal 339156.14); gathered pk=0 369170.34 dominated
(inputtotal 366834.64); gathered pk=2 397523.78; sorted 513231.84;
hashed 484878.39 (inputtotal 482542.70, unchanged from s2before —
again, serial untouched).

Q7 (rows=8817, width=248): split 343243.37 accepted=WINNER
(inputtotal 338133.16); gathered pk=0 385419.94 dominated
(inputtotal 376124.70); gathered pk=3 657636.98; sorted 910442.63
(inputtotal 901147.38); hashed 638225.58.

Q8 (rows=1, width=72): split 159409.06 accepted=WINNER (inputtotal
159403.05); gathered pk=0 159570.39 dominated (inputtotal 159552.60);
gathered pk=1 159709.16; sorted 184555.90; hashed 184417.13.

## 3. Mechanism (where the margins come from)

The fix changes only agg-term pricing via `seed.Rows`: the subtree
basis (`inputtotal`) is untouched by the assignment — Q5's split
inputtotal moved +19.07 (98674.12→98693.19) while the gathered arm's
inputtotal moved +733.40 (98673.81→99407.21) = 0.1×(7335−1.2): the
gathered arm pays `parallelTupleCost × seed.Rows` to cross the whole
input, the split arm pays crossing on partial-group states plus the
Partial pre-aggregation price. Margin swing on Q5: 0.32-loss → 750.20-win. The to-the-decimal
prediction-match is the gather term (0.1×(7335−1.2) = 733.38
predicted vs +733.40 measured = FIX-SEED §3's 7335 case); the full
750.52 swing additionally absorbs the split inputtotal +19.07 move —
it confirms `sr.Rows=7335` fired in production, and corrects
REPORT-step2 §4's `0.1×(1834−4) ≈ 183` partial-row guess: the source
is the SERIAL-row search rel, not the partial.

Independent confirmation from the gather term: Q9's gathered
inputtotal moved +26268.90 = 0.1×(303093−40404) ⇒ sr.Rows=303093;
Q8's +236.90 = 0.1×(Δ2369) ⇒ sr.Rows≈2370 (legacy ~1). Both exact
to the decimal. The seed source is proven live on Q5/Q9/Q8; Q7's
918503 stands on the 4× rule (§4).

## 4. Two isolated defects (the estimator table)

| Q | legacy seed | sr.Rows (search) | plan join rows | search/plan | PG join rows | plan/PG |
|---|---|---|---|---|---|---|
| Q5 | 1.2 | 7335 | 1834 | 4.00× | 2929 | 0.63× |
| Q9 | 40404 | 303093 | 75773 | 4.00× | 75748 | 1.00× |
| Q7 | — | 918503 | 229626 | 4.00× | 2520 | 91× |
| Q8 | ~1 | ~2370 | 592 | 4.00× | — | — |

(a) **Search-side parallel-rows 4× defect**: `search/plan` is
4.000× on all three harvested queries (75773×4=303092±1;
1834×4=7336∓1; 229626×4=918504∓1; 592×4=2368±2); the factor is
`max_parallel_workers_per_gather=4` from the GUCs. The search
joinrel rows are parallel-inflated relative to goopg's own plan
display AND to PG (Q9: plan 75773 ≈ PG 75748 to 0.9997×, search
4× both). The fix therefore prices seed terms on 4×-inflated rows
on every query. Dividing the search rows by the parallel degree
repairs Q5 (1834 vs PG 2929) and Q9 (75773 = PG-exact) to
near/fully-calibrated seeds.

(b) **Q7-class plan-side estimator gap**: even in plan convention
goopg's Q7 join (229626) is 91× PG's (2520, per-worker Sort input
under PG's Gather Merge; ×2 workers = 5040 total still leaves
45×). The 4× convention fix barely dents the tournament bias:
sort drops ~4.4× in N log N (918503×19.8→229626×17.8) while split
costs drop ~4× linearly — the sort/split ratio improves only by
the log factor (~1.11×) against a 567199.26 sorted-vs-split gap.
Q7 needs the join-selectivity fix (the n1/n2 OR-clause side is the
prime suspect: PG's Nested Loop rows 60539/50000/5000/800 vs
goopg's Parallel Hash Join chain at 459252/459252/229626), separately
scoped. Prediction: (a)-alone flips Q5/Q9 margins toward PG-exact
but Q7 still splits — re-measure, do not assume.

Net assessment of the fix direction: legacy 1.2→search 7335 on Q5
moved the seed from 2400×-under to 2.5×-over PG — directionally
right, won PG's shape. On Q7 the same sourcing feeds a 364×-over
seed that prices the sorted arm out superlinearly. The source is
not trustworthy until (a); the tournament is not PG-faithful on
Q7/Q8 until (b).

## 5. Display gap (pre-existing, characterized, non-flipping)

For split winners the plan-line upper costs sit on the PLAN-side
subtree basis, not the traced search-side basis: Q5 plan Partial
215001.42 = plan join total 215001.41 + dust (traced split total
98693.70 = search inputtotal 98693.19 + gather terms);
Q7 535903.60→536101.98; Q9 450425.42→451334.51. The gap is
PRE-EXISTING (Q9-s2before: traced 338876.10 vs plan 451497.01,
Δ112.6k) and merely newly visible on Q5/Q7, whose winners changed
serial/gathered→split. Gathered/serial winners plan≈trace
(Q5-s2before serial Δ0.34; Q7-s2before gathered Δ~10²) — consistent
with copy-back carrying traced costs 1:1 for surviving nodes while
the synthesized Finalize→Gather→Partial chain re-derives from plan
children. Winners are chosen in trace-space, and the subtree basis
cancels in split-vs-gathered comparisons (both = subtree +
arm-terms), so the gap cannot flip an outcome: Q5's plan-basis
serial/gathered alternative is ~216001 (plan join + gather setup)
vs split 215001 — split wins in both bases. Redesign item:
term-level audit of the split re-derivation (confirm no double-count
of Partial terms between inputtotal and plan lines), plus a
measurement-protocol note — plan-gate cost diffs on split winners
are on a different basis than the tournament that chose them.

## 6. Correctness backstop (values arm, 8/8 identical)

Full result-set digest compare, drift-controlled clone, top-mode,
fresh server per binary — all md5s byte-identical across
`goopg-r54step2` → `goopg-r54fix`:

q1 3cfdc698… / q3 1137ed9c… / q5 9a6c37e9… / q7 a251eec1… /
q8 6008ba10… / q9 fef63d80… / q10 cfb5fca6… / q19 c408d162…

Single-run wall times (correctness only, no timing claim):
q1 5.90→3.33 / q3 2.50→1.99 / q5 1.46→1.77 / q7 3.52→4.18 /
q8 0.52→0.43 / q9 4.07→3.49 / q10 1.16→1.30 / q19 1.78→1.75.
No timeouts, no hangs. The re-priced winners return identical rows.

## 7. Implementation (what the cut does — for the reviewer)

`searchedtree.go`: new `searchedJoinInputRelOf` — descends ONLY
through row-preserving pass-throughs
(`*Project/*Sort/*Gather/*GatherMerge/*Memoize/*OrdinalityWrap/*LockRows`
via Child, max depth 32), returns the first marked search root's
rel, nil otherwise (Aggregate/WindowAgg/Distinct/Filter/Limit/SetOp/
multi-child/unknown/nil all stop — the measured aggregate-child
shape is Sort(Project(Join)) and passes; `Project(Filter)` and
`Aggregate(Project)` nesting provably stop). Deliberately NOT reusing
`searchedRelOf`, which over-descends via `boundaryWalkChildren`
into filter/selectivity-carrying subtrees.

`groupingpaths.go` `createGroupingPaths`: one fail-closed assignment
after the seed Cost block —
`if sr := searchedJoinInputRelOf(child); sr != nil && sr.Rows > 0 {
seed.Rows = sr.Rows }` — Cost untouched (pseed shared under split
and gathered ⇒ input price cancels ⇒ Rows-only sound).

Tests: `TestSearchedJoinInputRelOfDescendsOnlyThroughPassThroughs`
(6 pass-throughs incl. measured Sort(Project) chain; 12 stop cases
incl. capped-LockRows ×2; nil-rel/nil-node; 40-deep cap) and
`TestCreateGroupingPathsSizesSeedFromSearchRel` (Project(Join)
child, sr.Rows=1834 searched vs unmarked legacy, strictly-greater
hashed total; pins the existence-before-verdict direction).
Optimizer suite + `go vet` green (no `-count=1`).

## 8. TPC-DS SF0.5 sweep

`GOOPG_BIN=/tmp/pp2/bin/goopg-r54fix scripts/tpcds-sf05-regression.sh
sweep` (foreground, ~1h, exit 0): **PASS=95 (57 ck-verified),
MISMATCH=0, CKMISMATCH=0, ERROR=0, TIMEOUT=0, SKIP=4**
(oracle-side SKIP_QUERYGEN). Log `/tmp/pp2/r54fix/ds05-sweep.log`;
report `bench/tpcds/runtime_goopg/tpcds-results-sf05/
sweep-20260910-233624.txt`. Row counts hold across the DS corpus —
the re-priced upper tournaments return identical rows everywhere,
matching the TPC-H values arm (§6).

Plan-shape channel (NON-BLOCKING by gate design): 41 same / 58
changed vs the RECORDED plans — but the recorded baseline is stale
(R48-era commit `8b1ee800e`; R53-slice-1 landed since), so the 58
conflate landed-round moves with fix-attributable moves. A
step2-binary DS sweep (the clean before/after) was NOT run:
FIX-SEED §4's DS protocol arm is Q84-only (plan byte-identical, §1;
rows PASS here), and a second hour buys no verdict-relevant
information — the round already fails back on TPC-H Q7/Q8 (§9).
Redesign re-measure (item (iii)) should include the step2-binary DS
sweep if it touches DS-relevant tournaments.

## 9. Exit (owed by §5 of FIX-SEED)

Proved: sizing the upper seed from the search joinrel moves Q5 to
PG's split (gather-term prediction-match to the decimal, §3), keeps Q9's split with
a ×14 margin, moves Q19's plain arm onto the gate pin, and leaves
Q1/Q3/Q10/Q84 shapes untouched with identical result sets. Failed:
Q7/Q8 move away from PG in both modes (sorted→split on 91×/4×-class
inflated seeds). Verdict: **FAIL BACK TO DESIGN**. Redesign scope:
(i) search parallel-rows ÷degree (defect (a) — uniform 4.000×,
mechanical); (ii) Q7-class join selectivity (defect (b) — 45–91×,
estimator work, separately scoped); (iii) re-measure all §2 margins
on calibrated seeds — predictions, stated before any redesign work:
Q7 still splits until (ii) lands (§4: the ÷4 improves the sort/split
ratio only ~1.11× against a 567199.26 gap); Q8's tournament after
(i) prices the seed at ~592 (PG sorts Q8 — nothing in (i)+(ii) as
scoped obviously restores its sort, so the re-measure must name Q8's
winner explicitly rather than inheriting Q7's);
serial-mode expectation after (i): the seed change moves the serial
tournament with no gather term in play (this round's serial Q7/Q8
flips prove it, via partialGroups/crossedRows) — so serial Q7/Q8
must be re-measured too, and a serial-persistent flip means (ii)-class
work, not a parallel artifact; (iv) split re-derivation term audit
(§5). No part of this cut lands as-is.
