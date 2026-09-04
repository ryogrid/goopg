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
  `pathNCols`/`pathAvgVarBytes` readers. Coordinate-identity
  precondition (length + names-in-order) guards the arm — an
  IndexOnlyScan leaf (index-ordered subset output) falls back rather
  than misselecting shifted columns. Unknown target → bit-identical
  fallback. No scan `NCols` stamping (would desync planner-prices /
  executor-builds).
- CALIBRATION (as built): the Target arm is provably identical to the
  fallback where it runs, so Slice 2 is behaviour-neutral — it does
  NOT flip live `Batches:2→1` (that needs the 10→6 column delta from
  per-joinrel keep-sets = Slice 3). Gate: `changed=0` PP both suites
  + `-digest`/`-diff` values-identical + model-currency proof in unit
  harness (30 cols→NBatch 4, 10→2, 6→1 @128MB; DPPATH join.hash
  923247→669589 crossing 754717; narrowed inner bytes ≈100 ✓).
  The live flip (runtime `Batches:` + widths) gates Slice 3.
- Progress 2026-09-04: Slice 1 landed `588aa5fb5` (assert-only Target
  payload; 24/24 + 22/22 both arms); §12 re-taken on HEAD
  (`analysis/planner-refactor-take3/p401-retake-20260904/README.md`:
  witness now Batches:2, widths 1096/896/896/710, Q9 14.7 s serial);
  Slice 2 (this commit) lands Target-driven keep-set + model-currency
  proof + identity/decline pins, gates 24/24 + 22/22 both arms +
  TPC-DS PASS=95.

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
