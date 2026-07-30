# 0125-0005 — Flipping the `GOOPG_RELSIZE_FALLBACK` default

Status: draft
Date: 2026-07-28
Milestone: M0125-0005 (§13.5 action 2, rider; specified as "commit B" in `0125-0003-…` §D5.3)

## Problem

`0125-0003` lands the relation-size fallback **behind a flag that defaults off**, so nothing
changes for any user. That is deliberate — but a flag nobody flips is a fix nobody gets, and
§7.3's RC-5 deferral names "after §7.1's flag is defaulted on" as its reopen criterion. Without
a task holding that flip, the criterion points at a commit no milestone owns.

This task exists to make the flip a **decision with evidence**, not a side effect.

## Why it cannot ride along with the implementation

The flip is the moment goopg's default planner behaviour changes on every un-ANALYZEd server.
Three specific reasons it deserves its own commit and its own gate:

1. **The measured prior is mixed, not favourable.** The nearest analogue — enabling statistics
   in round 4 — fixed TPC-H Q5 **22.8×** and regressed **Q22 128×, Q4 79×, Q8 53×, Q2 26×,
   Q12 4.4×**, taking the serial stream **1162 → 1307 s**. One large win, four to five large
   losses.
2. **The project's own correctness gate is in the blast radius.** `scripts/tpch-spotcheck.sh`
   runs **S-cold** — it issues no ANALYZE, and could not, since `ANALYZE <table>` in database
   `tpch` errors (ledger `bench-reorg ANALYZE-scope`). That is exactly the state this flag
   changes, and **Q12 is one of the regressed cells**. A careless flip converts the gate every
   planner commit must pass into a slow gate, or an OOM one — `CLAUDE.md` records Q21 drawing a
   host-level OOM at `GOMEMLIMIT=18GiB`.
3. **Precedent.** `costDrivenJoinOrder` produced real 18.8× and 4.1× wins and still ships off by
   default (round-5 §6), because two of its regressions do not complete. The bar for flipping a
   planner default in this repository is evidence, not plausibility.

## Required evidence

The flip lands only when all of the following exist:

1. **`0125-0003`'s C1→C2 table for every stage** (probe-side, DP seed, `baseRows`), showing the
   losses are bounded, with each of round-4's five regressed queries explicitly checked against
   the pre-registered watch list.
2. **A TPC-DS SF=1 sweep at both flag states**, uniform budget, following `0124-0001`'s protocol
   — the win must be demonstrated at the scale the timeout class was measured at, not only at
   SF0.5.
3. **`tpch-spotcheck.sh` re-measured in both flag states: wall clock *and* peak RSS.** A
   regression here blocks the flip regardless of the TPC-DS win, because it degrades every
   future commit's gate.
4. **A written decision** in `analysis/`, naming what was traded for what. If the answer is "the
   TPC-DS timeout class improves and TPC-H S-cold regresses", that is a legitimate outcome to
   *record and not flip* — a documented refusal is a successful completion of this task.

## Design

The change itself is one line plus its test: the flag's default becomes on, and
`TestTableRowsFallbackDoesNotFireWhenAnalyzed` (from `0125-0003` §D3) must still pass unchanged —
the invariant that the fallback never fires in an ANALYZEd state is what keeps the flip from
touching any ANALYZEd deployment.

Keep the environment variable working in both directions, so the flip is revertible at runtime
without a rebuild: `GOOPG_RELSIZE_FALLBACK=0` must disable it after the default moves.

## Consequences to schedule, not to absorb silently

- **§7.3 RC-5** (`shouldAttachBeforeMHJ`'s `SmallDimension` gate) becomes reopenable — its
  criterion is "after M0125-0002 **and** this task". Update its ledger row rather than leaving
  the criterion pointing at nothing.
- **Phase 6.2** (greedy join order for `n > 12`) becomes meaningful for the first time: the
  design's own argument is that it is pointless without real cardinalities. Update that ledger
  row too.
- Any benchmark number in `analysis/` taken before the flip is now in a different regime.
  Say so in the report rather than letting future readers compare across it.

## Non-goals

Re-opening RC-5 or phase 6.2 in this task. It flips one default and updates the ledger rows
whose criteria it satisfies.

## Gate

Units; `scripts/tpch-spotcheck.sh` in the new default state; `make plan-diff
LABEL=tpcds-round2-head` with the hunks enumerated — a default flip is expected to move plans,
and every moved plan must be named; the SF0.5 gate with checksums; plus the four evidence items
above, which are acceptance criteria rather than gates.
