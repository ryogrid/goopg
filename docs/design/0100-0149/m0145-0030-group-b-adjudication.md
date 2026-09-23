# M0145-0030 — group B: adjudicating the behavioural flip-triage tests

Status: in progress (started 2026-09-23). Task: `.ralph/fix_plan.md`
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

6 of the triage's 11 now pass under the flip as a side effect of M0145-0029:
- `TestNLISemiResidualExecution`, `TestNLIAntiResidualExecution`;
- `TestParallelNLIJointypeIdentity/semi`;
- `TestHashedInProbeActuallyFires`;
- `TestRunFastJoinConcrete`.

## Dispositions

| test | finding | disposition |
|---|---|---|
| `TestExplainSelfCorrelatedExistsDoesNotAliasCollide` | real defect: `Hash Cond: (t1.a = t1.a)` on the jointree arm | **fixed** `903780b3e` (below) |
| `TestCreateGroupingPathsGucOnPkFdStaysHash` | sorted instead of hashed | open |
| `TestPlannerSettingsReachScalarSubqueryJoin/…/nested`, `TestScalarSubqueryPropagationKeepsDefaultPlan/nested` | 2 costed inner plans, want 1 | open |
| `TestExplainAnalyzeRowsRemovedByJoinFilter` | triage: PG pushes the nullable-side ON qual down too | open (stale expectation to confirm) |

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

## Script audit (for the flip commit)

Two gate scripts pin the legacy arm and must change together with the
default:
- `scripts/tpcds-sf025-regression.sh:310`;
- `scripts/tpch-estimate-audit-arm.sh:108`.

The sweep's `:842` knob pass must become `=0`. To be completed and listed
here.
