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
- Progress 2026-09-04: Slice 3 first cut (statement-wide → joinrel
  source) was dormant on live Q9 (bit-identical EXPLAIN — root cause:
  prebuilt-leaf veto, NOT subproblem ineligibility); second cut lifts
  the veto (prebuilt as boundary leaf, walk never descends) + adds
  `corrAbove` decline (preserves Q2 decorrelation). LIVE FLIP: Q9
  witness Batches 2→1, widths 1096→776/896→640/896→736/710→582,
  delta 10→7 (below-point keys drop; orders-link order keeps 3 keys —
  gate asserts the rule, not literal 6). Gates: 24/24 values,
  plan-gate 22/22 (shape unchanged, widths-only movement), TPC-DS
  PASS=95 MISMATCH=0, Q9 serial 14.66→10.80 s, identical hashes at
  64/4/512 MB, GOOPG_ASSERT_ROW_SHAPE=1 on.

## Slice 3+ — Per-joinrel keep-sets; scan-node targets with fixup; upper targets last (reviewed 2026-09-04: proceed-with-changes, all six amendments below folded in)

- SINGLE-SCANJOIN-TARGET RULE (F1): one scanjoin target for the whole
  scan/join tree, computed from the union needed above the tree;
  joinrel tlists derived, NEVER parent-stamped onto shared paths —
  `addPathsToJoinrel` reads shared `outer.CheapestTotal` /
  `inner.CheapestTotal` (`joinpaths.go:176`), so a parent-stamped set
  would silently serve the second parent the first parent's
  projection (take3 01 §3 pattern).
- HASH-BUILD-ONLY CARRIES FORWARD (F2): the Slice-2 `kind ==
  "PathHashJoin"` refusal stays — merge inputs are OUT of scope for
  the first cut (goopg merge join order lives in `HashKeys` list
  order, `createplanjoin.go:327-335,371-382`; executor sorts by that
  order; projecting a merge input can drop/shift a sort-key column
  `sortInnerAndOuter`/`absorbMergeSort` assume present). Merge-input
  narrowing needs its own slice with a sort-key-preservation proof.
- FIXUP INVENTORY (F3): join quals (via `translateToLayout`), sort
  keys, agg/window args, subplan `Args` + `OuterColumnRef` remap
  (`joinlayout.go:502-515,553-593`), NLI probe keys. The refusal arms
  (`createplanjoin.go:194-200,227-244`) must NEVER be loosened —
  anything uninventoried declines (keeps the column), Slice-2
  precedent. Intermediate joinrels consumed by NLI probes or
  `MergeWholeRowRef` inside the tree are NOT boundary-repaired.
- ATTRIBUTION RULE (F4): name-keyed sets over-state across relations
  (safe-widening at statement granularity becomes wrong-narrowing at
  join granularity); self-joins (Q21's three lineitems, Q7's two
  nations) need RTE/source-qualified attribution or provably-safe
  fallback — the guard-blind permutation class `buildKeepSet` warns
  about must not be re-opened.
- CUT ORDER (F5/F7): FIRST cut = keep-set SOURCE change at the
  Slice-2 site only (`joinInputsFor` → `narrowBuildInput`,
  `createplanjoin.go:277-286`; `Project`-based, hash-only, all three
  Slice-2 guards carried over unchanged). DEFERRED in order: (a)
  merge/NL input policy, (b) scan-node application at `createPlanNode`
  with fixup (narrowing node without layout trips `:289`; narrowing
  both replays the P4-01b leaf-schema incident — proven-safe shape is
  `Project`-below-build-side only), (c) upper targets
  (`make_group/window/sort_input_target`) above the search root. Each
  its own commit, each values-gated.
- PER-COMMIT GATE (F6): predict per-level column delta (witness
  10→6) and per-level `hashsize.Choose` `NBatch` BEFORE measuring
  (§13.6 lesson); width ≈100 not 6; runtime `Batches:` on fresh
  server, pinned GOGC/GOMEMLIMIT/cgroup, EXPLAIN ANALYZE `work_mem`
  64 MB S-cold serial; diff against the PG ORACLE not goopg-vs-goopg
  (§13.5); gate at 64/4/512 MB budgets; `GOOPG_ASSERT_ROW_SHAPE=1`
  on; PP movement on the flip commit is widths-only — any plan-shape
  change fails the commit.

## Pre-code step (take3 TODO.md:503-504)

Re-take the §12 EXPLAIN table on current HEAD before Slice 2 (taken
at P4-A rev 6; ELEVEN optimizer commits since, incl. two
plan-affecting flips: `82dd30bbc` collapse default-ON,
`00d56df90` NARROW_BUILD default-ON which directly moves Q9
`Batches:`) — otherwise the 8→1 claim diffs against a stale
baseline. All "08"/"TODO" refs in this doc mean take3's.
