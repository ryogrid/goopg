# M0145-0008 legacy-deletion slice 4: GOOPG_PGSHAPED_DP retired; the §6 retirement audit

Status: **LANDED 2026-09-24. M0145-0008 closes with this slice.** Earlier
slices: `m0145-0008-del1-fireset-head-vs-staged.md`,
`m0145-0008-del2-legacy-pipeline-deleted.md`, `m0145-0008-del3-dead-code.md`.
Evidence: `analysis/m0145/m0145-0008-del4/`.

## GOOPG_PGSHAPED_DP retired

M0145-0001 §6 lists `GOOPG_PGSHAPED_DP` among the knobs deleted at the 0008
cutover. It was the PG-shaped search's kill-switch:
- flipped on by M0127-P5.9 (2026-08-06);
- after M0127-P6.3 deleted the bushy enumerator, `=0` meant "no join-order
  search at all": the syntactic FROM order plus the rule rewrites.

Nothing ships that arm, but two harness scripts selected it by default:
- `tpch-estimate-audit-arm.sh` exported `PGSHAPED:-0`, so every TPC-H
  capture through it (`jointree-parity-capture.sh tpch`, the opt-in TPC-H
  fire-set lane) measured a planner with no join search.
- The M0145-0020a false-red Q9 came from the same default in the acceptance
  arm. That script was already moved to `1`, but the estimate-audit arm
  never was.

Removed:
- `pgShapedDP` / `pgShapedDPFromEnv` / `pgShapedDPEnabled` (joinsearch.go);
- the two guards that read them: `tryPGShapedJoinSearch` and
  `assertSearchedBoundariesIntact`;
- the resolver entry in `flagResolvedState`. The flag is retired through
  `flagProvenanceRetired` (`"M0145-0008"`), and `planner-flags.env` is
  regenerated;
- the `PGSHAPED` variable and the header token in
  `tpch-acceptance-arm.sh`, `tpch-estimate-audit-arm.sh` and
  `jointree-parity-capture.sh` (`TPCH_FIRESET_PGSHAPED`).
  `GOOPG_PGSHAPED_DP_TRACE` (the enumeration trace) stays.

**Measured consequence:**
- The TPC-H capture lane reads `PLAN-PARITY: queries=22 match=3` on the
  shipped planner.
- The same lane read `match=1` at slice 3, on the accidental `=0` default.
- The fire-set A/B is identical on both arms (`fires=none`), so the 3-vs-1
  is the lane correction, not a plan change.

### Tests

Tests that pinned the kill-switch arm were run on the search:
- `useLegacyEnumerator` (legacyarm_test.go, deleted);
- the explicit `pgShapedDP = false` subtests;
- `withPGShapedDP`, which forced it on and is now a no-op, removed.

**15 failed, and all 15 are deleted.** Each asserts a *rule* rewrite on a
stat-less fixture, where the search picks a bare nested loop on cost:
- `rewriteJoinsToNLI` promotion (`TestNLIRule*`, `TestNLI*Residual*`,
  `TestNLI*Alias*`);
- small-dimension build side (`TestHashJoinBuildOnSmallDim`);
- OR-of-ANDs join-key extraction (`TestPlanOrOfAndsExtractsJoinKey`,
  `TestPlanQ19LiveSQL`, `TestInExprLiteralListPushdown`);
- `TestHashKeysAreInt64OnPlannedIntegerJoin`;
- `TestPlanCommaFromPushesEqualityIntoJoin`;
- `TestSAOPMultiTableRewriteMoves`.

M0127-P5.9's own note on the helper forbade relaxing these assertions to
accept the searched plan, so they are deleted, not rewritten. Its coverage
obligation (a searched-arm test for the same behaviour) is met by
`TestPGShapedSearchPicksNLIOnCost`, `TestPGShapedSearchPicksHashJoinOnCost`
and the multikey `searched enumerator` subtest.

**Residue (ledgered):** the rule passes still run on trees the search
declines (LATERAL, more than `maxSearchRels`, a pinned FULL join). They no
longer have a direct pin on such a tree.

The kill-switch pins themselves are also deleted:
`TestPGShapedSeamIsInertWithTheFlagOff`, `TestPgShapedDPDefaultsOn`,
`TestPgShapedDPKillSwitchPolarity`, and multikey's `legacy syntactic
builder`. `planTreeString` / `walkPlanString` / `findFirstJoin` moved to
`plantree_helpers_test.go`.

## The M0145-0001 §6 retirement audit

Each row of `m0145-0001-jointree-ir-and-lowering-contract.md` §6, checked
against the code at this commit:
- **GONE:** no declaration and no use outside comments.
- **DEAD:** `deadcode ./cmd/goopg` reports it.
- **LIVE:** still called on the shipped pipeline.

| §6 row (planned retirement) | verdict |
|---|---|
| `GOOPG_JOINTREE_PIPELINE` (0008) | GONE (slice 2) |
| `whereEligibleForPreDPUnnest`, `preDPUnnested`, `GOOPG_UNNEST_PREDP` (0003) | GONE (slices 2–3) |
| `runJoinSearchBelowPinned`, `spliceSearchedSpine`, `remapSublinkOuterRefs`, `layoutPosMap`, spine `remapByPosMap` (0003) | GONE (slice 3); `reresolveJoinByName` LIVE (NLI rebinding, `nl_index_join.go`) |
| `admitSemiAnti` restriction (0003) | GONE; `semiAntiChainLink` / `bodyQuals` LIVE (seam extraction) |
| `FlattenedRHS`, `decomposeFlatBodyTree`, `flatBodyScopeProject`, `schemaIsLeafConcat` (0003) | LIVE (pull-up bridge + post-hoc unnest) |
| post-hoc unnest family `unnestSubqueriesInPlan` … `findFilterContainingExistsExpr` (0003) | LIVE — the route for every sublink the pull-up declines |
| `remapSourceTableIdx` (0003) | LIVE |
| eager `planExistsExpr` / `planInExpr` (0003) | LIVE |
| `leaf-count` / `chain-not-flattenable` / `semianti-*` decline families (0003–0005) | LIVE (seam walk) |
| `chainCarriesLateral` (0003–0005) | LIVE |
| `setOpBranch*` (0004/0006–0007) | LIVE (window/set-op paths) |
| `splitOuterSpine` + `outer-spine` / `spine-width` declines (0005) | GONE (slices 2–3); `prefix-*`, `offset-disagreement`, `residual-hits-pad` LIVE |
| `tryJoinSearch` / `tryPGShapedJoinSearch` / `extractSearchLeaves` (0005) | LIVE — the search entry |
| `outer-link-no-sjinfo` / `semianti-link-no-sjinfo` (0005) | LIVE (fail-closed checks) |
| `isSimpleSingle` bypass + `GOOPG_ONEREL_SEARCH` + `joinTreeHasOuterLink` (0005) | bypass, knob and arm GONE; `isSimpleSingle` LIVE only as the one-relation index-producer guard |
| `heldAbovePrefix` (0005) | LIVE |
| `pushPredicatesIntoCrossJoins` / `pushSingleSideQualsIntoInnerJoinInputs` / `rewriteScanInputsWithSingleTablePredicates` / `pushOuterQualsIntoLaterals` (0005) | LIVE |
| `localizeExprToLeaf` / `rebaseChainQual` / `rebaseSemiAntiChainQual` / `remapWalkOrderFlatToSpans` / `buildLeafSpans` (0005) | LIVE |
| stage-builder elections (0006) | LIVE |
| `wrapSetOpSortLimit` (0006–0007) | LIVE |
| `rewriteJoinsToNLI` + `stampSemiProbePrices` (0007) | LIVE |
| `fillJoinHashKeys` as a pass (0007) | LIVE |
| `remapSubqueryColumnRefs`, `translateToLayout` (0007) | LIVE; `applyJoinTreePosMap` / `remapPosMapAfterRewrite` / `remapWithBindings` / `remapExprRefsToMHJ` GONE |
| `tryPromote*IndexOnlyScan` post-hoc wraps (0007) | LIVE |
| rowmark passes (0007) | LIVE |
| legacy pipeline wholesale + `GOOPG_UNNEST_PREDP` / `GOOPG_PGSHAPED_DP` / `GOOPG_ONEREL_SEARCH` (0008) | pipeline and all three knobs GONE; `planFromClause` and the `joinlist` proto-IR LIVE |

**Reading.** Everything §6 assigns to 0008 — the pipeline, its knob and the
three legacy knobs — is gone. So is every row whose mechanism only the
legacy pipeline reached. Each row still LIVE was planned to die when an
IR-built search problem replaced the node-tree walk (0003/0005/0007). Those
milestones closed with the jointree pipeline running *through* the walk,
the post-hoc unnest and the rule passes, not instead of them. These are the
shipped planner's live route, not legacy leftovers. Retiring them is
reimplementation (the IR-built problem, deferred-sublink planning,
lowering-owned hash keys and rowmarks), not deletion, so it is outside the
cutover. It is filed as **M0145-0008n** (recon: sequence the live rows
against M0146's re-baseline census, since several are parity-relevant, such
as post-hoc unnest shapes and rule-built NLIs on declined trees) and
ledgered.

## Gates (staged tree)

- units: PASS.
- `tpch-spotcheck`: PASS (Q12=2, Q13=33).
- fire-set HEAD `14e36d849` vs staged: `fires=none` at SF0.25, SF1 and
  (opt-in) TPC-H.
- `tpcds-sf025 sweep`: `PASS=96`, PLAN-SHAPE `same=99 changed=0`.
- acceptance arm: 24 MATCH on values. The baseline file still carries the
  old header token, and the value diff ignores headers.
- ea-ratchet: 52/52.
- deadcode: 84, no new unreachable function.

Movement: none.
