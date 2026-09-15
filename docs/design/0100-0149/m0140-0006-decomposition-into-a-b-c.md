# M0140-0006 — decomposition into 0006a/0006b/0006c

**Status:** accepted
**Milestone:** M0140 (TPC-DS parallelism), plan-parity group
**Harness:** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Task:** `.ralph/fix_plan.md` M0140-0006
**Outcome:** planning/filing only, no production change. Splits the oversized
parent task into three loop-sized sub-tasks so a future loop can pick up a
bounded slice instead of re-deriving the scope from the M0140-0004 recon.

## Why this loop didn't implement

M0140-0006 was filed (2026-09-15) explicitly to correct M0140-0004's
recon-only closure — the task title reads "the implementation" on purpose.
Re-reading the recon (`m0140-0004-partial-append-producer-recon-and-defer.md`)
and re-grounding it against current line numbers this loop
(`internal/optimizer/planner.go:1106-1153` `planSegment`/`applySetOp`,
`internal/optimizer/windowsetoppaths.go:258-293` `createSetOpPaths`,
confirmed unchanged in shape) reconfirms the recon's own sizing claim: landing
a real partial-Append producer needs three structurally independent pieces,
each "comparable in size to an entire prior C-19-series slice". Attempting
any one of them blind, inside a single loop, against a shared fold that is
already dense with precedence-correctness comments (`M0125-0016`'s
left-associative INTERSECT/UNION/EXCEPT climb sits in the exact function
(`planSegment`'s enclosing fold) piece (1) below must touch) is the kind of
one-task-per-loop violation `.ralph/PROMPT.md` warns against, and a wrong
edit there risks a silent row-count regression across every SetOp query in
both TPC-H and TPC-DS, not just Q5/Q76 (Hard-won Rule #1).

## The split

1. **M0140-0006a — expose SetOp-branch `PartialPathlist`.** Give each UNION
   ALL branch (`planner.go:1106` `planSegment`, called from the
   `applySetOp`/`foldSetOpRange` fold at `planner.go:1120-1208`) a route to
   retain a `RelOptInfo` with a `PartialPathlist` instead of collapsing
   straight to a finished `Node` via `planSelectWithSettings`
   (`planner.go:1114`), so `createSetOpPaths`
   (`windowsetoppaths.go:258-293`) has something other than
   `seedPathForNode`'s opaque-`Node` wrap (`windowsetoppaths.go:296-305`
   in the recon's line numbers, now `windowsetoppaths.go:298`) to read a
   partial candidate from. Purely a plumbing change: no new cost producer,
   no new executor node — the new field can go unread until 0006b exists.
   **Acceptance:** TPC-H (`match=8`) and TPC-DS (`match=2`) both
   byte-identical before/after (`shape-delta.sh shape-changed=0`), because
   nothing should select a partial path yet.
2. **M0140-0006b — the partial-Append cost producer.** The
   `addPartialHashJoinPath` counterpart (`joinpathsparallel.go:82`'s shape):
   seed `setOpRel.PartialPathlist` from 0006a's branch partial paths, priced
   on PG's `cost_append` partial-path arithmetic
   (`postgres/src/backend/optimizer/path/costsize.c:2250` streaming-UNION-ALL
   comment already cited by `windowsetoppaths.go:337-339`'s serial arm),
   gated behind `gatherPathsMode` (`gatherpaths.go`) the same way K80 is.
   Depends on 0006a landing first. **Must not land ahead of 0006c** — see
   below.
3. **M0140-0006c — executor claim-set for `setOp` under `Gather`.** A
   correctness prerequisite, not an optimization: `gatherOp`
   (`operators_gather.go`) has each worker build its own full copy of the
   child subtree, safe for a base scan only because `parallel_scan.go`'s
   `ParallelGroup`/`claimed()`/`claimLeaf`/`parallelClaimSet` hand each
   worker a distinct block range. `setOp` (`operators_setop.go:32`
   `newSetOp`) has no equivalent claim logic — `Open` streams both children
   to completion unconditionally. Landing 0006b without this first would
   make every parallel worker replay both entire UNION ALL branches,
   silently duplicating every row the moment `gatherPathsMode` picks the new
   partial-Append candidate — a wrong-answer defect, not a missed
   optimization. **Must land before or with 0006b's flag going live**
   (0006b may be *built* first since it is gated off, but must not be
   promoted default-on, nor exercised by any gate that could pick it, until
   0006c exists).

`generateUsefulGatherPaths` (the existing K80 consumer, `considerparallel.go`)
reading the new `PartialPathlist` is expected free by construction once
0006a/0006b exist (per the original recon item 3) — verify this as part of
0006b's own acceptance rather than filing a fourth sub-task; if it turns out
not to be free, 0006b's own recon should say so and re-split.

## Disposition

`.ralph/fix_plan.md` M0140-0006 is closed on this basis: the task was to
either land the producer or record why it can't be landed in one loop, with a
concrete, loop-sized resume path — this doc does the latter, sharper than
M0140-0004's recon-only closure it was filed to correct, because it leaves
three directly actionable tasks instead of one oversized one. No production
code changed; no test pins moved; TPC-DS values/match floor unaffected.
Ledger row appended (task-id `m0140-0006`), pointing at 0006a/0006b/0006c
rather than re-deriving M0140-0004's resume point.
