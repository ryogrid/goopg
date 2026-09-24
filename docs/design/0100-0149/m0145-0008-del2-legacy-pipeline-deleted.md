# M0145-0008 legacy-deletion slice 2: the legacy pipeline and its knob are deleted

Status: **LANDED 2026-09-24**. Task: `.ralph/fix_plan.md` M0145-0008 (the
cutover). Slice 1 (the HEAD-vs-staged fire-set gate, the precondition):
`m0145-0008-del1-fireset-head-vs-staged.md`. Flip: `m0145-0008-cutover-flip.md`.

## What was deleted

- **The dispatch.** `planSelectWithSettings` is now the statement body
  itself. `planSelectLegacyPipeline`, `planSelectJointreePipeline`,
  `planSelectImpl`'s `jointree` parameter and `jointreepipeline.go`
  (`jointreePipeline`, `jointreePipelineFromEnv`, `SetJointreePipeline`) are
  gone.
- **Every `jointree` / `jointreePipeline` branch, folded to the jointree
  arm.** In `planner.go`:
  - the rule-based single-table FROM bypass and its WHERE twin (the
    `isSimpleSingle && !oneRelSearchEnabled() && !appendrelMember &&
    !jointree` chooser: `planIndexScanFromWhere`, NOT NULL reduction, LIKE
    range injection). The surviving generic arm's `else` body is de-indented
    one level.
  - the S5a pre-DP unnest arm (`runJoinSearchBelowPinned`) and its
    `preDPUnnested` flag. The post-hoc `unnestSubqueriesInPlan` now runs
    unconditionally, as it already did on the jointree arm.
  - the WHERE-less `joinTreeHasOuterLink(node) || appendrelMember ||
    jointree` gate, now a plain `else`.
  - the `jointree && len(ctx.bindings) == 1` NOT NULL reduction guard, and
    `(jointree || oneRelSearchEnabled())` on the one-relation index
    producer.
  - `appendrelSubquery`'s `jointreePipeline &&`.

  In `joinsearchseam.go`: the search floor is the constant 1; the outer
  spine is never peeled (`chain, jl := node, ctx.joinlist`, `spine` always
  empty); the deferred-leaf `nChain` count and the `extractScopeLeaves`
  source are unconditional. In `collapse.go` and `specialjoin.go`, the
  deferred semi/anti leaf numbering (`jointreeItemEmittingRels`) is the only
  numbering.
- **The knob.** `GOOPG_JOINTREE_PIPELINE` moves to `flagProvenanceRetired`
  (`"M0145-0008"`, the M0145-0018 precedent: the order entry stays so older
  artefacts still decode). `scripts/planner-flags.env` is regenerated, and
  the stamp now reads `retired(M0145-0008)`.
- **The sf025 legacy control pass** (`SF025_FLOW_KNOB`) is off by default.
  With no legacy pipeline it would be a second capture of the same
  pipeline, labelled as a legacy control. The block itself goes in the
  dead-code slice.

## Test dispositions

Every test the flip had pinned to legacy (`pinLegacyPipeline`,
`SetJointreePipeline("0")`) was **run unpinned first**. The disposition
follows the result:

- **Passes on the jointree pipeline:** kept, pin removed. Most of them:
  every `pinLegacyPipeline` caller not listed below.
- **Asserts a legacy-only shape:** deleted. 30 optimizer test functions plus `jointreepipeline_test.go`: hash-keyed
  semi/anti joins from the post-hoc unnest (`TestUnnestCorrelated{Exists,NotExists}`,
  composite/IN/SJInfo key pins, `FlattenedRHS` stamps), the legacy rule
  joins in `planner_test.go` (`TestPlanSelectJoin`,
  `TestPlanJoinPicksHashAlgo`, `TestPlanJoinHashBuildSidePicksSmaller`,
  `TestPlanSelectCommaFromUsesCrossJoin`), the pinned-spine / admit-semi-anti
  extraction probes (`semiantichain_test.go` ×4,
  `m0142_0008a_3i_*` ×2), the "stays a SubPlan" pins whose shape PG now
  pulls up (`TestUnnestExistsUnliftableResidualStaysSubPlan`,
  `TestIndexKeyRangeCorrelationStaysSubPlan`, `TestJoinOnInSubquery_InnerJoin`),
  the knob-off inertness pins (`TestOneRel{Reroute,Search}IsInertWithTheKnobOff`,
  `TestLegacyArm*`), the dispatch/polarity pins (`jointreepipeline_test.go`),
  and `TestPGShapedSeamDeclines/single_relation` (the floor is 1).
  Executor side: `TestParallelNLIJointypeIdentity`'s semi/anti rows (the fused
  semi/anti NLI family was legacy-only), `TestHashedInMixedKindFallsBack` (it
  pinned non-PG behaviour: PG raises 42883 on `int = text`, and that gap is
  already ledgered), and `TestSubqueryUnnestKillSwitch` (its fixture's EXISTS
  is pulled up now, so the post-hoc switch no longer governs it).
- **Rewritten to the PG property it guarded:** the two correlated-IN
  correctness pins. PG 18.3 pulls a correlated IN up as a LATERAL semi join
  (`convert_ANY_sublink_to_join`, subselect.c:1356 `use_lateral`), and so
  does the jointree pipeline. The legacy test asserted "must stay a SubPlan",
  which was the legacy unnest's own refusal after its key-selection bug. The
  new `assertCorrelatedInKeepsBothConjuncts` pins the correctness property
  instead: when the IN is decorrelated, the semi join carries both the IN
  equality and the correlation qual (probed: an AND of both).
- **Both-arm comparisons:** kept the jointree half. The declined-pull-up
  cases now pin the route census (`jointree-pullup` 0, `jointree-posthoc` 1)
  in place of shape equality with a knob-off plan.

Coverage note: some deleted tests exercised post-hoc unnest code that is
still reachable, through sublinks the pull-up declines. The post-hoc route is
still pinned by `TestJointreeArmBypassesThePinnedSpineRoute` and the
declined-pull-up census cases.

## Gates (staged tree)

- units (`RALPH_PRECOMMIT_SCOPE=units`): PASS.
- `tpch-spotcheck`: PASS (Q12=2, Q13=33).
- **`tpcds-fireset-gate.sh` (HEAD `d7ca698fa` vs staged): `fires=none` at
  SF0.25 and at SF1.** The deletion moved no TPC-DS plan, which is the
  expected result for code the default no longer reached. This is the first
  commit gated by slice 1's HEAD-vs-staged design.
- `tpcds-sf025 sweep`: `PASS=96`, PLAN-SHAPE `same=99 changed=0`,
  FLOW-CONVERGENCE `pinned-spine=0 jointree-pullup=23`.
- `tpch-acceptance-arm` vs `arm-on-20260922-loop77`: 24 MATCH on values.
- `make ea-ratchet`: 52 vs 52, PASS.

Movement: none. No plan moved on either corpus, and no value changed.

## Next: the dead-code slice

`deadcode ./cmd/goopg` lists 20 functions in `internal/optimizer` that this
slice made unreachable (baseline 87, now 106):
- `predp.go` whole: `whereEligibleForPreDPUnnest`,
  `runJoinSearchBelowPinned`, `spliceSearchedSpine`, `layoutPosMap`,
  `remapSublinkOuterRefs`.
- The spine peel: `splitOuterSpine`, `spineLinkSearchable`,
  `joinlist.innerPrefixBelowOuterSpine`, `fromItemRels`.
- `enclosingtree.go`'s splice asserts: `walkEnclosingTree`,
  `assertEnclosingTreeColumnRefs`, `splicedSearchedRoot`,
  `assertSpineConsumesIdentityBoundaryMap`.
- The remap helpers only the splice used: `rewriteExprRefsInPlace`,
  `remapByPosMap`, `remapOuterRefsInSubplan`.
- `joinTreeHasOuterLink`.
- The now-unread knobs `GOOPG_ONEREL_SEARCH` (`oneRelSearchEnabled`,
  `minSearchRels`) and `GOOPG_UNNEST_PREDP` (`unnestPreDPEnabled`). Both
  retire through `flagProvenanceRetired`.

About 20 test files reference them. They go together with the spine-aware
code in `tryPGShapedJoinSearch` (always `len(spine) == 0` now), the
sf025 `SF025_FLOW_KNOB` block, and the remaining `JOINTREE` variables in the
fire-set / parity-capture / estimate-audit scripts. After that, the seam
guards on M0145-0001's retired list.
