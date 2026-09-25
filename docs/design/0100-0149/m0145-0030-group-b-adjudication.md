# M0145-0030 — group B: adjudicating the behavioural flip-triage tests

Status: complete 2026-09-23 (loop #24). Residue is handed to the flip commit (§Hand-off). Task: `.ralph/fix_plan.md`
M0145-0030 (Kind: impl, Parent: M0145-0008). Origin: the M0145-0008 flip
triage, group B (`m0145-0008-flip-test-triage.md`).

## Method

Each test is re-run under a local flip (`jointreePipelineFromEnv`:
`v == "1"` → `v != "0"`, reverted before every commit) and classified:
- **PG-faithful change:** update the expectation, with oracle evidence;
- **legacy-only feature:** pin or rewrite it with an explicit setting;
- **real new-pipeline defect:** fix it.

No flip happens until each test is dispositioned.

## Baseline after M0145-0029 (2026-09-23)

Loop #21 recorded that 6 of the triage's 11 passed under the flip as a side
effect of M0145-0029. **That was wrong.** Loop #23 re-ran them under the
exact local code flip, both at HEAD and in a worktree at `d9b962326`
(before fix 1). All 5 executor tests fail at both points, so fix 1 did not
cause this:
- `TestNLISemiResidualExecution`, `TestNLIAntiResidualExecution`: Hash Semi
  Join is elected where the test expects the NLI semi path;
- `TestParallelNLIJointypeIdentity/semi`;
- `TestHashedInProbeActuallyFires`;
- `TestRunFastJoinConcrete`.

They are open again (table below). Group I's `TestSAOPWithConjunctMoves` and
the executor index end-to-end tests also still fail under the flip. They
belong to M0145-0029's residue, not to this task.

## Dispositions

| test | finding | disposition |
|---|---|---|
| `TestExplainSelfCorrelatedExistsDoesNotAliasCollide` | real defect: `Hash Cond: (t1.a = t1.a)` on the jointree arm | **fixed** `903780b3e` (below) |
| `TestCreateGroupingPathsGucOnPkFdStaysHash` | real defect: the index-ordered sorted variant was priced from its rule-era display cost | **fixed** (fix 2, below) |
| `TestPlannerSettingsReachScalarSubqueryJoin/…/nested`, `TestScalarSubqueryPropagationKeepsDefaultPlan/nested` | PG-faithful: the middle subquery's single-rel scan is now costed (145 = PG's seq-scan cost); the innermost join is unchanged at 171.25..354.75 | **stale pin, updated**: nested arms read the innermost costed plan (`innermostCostedScalarInner`) |
| `TestExplainAnalyzeRowsRemovedByJoinFilter` | PG-faithful: PG 18.3 puts the one-sided ON qual `b.val <> 'y'` in b's scan Filter | **stale expectation, updated**: the query uses the two-sided residual `a.id + b.id <> 4`, which PG prints as `Join Filter` / `Rows Removed by Join Filter: 1` |
| `TestNLISemiResidualExecution`, `TestNLIAntiResidualExecution` | PG-faithful: on the 4-row inner PG 18.3 also elects Hash Semi/Anti Join, never the index probe (not even with hash, merge and material all off) | **stale fixture, re-based**: `newNLIResidualFixtureLargeInner` adds 100k non-matching inner rows, where PG elects the probe `Nested Loop Semi Join`. Passes on both arms. For NOT EXISTS PG elects Merge Right Anti Join, which goopg lacks (no JOIN\_RIGHT\_ANTI, ledgered) |
| `TestParallelNLIJointypeIdentity/semi`, `/anti` | fixture re-based (100 outer x 100k inner, stats seeded, PG elects the probe). The jointree arm emits R25's decomposed lateral `Join`, not the fused node, and with `GOOPG_INDEX_PROBE_MULT=2` a bitmap probe, neither partial-capable | **legacy-only family.** The guard now also requires `NestedLoopIndexJoinIsPartialCapable`, so it fails as "wrong family" instead of a false N-copy. The flip commit pins or deletes it. Partial decomposed semi/anti is a capability gap (ledgered) |
| `TestHashedInProbeActuallyFires` (+ `TestHashedInBudgetPressure`, hidden behind its panic) | relied on the legacy unnest switch to keep a top-level IN as a SubPlan. PG pulls that IN up; it hashes the SubPlan only where pull-up is impossible | **stale query, updated**: `… IN (…) OR a < 0`, PG's `(hashed SubPlan 1)` shape. Passes on both arms |
| `TestHashedInMixedKindFallsBack` (hidden behind the same panic) | **non-PG premise**: PG rejects int IN (text subquery) with 42883. goopg accepts it and the arms disagree (1 row vs 0) | annotated; type-check gap ledgered; the flip commit decides (see §Hand-off) |
| `TestSubqueryUnnestKillSwitch` (hidden behind the same panic) | asserts goopg's internal unnest kill switch, which PG has no equivalent of and the jointree pull-up does not consult | **legacy-only (group L)**; the flip commit pins or deletes it |
| `TestRunFastJoinConcrete` | stale structure pin: the join search emits Project over Project over Join (with scan narrowing Projects). The legacy arm emits the identical stack whenever it searches (verified with the comma-join form); its single wrapper came from the rule-built explicit-JOIN path | **updated**: walk past any chain of Projects. Passes on both arms |

## Hand-off to the M0145-0008 flip commit

After this task, under the flip the optimizer and executor packages fail
only on:
- **group I** (M0145-0029's residue, not this task): `TestSAOPWithConjunctMoves`,
  the executor index end-to-end tests, `TestIOS_HeapFallback`, and the two
  deform tests;
- **three executor tests this task hands over:**
  - `TestParallelNLIJointypeIdentity/semi`, `/anti` (legacy-only fused family);
  - `TestSubqueryUnnestKillSwitch` (legacy-only switch);
  - `TestHashedInMixedKindFallsBack` (non-PG premise; convert it to a 42883
    assertion once the type check lands, or pin it until then).
  The optimizer package's `pinLegacyPipeline` is test-local and cannot be
  reached from `internal/executor`. AGENT.md forbids a second
  pipeline-selection mechanism, so the flip commit chooses between deleting
  these arms and adding a sanctioned test-only pin.

## Fix 1: pulled-up bodies get their own SourceTableIdx range (`903780b3e`)

A pulled-up EXISTS/ANY body numbers its tables from 1, as the outer query
does, so a self-correlated EXISTS spliced two `SourceTableIdx = 1` scans into
one plan. `explain_names.go` resolves a ColumnRef's alias first-wins per
`SourceTableIdx`, and printed `t1.a = t1.a`. The legacy unnest avoids this
with `remapSourceTableIdx`; the jointree pull-up (M0145-0003) never did.
`assignPulledSourceOffsets`, run after `flattenPulledBodies`, gives each body
an offset past every `SourceTableIdx` already in use. It shifts the body's
leaf scans (with `remapSourceTableIdx`) and binding records, and
`rebasePulledQual` adds the owning body's offset to the refs it rebases, or
the ancestor body's offset for a nested body's outer refs. Pinned by
`TestPulledExistsBodyGetsItsOwnSourceTableIdx`. The new pipeline's fire-set
plans are identical apart from column qualifiers (for example, Q16 now prints
`cs_order_number = cs2.cs_order_number`).

## Fix 2: the index-ordered grouping variant is priced from the search rel

On the jointree arm a single-table scope goes through the one-rel search.
For `btg(x, y, z, w)` with `btg_x_y_idx(x, y)`, no stats, and
`GROUP BY y, x`, the search rel holds a seq path at 1.01 and a full
ordered index path at 16.14. The GROUP_AGG rel's index-driven sorted
variant (`upper.groupagg.sortedidx`) did not use that path. It built its
own IndexOnlyScan through `indexOrderedAggInput` and priced it with
`legacyDisplayCostOf` at 0.01, so sorted cost 0.03 and beat hashed at 1.03.
PG 18.3 on the same table elects `HashAggregate` over `Seq Scan`. PG takes
the sorted input from `input_rel->pathlist` (`add_paths_to_grouping_rel`,
planner.c:7128), so the input price is `cost_index`'s.

`searchRelIndexPathCost` (groupingpaths.go) reaches the search rel with
`searchedJoinInputRelOf(child)`. It finds the cheapest unparameterised
`PathIndexScan` on the same index and prices the variant from it: sorted
16.16, hashed 1.03, hashed wins. A legacy-arm child is not a join-search
product, so the helper declines and the legacy arm is byte-identical.
Pinned by `TestIndexOrderedGroupingPricedFromSearchRelJointree`, which
fails without the fix. Knob-arm TPC-DS SF0.25 plan shapes are unchanged
(99/99), so this shape does not fire in that corpus.

## Script audit (for the flip commit)

Every harness site that selects the pipeline arm, as of loop #23. None of
them may change before the flip. Each must change IN the M0145-0008 flip
commit, or the pre-flip baselines silently switch arms:
- `scripts/tpcds-sf025-regression.sh:310`:
  `sf025_jointree_pipeline="${GOOPG_JOINTREE_PIPELINE:-0}"` → default `1`.
- `scripts/tpcds-sf025-regression.sh:823-893`: the extra EXPLAIN-only
  knob-arm pass (`GOOPG_JOINTREE_PIPELINE=1` at `:842`). After the flip it
  duplicates the main capture. Invert it to `=0`, as a legacy-arm side
  channel until the legacy code is deleted, or retire it.
- `scripts/tpch-estimate-audit-arm.sh:35,108`: `JOINTREE` default `0` →
  `1` (comment and code).
- `scripts/tpcds-fireset-gate.sh:13,56-57`: `BASELINE_JOINTREE=0`,
  `CANDIDATE_JOINTREE=1`. This is a same-binary arm comparison, which stops
  meaning anything once the legacy arm is no longer shipped. The flip
  commit has to decide what the gate compares afterwards (HEAD vs staged
  on the new arm). Flagged, not decided here.
- `scripts/jointree-parity-capture.sh:99`: default `1`, which is already
  the post-flip arm. No change.
- `scripts/planner-flags.env:49`: the label `unset(off)` is regenerated in
  the flip commit (group K's `TestFlagProvenanceEnvIsGenerated`).
- `scripts/tpch-acceptance-arm.sh`, `scripts/tpch-spotcheck.sh`, `ci/`,
  `Makefile`: no knob reference, so they follow the default.
