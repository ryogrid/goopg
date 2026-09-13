# The frontier: TPC-H's two closest queries are blocked on ONE missing capability

Written after R127's scope was withdrawn (`SCOPE-WITHDRAWN.md`). This
document exists so the next worker does not re-derive it, and does not
re-run the dead experiments a fourth time.

## 1. Where TPC-H actually stands

`match=6/22` (measured at R126, `r126-fk-persistence/r126-tpch.plans.txt`).
Ranked by divergent-category count:

| n | query | categories |
|---|---|---|
| 0 | Q1 Q6 Q11 Q14 Q15a | MATCH |
| 1 | Q10 | rendering (already MATCH) |
| 1 | **Q9** | join-order |
| 2 | **Q4** | aggregation-strategy, sort-strategy |
| 3+ | the other 14 | 3 to 7 categories each |

Corpus categories: `join-order=14`, `join-method=10`,
`aggregation-strategy=10`, `scan-type=9`, `sort-strategy=9`,
`parameterisation=5`, `qual-placement=4`, `rendering=1`.

## 2. Both closest queries converge on the SAME blocker

This is the finding. It is not obvious from any single round, only from
reading the lineage end to end.

**Q4** (`r81-q4-ordered-remeasure/STEP0.md`): the election is decided by
a startup-cost ratio against `stdFuzzFactor = 1.01`. goopg sits at 1.0086
(inside fuzz → tie-break → hashed); PG at 1.0118 (outside → sorted wins).
The startup gap is driven by input scale, and R81 is explicit that
**widths dominate rows**: semi output width **448 vs PG's 16** — "widths
ratio 28x **exceeds** the rows ratio 16.6x". R81 closed with two unblock
conditions, and named the first as **DatumBytes/projection pushdown,
"neither exists"**.

**Q9** (`r69-nli-probe-audit/REPORT.md` §6, after the NLI price audit):
"Slice (b) width/footprint (conditional follow-up — **now the
load-bearing half of Q9**: hash 539k→~60k crosses NLI-true ~246k)".
`r77-production-splice/REPORT.md` closes: "Next: rows/width program".

So: **Q4 needs widths. Q9 needs widths. Neither needs anything else
first.**

## 3. The width program has been attempted, and did not move parity

R120–R124 are that attempt, and their own reports are the evidence:

- R120 found the cost-input currency defect (`hashsize.EntryBytes` over
  the FULL column set).
- R121/R122 built narrowing and propagated it through joins.
- R123 located the confound; R124 eliminated it — and measured **R124's
  own increment** parity-neutral, with 22 of 32 narrowed rels producing
  no cost movement at all.

**CORRECTION (R128).** The sentence above originally read "measured the
whole chain parity-neutral". That is wrong and was corrected when R128
re-measured the flag directly: with `GOOPG_NARROW_COST_INPUTS=1`, TPC-H
`join-method` goes 10 → 9 and `scan-type` 9 → 8, no category rises, and
`parallelism` stays 0. **R122's two categories are real; it was R124's
increment on top of them that was neutral.** Artefacts:
`../r128-parity-over-throughput/parity-{OFF,ON}.txt`.

The distinction that matters, and that R120–R124 did not cross: they
narrowed what the **cost model is told**, not what the executor
**produces**. Q4's semi output is 448 bytes wide *in fact*. Telling
`costAgg` a smaller number is not the same as PG's plan carrying a
16-byte tuple, and R81's blocker is named as `DatumBytes`/**projection
pushdown** — a real narrowing of the tuple, not a cost-input estimate.

Corroborating memory, independently recorded:
`goopg_optimizer_no_attr_needed_no_ios_path` — "inside a join tree there
is no Project above the scan at all". There is nowhere to hang a
projection today.

## 4. Consequence for planning the next round

Any round targeting Q4's election or Q9's join-order **without**
projection pushdown is re-running a known-negative experiment. The
evidence for that is not speculative:

- rows-only on Q4: refuted, R71 (four forced values, no election change).
- semi-selectivity wiring on Q4: bounded at 1.28x and ruled insufficient,
  R78.
- FK evidence on Q9: refuted this session
  (`../r126-fk-persistence/step-d-recon-fk-chain-is-a-no-go.md` — all
  eight FKs declared, `match=6` unchanged).
- **bucket charge (`MapSlotBytes` 48→96): INERT — measured R129 recon**
  (`../r128-parity-over-throughput/r129-bucket-charge-recon.md`). Plan
  shapes identical, categories identical, match 6/22 unchanged. Q14 does
  hold MATCH now (the historical blocker is gone), but there is no parity
  reason to land it; its value is memory accuracy and belongs to
  `minimize_datum`.
- cost-input narrowing corpus-wide: **NOT parity-neutral — corrected by
  R128.** The chain buys TPC-H `join-method` 10→9 and `scan-type` 9→8; it
  was default-OFF; **R128 promoted it to default ON** (commit
  `9dc6ee6e5`), taking the decision R124 §6 handed forward. What it does not do is flip any query to MATCH, so it does
  not by itself unblock Q4 or Q9.

**The honest next round is the projection-pushdown / DatumBytes
capability itself** — infrastructure, not a parity tweak, with no parity
prediction attached to its first slice. It is large, it is the shared
dependency of the two closest queries, and every cheaper lever aimed at
those two has now been measured and rejected.

The alternative is to accept that TPC-H parity is capped near 6–7/22
until that capability exists, and to redirect effort at the categories
that do not depend on it. Whoever schedules next should choose between
those two deliberately, rather than by picking the next plausible-looking
query.

## 5. Standing hazard for this directory

125 round directories. Three hypotheses were refuted by measurement in
one session, and a fourth had been refuted **in writing, on disk, six
rounds earlier** and was still scoped. Before scoping round N: `ls` the
directory, and grep the prior rounds for both the target query and the
target mechanism. Recent-commit context is not sufficient here.
