# R56 SCOPE — upper-sort placement: worker Sort under Gather Merge (2026-09-11)

Follows R55 LANDED (`c37128039`, REPORT.md): P3 133→26 inside the
24–94 bar; Q7/Q8/Q5/Q1/Q3/Q9/Q10 plans byte-identical to q7fix;
values 8/8; DS SF0.5 PASS=95 all-zero. R55 probe A exonerated the
sort TERM (`costSortRun` term-identical to PG `cost_tuplesort`,
~80.9 both engines at 1468/width-248). R55 §3 tie-break
calibration stays LEDGERED and ORDERED after this audit.

R54 end state (the contest this round accounts for — Q7 top,
`start-q78-top-r55.log`, ×2 stable, R55 byte-identical):

- gathered no-split HashAgg **149268.71** (winner)
- split Finalize→Gather→Partial **149301.53** (+32.82)
- Sort+GroupAgg (pk=3) **149651.11** (+382.40)
- PG 18.3 sorts: GroupAggregate→Gather Merge→Sort over a 2520-row
  nested-loop join (live :65432, same GUCs: work_mem=64MB,
  mpwg=4, stock cost defaults).

One read-only probe ran (no code, no servers — trace + oracle
reads only). Verdicts below are numeric, not directional.

## 1. Probe C verdicts

### (i) costAgg terms: NO-MECHANISM (the R55-named lead exonerated)

Term-by-term `costAgg` (`cost_funcs.go:370-449`) vs PG `cost_agg`
(`costsize.c:2682-…`):

- Grouping-comparison term (`cost_funcs.go:381,387-388,398`:
  `cpuOperatorCost × numGroupCols` per input tuple) is IDENTICAL
  to PG's `(cpu_operator_cost * numGroupCols) * input_tuples`
  (SORTED and HASHED both). The R55-named lead is exonerated.
- Trans/final (F3 stubs: `cpuOperatorCost × nAggs`, zero
  startup) differ from PG's procost-derived `AggClauseCosts` —
  but SYMMETRICALLY: the traced upper adds are 58.77 / 58.76 /
  58.75 / 58.76 (hashed-serial / sorted-serial / split-finalize /
  gathered-finalize — every strategy, same cents). A term that
  adds the same number to all four finalists cannot own a
  margin between two of them. F3 stays ledgered; it is not
  this round.
- Spill arm (R3) INERT here: 1468 partial groups « threshold
  (collapses to nbatches=1, depth=0 — the code's own early
  return). PLAIN-via-HASHED matches PG's PLAIN arm (comment
  block, unchanged).

### (ii) The margin equation closes to the cent (no owner left above it)

With `parallelTupleCost=0.1` (verified PG-faithful: oracle
`cost.h:29` + live `SHOW` both 0.1 — an early draft of this
probe misremembered 0.001; the oracle corrected it, no ledger
item), `cpuOperatorCost=0.0025`, workers=4:

| finalist | chain | arithmetic | total |
|---|---|---|---|
| gathered pk=0 | Gather(join 147622.55): +1000+0.1·5874=+1587.40 → 149209.95; Finalize(5874 rows, 2 groups): 0.01·5874+0.025=+58.76 | 149209.95+58.76 | **149268.71** |
| split | Partial(join): 0.01·1468+…=+33.03 → 147655.58; Gather: +1000+0.1·5872=+1587.20 → 149242.78; Finalize(5872,2): +58.75 | 149242.78+58.75 | **149301.53** |
| sorted pk=3 | Gather (same 149209.95); Sort(5874 rows, width-248): 0.005·5874·log2(5874)+0.0025·5874 = 367.74+14.69=+382.40 → 149592.35; GroupAgg(5874,2): +58.76 | 149592.35+58.76 | **149651.11** |

The 382.40 margin sits ENTIRELY in the Sort node at N=5874 —
a term Probe A already proved faithful, at an N the join
estimator (not any agg term) produced. costAgg contributes
exactly 58.76 to both sides: zero, net.

### (iii) Structural gap: WELL-DEFINED-CUT

PG's winning SHAPE — GroupAggregate → Gather Merge → Sort →
partial join (worker sorts of 2520, leader merge) — is ABSENT
from goopg's tournament. goopg's pk=3 rival is GroupAggregate
→ Sort → Gather (leader sort of 5874): Sort OVER Gather, not
under Gather Merge. Trace proof: zero keyed partials on the
aggregate's join input (no `kind=3` partial line carries
pathkeys — all four pk=3 lines are upper/final sorts), so
`makeGatherMergePath` (fires only over already-sorted
partials, `gatherpaths.go`) never sees the join rel. (Note:
`kind=3` covers both serial `join.hash` and partial
`join.hash.partial` paths — the invariant is about keyed
partials, i.e. no `upper.*gathermerge` producer pre-cut; the
implementation round pins the producer-name form.)

Precedent: C-19e (`docs/design/planner-c19e-partial-sort/`,
flag-gated, landed) replaced the scan-level
`sortPartialRootPays` structure-rule with a cost tournament
(worker Sort + GatherMerge vs leader Sort + Gather, "no new
constant" — every term an existing PG-faithful function) and
moved exactly one plan in 22 (q16). This round is the
upper-aggregate analogue of C-19e, at the site C-19e did not
touch (search-built upper in `partialaggupper.go`, not the
post-pass).

## 2. The authorised cut (site b ONLY)

Add a third no-split upper arm next to the hashed/sorted arms
(`partialaggupper.go:393-415`): `GroupAgg → GatherMerge →
Sort → pseed` — worker Sort priced at perWorkerRows through
`costSortRun`, merge through `gatherMergeCost`, upper through
the SORTED arm of `costAgg`, all existing functions, no new
constant (C-19e §3's argument, unchanged). It competes in
`add_path`; `setCheapest` adjudicates.

Out of scope (hard shapes, ledgered): site (a) — search-level
sorted partial join paths (a search-surface expansion; Q9-rebind
hang caution M0072-0002 applies: bound the change, and this
is not it); F3 procost; AGG_MIXED; the join-rows N-lead
(5874 vs PG 2520 — estimator territory, §3's residual owner);
R55 §3 tie-break calibration (still ledgered, still ordered
after this audit); Q8's +25k sorted gap (different scale and
owner — join-input N, not upper structure; winner-immobility
must-held, §4).

## 3. Prediction (falsifiable, flip NOT promised)

At current traced inputs (N=5874 leader / 1468 per worker,
width-248, G=2, 1 agg, 3 group cols):

- worker Sort(1468) ≈ 0.005·1468·log2(1468)+0.0025·1468
  ≈ 77.22+3.67 = **80.89** (probe A number, reproduced; pin
  N stated explicitly as 1468, the trace-displayed rows —
  `perWorkerRows` divides as 5874/4 = 1468.5, which moves
  only the third significant decimal).
- GatherMerge run over 5874 ≈ 5874·0.005·log2(5)
  +0.0025·5874+0.1·5874·1.05 ≈ 68.20+14.69+616.77 = 699.66
  (+~0.06 heap-creation startup inside `gatherMergeCost`,
  negligible against the bar but included in the §4 pin;
  `workers` = `pseed.ParallelWorkers` = 4, N = 5);
  vs current Gather run 0.1·5874 = 587.40 (setup 1000 and
  pseed cancel).
- Predicted rival: 149651.11 − [(587.40+382.40) − (80.89+699.66)]
  ≈ 149651.11 − 189 ≈ **149460**: margin 382 → ~190,
  direction DOWN, toward gathered but NOT past it.

Bar: the pk=3 rival lands in **[149350, 149550]** with all
other finalists byte-identical. A landing above 149651 (no
movement) FAILS the prediction — the tournament model is
wrong somewhere specific. A flip past gathered is NEITHER
required NOR forbidden: at unfaithful N (5874 vs 2520) a flip
would be as unowned as the tie, so a flip triggers re-audit,
not celebration. Either in-bar outcome promotes the N-lead
(join rows) to the next estimator follow-up WITH a number
(the residual margin at PG's N).

## 4. Must-holds (all assignments)

- Fix-seed §5 (`r54-parallel-admission-step0/FIX-SEED.md`) +
  structural Q19 MATCH + Q5-split-wins non-vacuous.
- Traced decomposition (§1.ii) reproduced by unit arithmetic
  BEFORE the candidate lands (pin the 58.76/33.03/382.40/58.75
  adds; the implementation round's first test).
- TPC-H digest MATCH; TPC-DS SF0.5 sweep PASS=95 all-zero;
  `pg-plan-parity-diff` on the measured corpus, unparsed=0.
- Q8's winner unmoved (split still wins); NO other query's
  winner moves (broadening: digest + sweep cover it).
- Sibling-path audit in-loop: the new arm's three terms vs
  `costSortRun` / `gatherMergeCost` / `costAgg` call sites.

## 5. Status

R56 = this audit (probe C, §§1–2) + §2 implementation in the
next task. Nothing here authorises a constant, a threshold
tweak, or §3 calibration.

### Amendment A (implementation round, 2026-09-11): companions

The §2 cut as authorised (site b ONLY — the `partialaggupper.go`
arm) proved insufficient in two places and exposed one latent
defect; the implementation round added three companions, all
mechanism-only under the same no-new-constant argument,
recorded in REPORT.md §2/§5 and approved at review
(APPROVE-WITH-NOTES):

- `upperorderedgrouping.go`: `groupingEmissionPathkeys` accepts a
  `PathGatherMerge` child as well as `PathSort` — found
  pre-compaction by the Q7 root-cost delta (without it the new
  candidate evicts the leader-sort candidate under identical
  pathkeys and `electOrderedGrouping` declines to a
  legacy-priced ORDER BY seed).
- `parallel.go`: Sort-through arms in the four spine walks
  (`drivingScan`, `stamp`/`unstamp`, `findPartialSubtree`) — the
  stamp arm is REQUIRED (`gatherChildPlan` refuses a worker
  subtree with no driving scan, so without it the upper arm
  could never build).
- `cte_inline_pushdown.go`: transparent `*GatherMerge`
  passthrough in `pushConjunctIntoCTEBody` — in-loop defect
  (Q78's three `date_dim` scans lost `d_year = 1998` under the
  new GM shape; single root cause, proved by unit + clone A/B +
  DS channel A/B).
