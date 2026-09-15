# Milestone 0141 — Upper-planner ordering contest

**Status:** planned — **scoping-gated**
**Filed:** 2026-09-14 (user directive, from
`METHODOLOGY3/04-forward-plan.md` Phase 1 Campaign B)
**Priority placement:** **S0 has landed (2026-09-15), and the gate below is
satisfied.** `M0141-S2a-fix` is now joint TOP PRIORITY for the whole group with
`M0139-0007` — see the `## Current Priority` banner in `.ralph/fix_plan.md`. The
sentence this line used to carry ("no implementation task exists until S0
lands") is historical and no longer describes the milestone.
**Reference plan:** `.ralph/fix_plan.md` (M0141 section)
**Harness:** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Prerequisites:** M0137; and **M0141-S0**, this milestone's own mandatory
scoping recon.

## Why this milestone is gated instead of scheduled

This is the named lever for `aggregation-strategy` (TPC-DS 69, TPC-H 10) and,
through the same mechanism, `sort-strategy` (76 / 9) — K24 calls it the largest
single item in the workstream. It is also the item the record refuses to size:
`METHODOLOGY3/02` §B4 calls it *"named and unscoped"*, K96 says the shape PG
picks is *"unreachable by construction"*, and K97 records R45's fix as
*"rejected on review as architecturally impossible"* and names the real item as
PG's `AGGSPLIT_INITIAL_SERIAL` / `AGGSPLIT_FINAL_DESERIAL` — **a multi-round
executor programme of unstated size**.

`METHODOLOGY3/04` §3 is explicit that this is **the weakest link in the owner's
(c) "split the goal" decision**: (c) holds that TPC-DS does not depend on the
width programme, which is true — but this campaign depends on a *different*
executor programme that nobody has priced. S0 exists to price it.

## S0 — the mandatory scoping recon

S0 is a **recon task** in the harness's sense: measurement and design note only,
**no production change**. Its deliverable is the thing this milestone currently
lacks:

1. The size of the `AGGSPLIT_INITIAL_SERIAL` / `AGGSPLIT_FINAL_DESERIAL`
   programme in goopg terms — which executor surfaces change, how many sites,
   what the row-borne partial-state representation costs.
2. A slice list (S1…Sn), filed **into this milestone's `fix_plan.md` section**
   by S0 itself.
3. An entry gate: the condition under which S1 may be selected.

S0 landed on 2026-09-15 and filed S1–S6. Two further slices were added by the
loop review the same day and are **not** S0's output, so record where they came
from:

- **`M0141-S2a-fix`** — the costing-order half of the width defect: narrowing
  information reaches the cost model too late, or in the wrong currency, to
  affect plan selection. It is joint TOP PRIORITY with `M0139-0007`, which
  establishes the **absorption principle** it applies. Read `AGENT.md`
  §"B2 — same statistics, same plan; absorb what cannot be made identical"
  **before** scoping it: the two rules there (derive before you measure; the
  substituted quantity must be a port of a named expression under `./postgres/`)
  are what separate this work from tuning, and a slice that cannot satisfy them
  is blocked rather than absorbed.
- **`M0141-S7` — Incremental Sort.** Owner-filed, not S0-filed: PG emits it in
  14 of the 99 TPC-DS reference plans and goopg has no implementation, so those
  queries cannot MATCH whatever the costing does. The entry gate in
  §"Per-task discipline" below governs S0's slices; S7 is independent of it and
  may be selected on its own.

## Known constraints — start from these, do not rediscover them

- **K96**: `Finalize GroupAggregate` is unreachable by construction —
  `rebuildWithGather` has mutually exclusive arms and `splitAggregate`
  hardcodes `NewGather` while copying the original strategy (goopg 0/0 vs PG
  5/18 and 9/85).
- **K97**: R45 was rejected because goopg's Partial aggregate emits **zero
  rows** (side channel, `o.rows = nil`), so a Sort between Partial and Gather
  would sort an empty stream and Q1 would return unordered rows.
- **K97's trap**: `aggregateOp` already sorts for determinism — **do not
  mistake incidental ordering for a pathkey.**
- **K24**: the upper planner receives a *finished `Node`*, not the join rel's
  paths. K23 needs `PartialPathlist`; K12(B) needs `Pathkeys`. **Same root
  cause** — K24 warns explicitly that they must not be scheduled as independent
  rounds.
- **F15 / R120 §5**: PG's `GroupAggregate` preference is **pathkey-driven, not
  spill-driven**. The success criterion is ordering delivery, not spill
  accounting — R120 already proved the spill route is net-negative (TPC-DS
  `aggregation-strategy` 69->71, and TPC-H lost a match).

## Per-task discipline (READ FIRST — binding)

1. **Design note when the task is selected**, indexed in
   `docs/design/README.md` in the same commit. Overrides
   `docs/milestones/README.md` §"Workflow Per Milestone" step 2 for this group.
2. **S0 is measurement-only.** A production diff in S0's commit is a scope
   violation.
3. **No S0-filed slice (S1–S6) may be selected before S0's entry gate is
   satisfied.** S0 landed 2026-09-15 and the gate is satisfied. `S2a-fix` and
   `S7` are not S0-filed and are not governed by that gate.

## Definition of Done

- S0 has produced a size, a slice list filed into `fix_plan.md`, and an entry
  gate — or a recorded no-go stating why the programme should not be attempted,
  which is an acceptable outcome.
- If slices proceed: `aggregation-strategy` and `sort-strategy` fall **together**
  on TPC-DS (they are one mechanism); values gates all-zero; the non-regression
  floor holds.
