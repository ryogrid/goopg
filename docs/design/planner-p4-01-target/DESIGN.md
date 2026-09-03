# P4-01 Design — PathTarget in slices, projection as path property

Blocking: executor EX1-04 (ledger `take3-EX1-04-blocked`). Status:
design for review. The P4-01b failure binds the method: never mutate a
leaf's emitted schema without moving the coordinate space with it
(Q2/Q5 0 rows, Q18 right-count-wrong-tuples — values gate caught it,
counts would not have). Every slice below is values-gated.

## Slice 1 — Target computation, no plan mutation (this item)

- Add a `Target` payload on `Path` for SCAN paths only
  (`PathSeqScan`/`PathIndexScan`): the ordered emitted-column list
  computed from `NeededCols` at path-creation time, extending the
  landed `NCols/AvgVarBytes` pair toward a column list. NEVER
  applied: no `createPlan` change, no cost change — behaviour-neutral
  by construction (modulo allocator noise: one small slice header per
  path). Must-avoid list: `comparePaths` dims (cost/pathkeys/
  parallel/outer — Target stays out), cost readers, DPPATH format,
  any Path equality/golden (none exists — verified).
- Assert-only consumers: debug/test check that `target ⊆
  rel.Output()` and join keys ⊆ target. NO EXPLAIN reporting in
  this slice (EXPLAIN text is PP diff input — a new annotation would
  break `changed=0`; reporting moves to Slice 2+; a trace channel
  read is allowed since `tracePath` prints kind/rows/costs only).
  Gate: `changed=0` both suites (PP) + `-digest`/`-diff`
  values-identical. A datum that only observes can only fail its
  assertions, never a query.
- Ordering contract (for Slice 2): the scan Target is stored in
  EMITTED-SCHEMA order, so `neededKeepSet`-style ascending checks
  pass without guard loosening (loosening = permutation
  wrong-answer class, P4-01b lesson 2; never firing = P4-01b-dormant
  recurrence, lesson 1).

## Slice 2 — Apply at the proven-safe site (after Slice 1 pins)

- The hash build side via the existing `narrowBuildInput`/`Project`
  mechanism (`narrowoutput.go` — row and schema narrow together,
  the rev-7 proven-safe shape), driven by the Slice-1 target instead
  of `NeededCols`-by-name; costing through the landed
  `pathNCols`/`pathAvgVarBytes` readers. Gate in MODEL currency:
  `NBatch` 2→1 (P4-A §8 table); runtime `Batches:` recorded, not
  gated — landed step-4 measured 8→4 @64MB (48 B/column Datum is
  co-dominant, out of scope), so 8→1 as a runtime gate is already
  falsified. Width ≈100 not 6, DPPATH join.hash below 754717,
  values both suites + timing, both-suites PP (widths-only, zero
  structural), regress suite, three-budget (64/4/512 MB) value gate.
- Dependency precision (for EX1-04): Slice 1 unblocks NOTHING at
  execution (assert-only). EX1-04's build chain needs Slice 2
  minimum (only an applied narrowed schema moves runtime batch
  geometry); its sort-input chain needs Slice 3
  (`make_sort_input_target`). "Blocking EX1-04" is true of the
  plan, not of this item.

## Slice 3+ — Scan-node targets with fixup; join/upper targets last

- Scan targets applied at `createPlanNode` (the single funnel) with
  setrefs-style fixup (`joinInputsFor` per-join site first, then the
  `createPlanAtSearchRoot`/`boundaryMap` above-tree half) —
  `make_group_input_target`, `make_window_input_target`,
  `make_sort_input_target`, `apply_scanjoin_target_to_paths` in that
  order. Each its own commit, each values-gated (the P4-01b lesson
  is per-slice, not once).

## Pre-code step (take3 TODO.md:503-504)

Re-take the §12 EXPLAIN table on current HEAD before Slice 2 (taken
at P4-A rev 6; ELEVEN optimizer commits since, incl. two
plan-affecting flips: `82dd30bbc` collapse default-ON,
`00d56df90` NARROW_BUILD default-ON which directly moves Q9
`Batches:`) — otherwise the 8→1 claim diffs against a stale
baseline. All "08"/"TODO" refs in this doc mean take3's.
