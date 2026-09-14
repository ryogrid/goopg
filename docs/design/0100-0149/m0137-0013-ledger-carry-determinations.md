# M0137-0013 — close or delete the ten-plus-round ledger carries

Status: accepted

## What this is

`03-process-retrospective.md` P7's "Ledger carry" finding names ten items that
"appear verbatim across ten-plus rounds" of `TODO.md`:

> R51 items 2–3, R52 §4.2, R54 follow-ups, "#6", R61-#4, the NLI staleness
> comment, R55 §3 tie-break, F3 procost, the Q8 gap, AGG_MIXED.

and records that R67 §3 legislated against the pattern ("the next round
touching aggregation-strategy must re-derive #6's deferral from its own
numbers") and "the carry continued." This task is filing/determination only
(per the task line and per M0137-0009's split-out rationale): for each named
item, re-open the `TODO.md` entries that carry it forward, determine whether a
later round already discharged it, and either (a) close it with a citation,
(b) file a proper `.ralph/deferral_ledger.md` row, or (c) delete it as
stale/superseded. No speculative code change is made here.

## Method

Traced each item through `TODO.md`'s round log (R51 → R130) and cross-checked
against `METHODOLOGY3/02-open-problems.md`, which — as of the 2026-09-14
stocktake — already gives most of these items a permanent, numbered home (an
`N`/`O`/`B` identifier) rather than leaving them as recurring prose. Where an
item's current disposition could not be confirmed from the documents alone,
checked the item against the tree at HEAD directly (`internal/optimizer/*.go`).

## Determinations

**1. R51 items 2–3** (`r51-implied-equalities-seam/SLICE.md` §3 "Out of
scope", three unnumbered bullets — the costing half is tracked separately, not
part of this ledger-carry; items 2–3 are the C-04a nullable-side
guard-interaction assertion and the `GOOPG_PGSHAPED_DP_TRACE` Q9
re-verification):
- Nullable-side assertion (`TODO.md:3180` names it "R51 item 3"): **(a)
  close the carry.** Still genuinely open engineering work — a comment-only
  guarantee with no assertion test — but it already has ONE permanent,
  non-duplicative home: `02-open-problems.md` N50 ("the nullable-side
  closure-input guarantee lives only in comments with no assertion test").
  No new filing needed; future rounds should cite N50, not re-describe it.
- `GOOPG_PGSHAPED_DP_TRACE` re-verification that `{part}|{partsupp}` is
  enumerated on Q9: **(c) delete as superseded.** R53 Step-0
  (`TODO.md:3176-3191`) built `DPTRACE cost` instrumentation and empirically
  confirmed Q9's DP levels share through L3 (including the `{ps,p}`
  partition) as part of the join-order-costing programme; R53 Slice-1 through
  R70 built extensive DPTRACE-based verification on top of it. The narrow
  original ask is moot — superseded by broader, more rigorous machinery that
  answers the same question and more.

**2. R52 §4.2** (`TODO.md:3227`: "H-Q5's Gather and DS-Q84's Gather Merge
died under R51-new shapes — both NEW plans entirely serial"): **(a) close,
discharged.** R54's Step-0 → Step-1 → Step-2 → FIX-SEED → Redesign chain
(`TODO.md:3225-3363`) resolved this by measurement: the Redesign LANDED
2026-09-11 (`r54-parallel-admission-step0/REPORT-redesign.md`) reproduces the
failed round's numbers to the decimal (Q5 split wins by exactly the predicted
+750.20, Q9 split kept +29801.70, Q19 shape-MATCH, Q1/Q84 identical) with all
must-holds green. The residual Q7 winner-margin fragility (32.82, 0.02%,
recorded at Redesign's close) is a different, narrower, already-separately-
tracked item — not part of this closure.

**3. R54 follow-ups** (`TODO.md:3364-3366`, filed right after the Redesign
landed: Q7's remaining sort gap, Q19 range/IN-in-OR defaults, Q8
sorted-vs-split tie-break): **(a) close the grouping label** — two of the
three are discharged with citation, the third already has its own permanent
home (no new filing):
- Q7 sort gap: discharged. R55 probe A found NO-MECHANISM (`costSortRun`
  already term-identical to PG's `cost_tuplesort`); R56's structural cut
  (third no-split upper arm, `costAgg`/`gatherMergeCost`, LANDED 2026-09-11)
  closed the WELL-DEFINED-CUT structural gap, moving the margin 382→~193
  inside its predicted band; R57/R58 then re-attributed the residual as a
  SHAPE consequence (worker-count selection, 4-vs-2, itself audited FAITHFUL)
  rather than a pricing bug. Fully explained, not lost.
- Q19 range/IN-in-OR defaults: discharged. R55 implementation LANDED
  2026-09-11 (`orRangeSelectivity`/`orInListSelectivity` in
  `internal/optimizer`) lands P3 133→26, inside the predicted 24–94 band,
  Q7/Q8/Q5/Q1/Q3/Q9/Q10 plans byte-identical.
- Q8 sorted-vs-split tie-break: **not discharged**, folds into item 7 below
  (`R55 §3 tie-break`) — same item under two names, already carries a
  permanent home at N45.

**4. "#6"** (large-group aggregation-strategy/stats framing — hash-cost-at-
scale on TPC-H Q3, column-ndistinct gap on DS Q39): **(a) close the carry.**
Already given a permanent, non-duplicative identifier in
`02-open-problems.md` B4: "O9 / K-note '#6' — hash costing at large group
counts prefers GroupAgg+Sort where PG keeps HashAgg... R67 §3 legislated
against carrying this further without re-derivation; the carry continued
anyway." The engineering item itself remains open (it is the named
aggregation-strategy lever, B4/K12(B)/K24 slice 3) but it is no longer an
untracked loose end — cite O9, not the bare "#6" label.

**5. R61-#4** (`estimateAggregate` never firing on CTE-body joins — arm never
fires, "walk-stop vs never-tagged", named at `TODO.md:3574`): **(a) close the
carry.** Already named verbatim, with its own permanent home, at
`02-open-problems.md` N45: "`estimateAggregate` never firing on CTE-body
joins (R61-#4)". Still open, already tracked — no new filing needed.

**6. The NLI staleness comment** (`estimateNLIndexJoin` staleness comment,
named at `TODO.md:3573` as an R61 follow-up): **(b) new deferral_ledger row —
not discharged, and NOT already tracked anywhere in `02-open-problems.md`.**
This is the one item in the list that risked being silently dropped rather
than merely re-worded across rounds. Confirmed live at HEAD by direct code
read: `estimateNLIndexJoin` (`internal/optimizer/cardinality.go:235-239`)
returns `EstimateRows(j.Outer)` unconditionally for every
`NestedLoopIndexJoin.Type`, including `JoinTypeSemi`/`JoinTypeAnti` — it never
calls `semiJoinMatchFraction`. The sibling estimator for the SAME join
semantics on a *regular* `*Join` node, `estimateJoin`
(`internal/optimizer/cardinality.go:608-625`), DOES apply
`semiJoinMatchFraction(j, r) * joinResidualSelectivity(j)` for
`JoinTypeSemi`/`JoinTypeAnti`. This is the project's recurring "sibling code
paths must stay in sync" bug class (see `[[pattern_sibling_paths_must_agree]]`
in the auto-memory): a SEMI/ANTI join planned as a `NestedLoopIndexJoin`
silently gets the *inner* (unweighted) row estimate instead of the outer's
match-fraction-weighted one. Filed as ledger row
`m0137-0013-nli-semi-anti-match-fraction-gap` (below).

**7. R55 §3 tie-break** (calibration for the Q8 sorted-vs-split ~0.10% margin
— "a 161-cost tie-break over an estimator-justified seed, owned by neither
arm nor estimator", `TODO.md:3353-3355`, ordered after the R56 `costAgg`
audit at `TODO.md:3375-3376,3431`): **(a) close the carry.** Already named
verbatim at `02-open-problems.md` N45: "R55 §3 tie-break calibration". Still
open (never scoped past R56's audit, which found the *cause* — F3 procost,
item 8 — owns zero of the margin, leaving the tie-break itself unassigned) —
already tracked, no new filing needed.

**8. F3 procost** (`costAgg` trans/final symmetric-cost finding from R56
scope, "owns zero of the margin", `TODO.md:3396-3397,3432`): **(a) close the
carry.** Already named verbatim at `02-open-problems.md` N45: "F3 procost".
This item is itself a NO-MECHANISM finding (R56 probe C(i) proved the
`costAgg` trans/final terms are PG-identical and not the cause of the Q8
margin) — what remains open is the bookkeeping debt of the finding still
being carried as an active question in prose rather than closed as "checked,
ruled out." N45 already carries it correctly as ruled-out-but-unretired;
no new filing needed.

**9. The Q8 gap** (`TODO.md:3432`: "Q8 +25k gap", distinct from the R54-era
"Q8's 161-cost / 1.18× harvest" numbers which the Redesign round already
judged "justified" — see item 3 above): **(a) close the carry.** Already
named at `02-open-problems.md` N45: "Q8's ~25k cost margin". Still open — no
new filing needed.

**10. AGG_MIXED** (Hash-vs-GroupAgg strategy preference at ~6k rows / 2
groups, `TODO.md:3447,3542`): **(a) close the carry.** Already named verbatim
at `02-open-problems.md` N45 ("AGG\_MIXED (Hash-vs-GroupAgg at ~6k rows / 2
groups)") and further contextualized at B4 as part of the named-but-unscoped
aggregation-strategy lever (K12(B)/K24 slice 3 — PG's
`AGGSPLIT_INITIAL_SERIAL`/`FINAL_DESERIAL`, "a multi-round **executor**
programme"). Still open, substantial — already tracked, no new filing needed.

## Summary

| item | disposition | permanent home |
|---|---|---|
| R51 item 2 (nullable-side assertion) | (a) close carry | N50 |
| R51 item 3 (DP-trace re-verification) | (c) delete, superseded | R53 Step-0 DPTRACE + successors |
| R52 §4.2 (parallel admission) | (a) close, discharged | R54 Redesign, LANDED 2026-09-11 |
| R54 follow-ups: Q7 sort gap | (a) close, discharged | R55 probe A + R56 cut, LANDED |
| R54 follow-ups: Q19 range/IN-OR | (a) close, discharged | R55 implementation, LANDED |
| R54 follow-ups: Q8 tie-break | = item 7 | N45 |
| "#6" | (a) close carry | B4/O9 |
| R61-#4 | (a) close carry | N45 |
| NLI staleness comment | (b) **new ledger row** | `.ralph/deferral_ledger.md` |
| R55 §3 tie-break | (a) close carry | N45 |
| F3 procost | (a) close carry | N45 |
| Q8 gap | (a) close carry | N45 |
| AGG_MIXED | (a) close carry | N45, B4 |

Net: of the ten named items, three were genuinely discharged by later rounds
(R52 §4.2, and two of R54's three follow-ups), one is deleted as superseded by
broader instrumentation, six already have a single permanent non-duplicative
home in `02-open-problems.md` and need no new filing, and exactly one —
the NLI SEMI/ANTI match-fraction gap — was a real, previously-unfiled gap now
recorded in the deferral ledger. This closes the "Ledger carry" bullet of
P7; the other P7 bullets (default-off cost arms, deferral without an owner,
the skipped R80 reservation, stale prose, decaying scoping facts) are out of
scope for this task — the default-off-arms bullet was already handled by
M0137-0009.

## Gates

Filing/determination only, per the task's own scope boundary — no production
code, no `TODO.md`/`rNNN-*` round-directory edits. `go build ./...` and
`go vet ./...` unaffected (no `.go` files touched other than none).
