# M0145-0008 flip: unit-suite triage

Status: task M0145-0008 CLOSED 2026-09-24 (legacy-deletion slice 4, `m0145-0008-del4-pgshaped-knob-and-retirement-audit.md`). Earlier: task BLOCKED 2026-09-23 on two owner calls (multiplier; executor legacy pin) — see fix_plan M0145-0008 escalation. Earlier: triage produced 2026-09-23; group L pinned (test-only commit); groups
I and B were held in the M0145-0001 escalation until the owner's 2026-09-23
answer — **now filed as M0145-0029 (group I) and M0145-0030 (group B)**,
both sequenced before the flip. The default is NOT flipped yet.
Task: `.ralph/fix_plan.md` M0145-0008 (Kind: impl). Parent design:
`m0145-0008-cutover-readiness-timing-ab.md`.

## Method

The flip is one line — `jointreePipelineFromEnv` in
`internal/optimizer/jointreepipeline.go` changes from `v == "1"` (fail-closed
opt-in) to `v != "0"` (default on, `0` = legacy opt-out, the
`GOOPG_PGSHAPED_DP` precedent). Applied locally and run through
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: **54 failing
tests** — 37 in `internal/optimizer`, 17 in `internal/executor`, nothing else.
Every failure was classified before anything was changed; the flip line was
then reverted, so the default arm is unchanged by this slice.

## Correctness check first

Two failures claimed a correctness regression: `TestUnnestCorrelatedIn_Rejects*`
assert that `x IN (SELECT y FROM t2 WHERE z = t1.w)` stays a SubPlan, because
the LEGACY unnest once keyed that join on the correlation pair only and
returned wrong rows. On the jointree pipeline the IN becomes a semi join —
which is PG 18.3's own behaviour: `convert_ANY_sublink_to_join`
(`./postgres/src/backend/optimizer/plan/subselect.c`) converts a correlated ANY
sublink as a LATERAL join (`use_lateral = !bms_is_empty(sub_ref_outer_relids)`).
The join's predicate was inspected directly: `(x = y) AND (z = w)` and
`(x = y) AND (z = x)` — both conditions present, so the legacy bug class does
not exist on this route. The tests guard legacy-only code.

## Groups

| group | count | what it is | disposition |
|---|---|---|---|
| **K** — knob/flag | 2 | `TestJointreePipelineKnobPolarity` (asserts `""` → off), `TestFlagProvenanceEnvIsGenerated` (`scripts/planner-flags.env` records the default) | change IN the flip commit (they state the default itself) |
| **L** — legacy machinery | 31 | post-hoc unnest keying/SJInfo/FlattenedRHS stamping, pinned semi/anti spine seam (`extractSearchLeavesAdmitSemiAnti`), rule-based join construction on no-stats tables (`TestPlanJoinPicksHashAlgo` …), "knob off" inertness guards | **pinned to the legacy pipeline now** with `pinLegacyPipeline(t)` (`pipeline_pin_test.go`); a no-op under today's default, verified to turn all 31 green under the flip. They die or are rewritten with the code they cover in the legacy-deletion slice |
| **I** — index-path coverage on the one-relation search | ≥10 | `TestSAOPWithConjunctMoves` (`IN (2,3) AND i_flag > 1` gets no index), executor `TestIndexScan{Varchar,Char,Timestamp}EndToEnd`, `TestIOS_*` (4), `TestIndexOnlyDeformColdAndVisible`, `TestIndexDeformRescanPersistsBound` (point query elects `BitmapHeapScan`), `TestArrayIndexOnlyScanAnswersFromKey/date` | **held (lineage budget; would-be M0145-0029)** — the single-table scope is planned by the one-rel search on the new default instead of the rule-based bypass; measure per case whether the index path is never generated (a capability gap, like Q17's correlated probe) or generated and out-costed on no-stats tables, against what PG 18.3 elects on the same tables **Update 2026-09-23 (M0145-0029):** producers ported (restriction equality/range/SAOP, index-only with quals) and re-run (`9e368ade8`): 6 subtests fixed, 4 stale expectations (update in the flip commit), 3 multiplier-dependent (owner), 2 real gaps — table in `m0145-0029-one-rel-index-path-coverage.md` §Slice 5. |
| **B** — other behaviour | 11 | `TestCreateGroupingPathsGucOnPkFdStaysHash` (strategy changes from Hashed), `TestPlannerSettingsReachScalarSubqueryJoin/…/nested` + `TestScalarSubqueryPropagationKeepsDefaultPlan/nested` (2 costed inner plans, want 1), `TestNLISemiResidualExecution` / `TestNLIAntiResidualExecution` / `TestParallelNLIJointypeIdentity/semi` (NLI semi/anti not elected), `TestRunFastJoinConcrete`, `TestHashedInProbeActuallyFires` (nil kvcache — the hashed-IN SubPlan is not built, and the test dereferences it), `TestExplainSelfCorrelatedExistsDoesNotAliasCollide`, `TestExplainAnalyzeRowsRemovedByJoinFilter` (the nullable-side ON qual `b.val <> 'y'` is now a scan filter — PG pushes it down too, so this one is a stale expectation) | **held (lineage budget; would-be M0145-0030)** — per test: PG-faithful change (update the expectation, citing PG), legacy-only feature (pin or rewrite with an explicit GUC), or a real new-pipeline defect (fix). No flip until each is dispositioned |

## Order to the flip

1. (this slice) group L pinned — test-only, valid under both defaults.
2. Groups I and B driven to zero under a local flip — pending the owner's
   answer to the M0145-0001 lineage escalation (2026-09-23).
3. The flip commit: the one-line default change, group K's two tests,
   `scripts/planner-flags.env` regenerated, the full value-gate set on the new
   default (units, tpch-spotcheck, acceptance arm, SF0.25 sweep, fire set) and
   floor captures. The `jointree-parity-capture.sh`/fire-set scripts keep
   working because they pass `JOINTREE` explicitly. **Audited 2026-09-23 —
   two gate scripts pin the LEGACY arm explicitly and must change in the flip
   commit**, or the value gates would keep measuring the old pipeline after
   the default moves: `scripts/tpcds-sf025-regression.sh:310`
   (`sf025_jointree_pipeline="${GOOPG_JOINTREE_PIPELINE:-0}"`) and
   `scripts/tpch-estimate-audit-arm.sh:108`
   (`GOOPG_JOINTREE_PIPELINE="${JOINTREE:-0}"`). The sweep's separate
   knob-arm EXPLAIN pass (`:842`, hard-coded `=1`) must become `=0` so it
   stays a CONTROL arm instead of duplicating the default.
4. Legacy deletion (later slice): the group L tests go with their code.
