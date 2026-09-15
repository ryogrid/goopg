# Milestone 0140 — TPC-DS parallelism

**Status:** planned
**Filed:** 2026-09-14 (user directive, from
`METHODOLOGY3/04-forward-plan.md` Phase 1 Campaign A)
**Priority placement:** in the plan-parity group alongside M0138/M0139.
**Independent of M0139** — per the owner's **(c) = Go**, TPC-DS's dominant
blockers do not gate on the width programme, so this milestone proceeds
regardless of M0139's state. See the `## Current Priority` banner.
**Reference plan:** `.ralph/fix_plan.md` (M0140 section)
**Harness:** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Prerequisites:** M0137.

## Why this campaign, and why it leads Phase 1

`parallelism` blocks **87 of 99** TPC-DS queries. K37 measured the shape: PG
plans 66 of 99 in parallel where goopg plans serial — **zero the other way** —
and `Parallel Hash` appears 314 times in PG's plans and **0** in goopg's. It
feeds `join-method`, `scan-type`, `aggregation-strategy` and `sort-strategy`
simultaneously.

It is also the only Phase-1 item that is already concrete, sequenced and
evidence-backed — which is why `METHODOLOGY3/04` §3 puts it ahead of the
ordering contest (M0141), whose size nobody has established.

The lever is narrower than it looks, because three corrections already narrowed
it: **K80** — `Parallel Hash Join` needs no new `parallel_hash` work,
`addPartialHashJoinPath` already sets `ParallelAware: true`; it was dead solely
because `GOOPG_GATHER_PATHS` defaulted OFF — **M0140-0003 landed the flip
default-`all`**, so this producer is live at HEAD. **K82** — TPC-H Q14 is
byte-identical under the flip, so the two tracks are independent. **K92** —
relabelling goopg's leader-prebuild as `Parallel Hash` would be a
*misdescription*, i.e. the arbitrary forcing the goal forbids.

## Per-task discipline (READ FIRST — binding)

1. **Design note when the task is selected**, indexed in
   `docs/design/README.md` in the same commit. Overrides
   `docs/milestones/README.md` §"Workflow Per Milestone" step 2 for this group.
2. **Re-measure, never inherit.** The "5 failing tests at HEAD, four needing
   adjudication" list is **R43-era, ~87 rounds stale**, and at least one member
   (`TestSlice3LiveQ9ShapeDerivation`) was re-baselined by R51 nine rounds
   later. The ledger's own adjacent note binds: *"re-measure before relying on
   any prior round's figures."*
3. **Pre-register no match flip** on the flip task. R43 rev 3 measured TPC-H
   `parallelism` 18->16 under the flip with **no new match**; the success
   criterion is the category metric.
4. **A test can be wrong.** For whatever genuinely fails, R14's precedent
   applies — ask the PG oracle before changing planner behaviour to satisfy a
   pin.
5. **Do not re-open the forced-order Q96 line** without first re-establishing
   which planning route the query takes (`METHODOLOGY3/02` §B11): forced
   `join_collapse_limit=1` forms measure the legacy/prebuilt constructor, not
   the path search, which already voided R106's and R108's attribution. Note
   also §B5's terminal state — the inputs PG's final hash cost needs are
   **unobservable**, and the only oracle route crashed PG (R99).

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

- Re-measure the failing set under `GOOPG_GATHER_PATHS`, then adjudicate what
  genuinely fails against PG 18.3.
- Land the flip judged on the category metric. Measured effect at `all`:
  Gather 42->104, `Parallel Hash` 0->167 — *and parity did not improve* (K38),
  so the flip is worth landing on categories, not on matches.
- Partial-Append producer (K43): PG uses Parallel Append in the TPC-DS reference
  plans; goopg emits it **nowhere** (0 of 99 plans, counted 2026-09-15). The
  earlier "only Q5 and Q76 miss" reading was corrected by M0140-0004's own recon
  (`m0140-0004-partial-append-producer-recon-and-defer.md:35-40`): Q2/Q14/Q71
  also emit a plain serial `Append` and merely carry a compensating `Gather`
  elsewhere. The producer is **M0140-0006**.
- Record, do not attempt, the two items that are out of reach: **Q14's third
  category** (K92 — needs PG's real partial-inner execution model, *"NOT
  cheap"*) and the **non-planner floor** (K14/K15 heap density; K41's still
  unexplained dimension-table `relpages` divergence, `customer` 1,979 vs 2,872
  and `item` 716 vs 1,284 — no planner change can close these).

A structural fact to carry rather than rediscover: **K20** — `GOOPG_GATHER_PATHS`
gates *partial paths only, not parallelism*. The post-pass produces a Gather
regardless, so **there is no setting that yields a serial plan**; R13 named the
possible fix as a design question, not an edit.

## Definition of Done

- The failing set under the flip is re-measured at HEAD and each member
  adjudicated against PG — with the R43-era list explicitly superseded.
- The flip is landed (or a recorded no-go with its measurement), judged on
  category movement with the pre-registered "no match flip" scored honestly.
- A partial-Append producer exists, **or** its absence is a filed ledger row
  with a resume point **and** an unchecked follow-up task in
  `.ralph/fix_plan.md`. (Both artefacts — see the completion rule above.
  M0140-0004 filed the row but no task, which is how the producer became an
  orphan; the task now exists as **M0140-0006**.)
- Q14's third category and the non-planner floor each carry a ledger row naming
  the mechanism and what would unblock them — neither is silently dropped.
  **K92 and K14/K15 are deliberately left without a follow-up task in this
  group**: K92 needs PG's real partial-inner execution model, and the heap-density
  floor is a storage-density input no planner change can correct. The ledger row's
  `why` column must say so, and must name where the work belongs instead — which
  is what the group's completion rule (`AGENT.md`, "Completion rule for this
  group") requires of any deferral that has no owner here. **K41 is the
  exception**: its dimension-table `relpages` divergence is an on-disk defect
  with a concrete probe, so it *does* have an owner — **M0143-0007**, under the
  engine carry-overs rather than under a planner milestone.
- TPC-DS values sweep all-zero; the non-regression floor (TPC-DS match >= 2)
  holds.
