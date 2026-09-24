# M0145-0008 legacy-deletion slice 3: the code only the legacy pipeline reached

Status: **LANDED 2026-09-24**. Task: `.ralph/fix_plan.md` M0145-0008. Earlier
slices: `m0145-0008-del1-fireset-head-vs-staged.md` (the gate),
`m0145-0008-del2-legacy-pipeline-deleted.md` (the pipeline and its knob).
Evidence: `analysis/m0145/m0145-0008-del3/`.

## What was deleted

Slice 2 left 20 `internal/optimizer` functions that `deadcode ./cmd/goopg`
reported as unreachable. This slice deletes them, together with everything
that existed only to feed or test them.

- **`predp.go` (whole file), the S5a pre-DP route.**
  `whereEligibleForPreDPUnnest`, `runJoinSearchBelowPinned`,
  `spliceSearchedSpine`, `layoutPosMap`, `remapSublinkOuterRefs`. Its knob
  `GOOPG_UNNEST_PREDP` (`unnestPreDPOn`, `SetUnnestPreDPEnabled`) is retired
  through `flagProvenanceRetired`.
- **`onerelsearch.go` (whole file).** `GOOPG_ONEREL_SEARCH` (`oneRelSearch`,
  `minSearchRels`, `setOneRelSearchForTest`) is retired the same way. The
  seam's floor is the constant 1.
- **The outer-spine peel and splice.** `splitOuterSpine`,
  `spineLinkSearchable`, `joinlist.innerPrefixBelowOuterSpine` and
  `fromItemRels` (the legacy leaf numbering). In `tryPGShapedJoinSearch`,
  `spine` was always empty after slice 2, so this slice removes it and
  everything that read it:
  - the `nprefix < floor && len(spine) == 0` arm (now `nprefix < floor`);
  - `hasSpine` / `spineOffset` and `pgShapedOffsetChecksOK`'s spine-offset
    check. The function is left with its per-leaf offset check only, and
    loses its `widths`, `hasSpine` and `spineOffset` parameters;
  - the `prefixNullable(spine)` conjunct arm and `prefixNullable`;
  - `spineAbove` in the search-problem literal (the field stays in
    `relfromjoinlist.go`, always false);
  - the splice under the lowest spine link and `traceSeamSpine`.
- **`enclosingtree.go`'s splice tripwires.** `walkEnclosingTree`,
  `assertEnclosingTreeColumnRefs`, `splicedSearchedRoot`,
  `assertSpineConsumesIdentityBoundaryMap`. The scope helpers that other code
  still uses stay.
- **Remap helpers only the splice used.** `rewriteExprRefsInPlace`,
  `remapByPosMap`, `remapOuterRefsInSubplan`. Their lines in the Expr-switch
  inventory (`exprwalk_inventory_test.go`) are deleted, as that test
  requires.
- **`joinTreeHasOuterLink`.**
- **Scripts.**
  - The sf025 sweep's knob-arm flow lane (`SF025_FLOW_KNOB`, a second EXPLAIN
    pass under `GOOPG_JOINTREE_PIPELINE=0`) is deleted.
  - So are the `JOINTREE` / `BASELINE_JOINTREE` / `CANDIDATE_JOINTREE`
    variables in `tpcds-fireset-gate.sh`, `jointree-parity-capture.sh` and
    `tpch-estimate-audit-arm.sh`.
  - The fire-set gate's `BASELINE_REV=worktree` mode now needs an env file
    on at least one arm; otherwise it is an A/A and exits 2.
- `scripts/planner-flags.env` is regenerated: both knobs read
  `retired(M0145-0008)`.

`deadcode ./cmd/goopg` over `internal/optimizer`: 106 → **84**. That is
below the pre-slice-2 baseline of 87, and nothing new appears.

## Tests

Test functions that exercised only the deleted code were deleted:
- the enclosing-tree tripwire pins (11);
- the pre-DP and splice pins in `predp_test.go` (7);
- the `remapByPosMap` arm matrix (10);
- `TestRewritePlanExprInPlaceMutates`;
- the spine joinlist pins (2);
- `TestCorpusQueriesWithASearchableInnerPrefix`, which had been pinned
  empty since C-04a;
- the spine-offset halves of the `pgShapedOffsetChecksOK` pins.

Helpers other tests share were kept: `preDPCatalog`, `planShapeString`,
`etCol`. `innerPlanWithOuterRef` moved from the deleted
`remap_arms_test.go` to `outerref_fixture_test.go`.

Tests that only toggled `GOOPG_ONEREL_SEARCH` now run on the only arm, the
searched one-relation scope, without the toggle.

`TestPreDPExistsValuesSurviveDPReorder` (executor) keeps its first half: the
correct ROWS for a correlated EXISTS over a three-relation FROM that the
search reorders. Its "legacy order" rerun was dropped: with no legacy order
it ran the same query twice.

## Gates (staged tree)

- units: PASS.
- `tpch-spotcheck`: PASS (Q12=2, Q13=33).
- fire-set HEAD `e49535540` vs staged: `fires=none` at SF0.25 and SF1.
- An opt-in `CORPORA=tpch` fire-set run: `fires=none`. It also exercises
  this slice's edits to the TPC-H capture lane (`jointree-parity-capture.sh`
  → `tpch-estimate-audit-arm.sh`).
- `tpcds-sf025 sweep`: `PASS=96`, PLAN-SHAPE `same=99 changed=0`.
- acceptance arm: 24 MATCH on values.
- ea-ratchet: 52/52.

Movement: none.

## What remains in M0145-0008

The rows of M0145-0001's retirement list (§6 of
`m0145-0001-jointree-ir-and-lowering-contract.md`) that name 0003–0008 are
**not all dead code**. `tryJoinSearch` / `tryPGShapedJoinSearch`, the post-hoc
unnest family (`unnestSubqueriesInPlan` et al., still the route for
sublinks the pull-up declines), `planFromClause` node-lowering, the
`joinlist` proto-IR and several decline classes are still the live route
on the jointree pipeline. §6 planned them to die once the IR-built problem
replaced them, and that replacement is only partly built. The next slice is
therefore an **audit**, not a deletion. For each §6 row, record whether the
mechanism:
- is already gone;
- is dead (deadcode); or
- is still live, with the task that must land before it can go.

Then close the dead rows and file the live ones.
