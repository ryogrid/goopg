# Milestone 0137 — Parity measurement harness and instrument repair

**Status:** in-progress (13 of 18 tasks complete, 2026-09-15)
**Filed:** 2026-09-14 (user directive, from
`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/04-forward-plan.md`
Phase 0)
**Priority placement:** **FIRST.** The `## Current Priority` banner in
`.ralph/fix_plan.md` ranks M0137 at the head of the plan-parity milestone group
(M0137–M0143), above every pre-existing milestone. M-NIGHTLY keeps its
unconditional *filing* obligation but no longer outranks this group for
*selection*.
**Reference plan:** `.ralph/fix_plan.md` (M0137 section)
**Harness:** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
(binding; read it before selecting any task here)
**Prerequisites:** none — this milestone is the prerequisite for the other six.

## Why this is first

Every other milestone in this group is judged by measurement, and the
measurement instruments have decayed. `METHODOLOGY3/03-process-retrospective.md`
§P4 documents the damage: the K18 `$$`-tempfile trap produced **false structural
readings in four separate rounds** (R122, R123, R124, R128) and is still live;
captures are hand-stamped or unstamped, so an arm attribution "rests entirely on
filename convention"; stats-epoch drift measured at **1.31x on Q9** exceeds most
effects being claimed; and `make plan-gate` has been a standing opt-out since
R65 against a baseline never refreshed across ~125 rounds of intentional plan
change.

A campaign measured on these instruments will not be believable. K91 states the
rule this milestone exists to restore: **a harness must fail loudly, and an A/B
must prove which binary answered; where it cannot, the result is not evidence.**

M0137 also builds the *navigation* layer. The previous phase left 126 round
directories with no index, and `METHODOLOGY3/03` §P2 shows what that costs: R127
was withdrawn on nine findings, three fatal, **every one refuted by a document
already on disk in this directory**, because its author opened none of the eight
prior rounds on the same query. A written warning already existed and did not
work. The fix is mechanical, not exhortation.

## Per-task discipline (READ FIRST — binding)

1. **Design note when the task is selected.** Write
   `docs/design/<task-id>-NNNN-short-slug.md` (status `draft` -> `accepted`) at
   task start, and index it in `docs/design/README.md` in the same commit. This
   is the M0134 precedent and it **overrides** `docs/milestones/README.md`
   §"Workflow Per Milestone" step 2 ("write the design docs first") for this
   milestone group — there is no up-front "Required Design Docs" list here.
2. **No new round directories.** Do not create `rNNN-*` directories and do not
   follow the previous phase's SCOPE -> review -> REPORT cadence; that was an
   interactive-session convention. Raw artefacts go under `analysis/m0137/`.
3. **An instrument change must prove itself.** Every task that touches a capture
   or gate lands with a test or a recorded before/after demonstrating the defect
   it closes — the whole class of bug being fixed here is one where *the null
   result and the broken result are indistinguishable* (K91).

## Completion rule for this milestone group (added 2026-09-15 — binding)

**A deferral needs two artefacts, not one.** A ledger row records *what* was
deferred; a `.ralph/fix_plan.md` `[ ]` task records *who owns it next*. Closing
a task with only a ledger row leaves the mechanism an orphan — the
2026-09-15 review found **eight mechanisms** in exactly that state, with no
executable task anywhere.

So, for every M0137–M0143 task:

- Deferring any part of the task requires **(a)** a `.ralph/deferral_ledger.md`
  row with a concrete resume point **and (b)** an unchecked task in
  `.ralph/fix_plan.md` under the milestone that will finish it. Both, or the
  task is not complete.
- Where a Definition of Done below says "*or* its absence is a filed ledger
  row", read it as "**and** a filed follow-up task". The earlier wording made
  "write a ledger row" a legitimate way to close an implementation task; it is
  not.
- If the follow-up genuinely belongs to no milestone in this group, say so
  explicitly in the ledger row's `why` column and name where it does belong.

## Scope

Eighteen tasks, grouped:

**Capture pipeline (0001–0003).** The parity capture scripts currently exist as
two divergent copies inside round directories
(`plan_parity_fix_take2/methodology/` and `.../r2-instrument/`), both carrying
the K18 `$$` trap. Promote one canonical copy into `scripts/`, fix the trap at
source with a regression test, machine-stamp every capture, and write down the
canonical baseline-capture procedure — including the fact that **TPC-H
baselines come from `estimate-audit -plan-only`, not `capture-tpch.sh`**, whose
fresh-session-per-query behaviour captures TPC-H plans on *empty* stats
(`.ralph`-adjacent record: `plan_parity_fix_take2/TODO.md:4861-4875`), and that
`-serial` defaults **true** (`cmd/estimate-audit/main.go:293`), which is why
TPC-H `parallelism` reads 0 — measured *out*, not solved.

**Reference and gate integrity (0004–0006).** Reconcile the unreconciled TPC-DS
`match=2` vs `match=1` (same script family; the difference is the **reference
and session GUCs**, live PG `:65438` vs the committed `bench/tpcds/plans-pg`
fixture), rule on that fixture's standing the way K9 rules on the TPC-H one,
re-baseline `make plan-gate` so it is a live signal again, and make the
stats-epoch declaration a checked step rather than a remembered rule.

**Navigation and debt (0007–0009).** Give each lane a private clone and port
(shared-resource contention on `:65433` is the stated reason for most deferred
gates); build the per-query and per-mechanism indexes over the round corpus and
make citing them a scope gate; retire the flag and ledger debt R124 §7 already
resolved to *delete*.

**Correctness channel (0010).** Two additions justified by bugs that actually
shipped: a qual-placement census (R56's Q78 lost three `Filter:` lines with
checksums still passing) and a duplicate-sensitive values check (R83's
Limit-below-Unique bug was masked because `78 < 100`).

**Review carry-overs (0013–0018).** Filed 2026-09-15 by the loop review in
`tmp/METHODLOGY3_RALPH_CHECK0915/`, after 36 completed tasks left the goal metric
unmoved. 0013 closed the ten-plus-round ledger carries. 0014 automates the
seam-decline census the report requirements ask for; 0015 is the one-line
`PlanCost` embed that closes C3/K63; 0016 is O15's `*Gather` arm; 0017 re-captures
TPC-H without `-serial` so `parallelism` becomes scoreable at all; 0018 re-scores
`make ea-ratchet` and settles whether it belongs in this group's gate table.

**Instrument-integrity carry-overs (0011–0012).** C3/K63 is an *instrument*
defect and belongs here: goopg's EXPLAIN reports a scan cost the planner did not
use (4.5x on Q12), which corrupts `plan-gate MODE=semantic-cost` and every
estimate audit, and R76's second seam on Q22 (16,666 displayed vs 18,200
stamped) was never diagnosed. Separately, four blockers the previous phase left
unowned get a ledger row each so they stop being invisible — **B6** (no Memoize
on the NL probe path R59 repriced; Q72 4s -> 320s), **B8**
(`indexProbeCostMultiplier = 2.0`, parity vs wall-clock), **B10** (`corr = 0`
fallback + R30's synthesised index geometry) and **O15** (`*Gather` crossing
excluded from `pushConjunctTraced`, the same class as R56's Q78 defect).

## Definition of Done

- One canonical capture pipeline in `scripts/`; the round-directory copies no
  longer used by any procedure; two consecutive captures of an unchanged binary
  diff **empty** (test-enforced).
- Every capture artefact carries a machine-written stamp: binary path, inode,
  serving-PID `/proc/<pid>/exe` verification, flag arm, pinned GUCs, stats epoch.
- The TPC-DS reference question has one documented answer, and both numbers stop
  being quoted side by side.
- `make plan-gate` passes or its residue is adjudicated and explicitly carried;
  it is no longer a standing opt-out.
- `INDEX-by-query.md` and `INDEX-by-mechanism.md` exist and are cited by the
  first task scoped after them.
- `GOOPG_HASHAGG_WIDTH_CURRENCY` is deleted; the default-off arm cap is stated
  in the harness (both DONE, M0137-0009); the ten-plus-round ledger carries are
  each closed or deleted (split out to M0137-0013, **completed 2026-09-15**).
- The qual-placement census and the duplicate-sensitive values check run as gate
  artefacts and are named in `AGENT.md`'s harness section.
- The display-vs-consumed cost seam (C3/K63) is root-caused **and fixed**, so
  cost-bearing artefacts can be trusted. M0137-0011 root-caused it (`optimizer.Filter`
  does not embed `PlanCost`, so `stampPlanCost` silently drops it) and filed the
  row; the one-line fix is **M0137-0015**.
- B6, B8, B10 and O15 each carry a ledger row **and** a follow-up task. O15's
  own gate (M0137-0010's qual-placement census) is already satisfied, so it is
  actionable now — it is **M0137-0016**. B6/B8/B10 are larger and are carried as
  named prerequisite reading for M0142 rather than as M0137 tasks.
