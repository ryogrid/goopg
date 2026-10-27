# M0147-0051 — delete the legacy NLI post-pass and `goopg.enable_nestloop_index`

Status: landed 2026-10-27.

Parent: M0147-0050, the recon that found the pass inert on the corpora. Unblocked by M0147-0053.

## What was deleted

M0054-0006's `rewriteJoinsToNLI` ran after planning. It walked the finished tree, skipped every subtree the PG-shaped
search had built, and rewrote an eligible unsearched `*Join` into a fused `*NestedLoopIndexJoin` probing a B-tree
index. It ran under a rule-based cost gate, with an optional Memoize attach (`maybeAttachMemoize`). Two switches
controlled it:
- the per-statement goopg GUC `goopg.enable_nestloop_index`;
- `GOOPG_NLI_COSTGATE=legacy`, which restored the stats-blind semi/anti gate.

PG has no such pass. Its parameterised index nested loop is an ordinary path from `add_paths_to_joinrel` →
`match_unsorted_outer` (`postgres/src/backend/optimizer/path/joinpath.c`).

## Why now

- **M0147-0050.** With the GUC off, corpus EXPLAIN was byte-identical. A probe showed the pass rewrote 0 corpus joins
  and 20 regress joins, and disabling it changed no regress verdict.
- **First trial (M0147-0051).** Disabling the call failed exactly one test. Its LEFT JOIN, with a preserved-side ON
  conjunct, was one the search declined; the pass gave it PG's index-probe nested loop.
- **M0147-0053.** That fix let the search build such joins, and the statement now plans PG 18.3's own shape through
  the search.
- **Second trial.** With the call disabled, the units gate is green. Nothing depends on the pass.

## Removed

- `planner.go`: the call.
- `nl_index_join.go`: `rewriteJoinsToNLI`, `walkRewriteNLI`, `tryBuildNLI` and the 22 helpers only it reached,
  together with the `GOOPG_NLI_COSTGATE` env read, `nliCostGateLegacy`, `SetNLICostGateLegacy` and the unused
  `nliMaxOuterRowsHeuristic`. The file now holds only the index-probe helpers the parameterised path producer uses.
- `nl_index_join_selectivity.go`: the legacy rebind guard's three helpers.
- `memoize.go`: deleted. It held `maybeAttachMemoize` and three constants nothing else reads.
- `PlannerSettings.EnableNestLoopIndex`, its `readBool` and plan-cache fingerprint bit (`dispatch.go`), the
  `goopg.enable_nestloop_index` registration (`goopg_gucs.go`, `defaults.go`) and its `postgresql.conf.sample`
  line. `SET goopg.enable_nestloop_index` is now rejected as an unrecognised parameter, as PG rejects it.
- `flaglabels.go`: `GOOPG_NLI_COSTGATE` moves to `flagProvenanceRetired` (`retired(M0147-0051)`).
  `scripts/planner-flags.env` is regenerated. The lever count stays at 2: the variable was on the keep list, so it
  was never counted.
- Comments that described the pass as live are updated: `joinpathsmemoize.go`, `createplannl.go`, `nlicensus.go`
  (its `rewrite` route is no longer produced), `unnest.go`, `cmd/goopg/main.go`, `guc.go`, `cluster.go`, and the
  wiki's optimizer and cmd-goopg pages.

`deadcode ./cmd/...` was run to a fixed point. Nothing new became unreachable, and one already-unreachable entry
went with the deleted code. `subplan_cost.go`'s `firstIndexColumn` and `matchSetByColumnName` are left alone: they
were already reachable only from pre-existing dead code.

## Tests

- Deleted, because each tested only the removed code: `memoize_test.go`, `nl_index_join_cond_lower_test.go`,
  `nl_index_join_residual_test.go`, `nl_index_join_selectivity_test.go`, `nli_inner_unwrap_test.go` and
  `nli_shift_arms_test.go`, together with the tests in mixed files that called `tryBuildNLI`, the cost gate or
  `pickIndexCoveringLeadingPrefix`. That includes `TestNLIRuleRespectsKillSwitch` and the seam test's
  `rewriteJoinsToNLI` subtest.
- `exprwalk_inventory_test.go` drops the two deleted Expr switches.
- The flag detector's helper-wrapped-read fixture moves to `GOOPG_EXISTS_TO_ANY`.
- The `internal/utils/misc` GUC tests use `goopg.enable_opportunistic_prune` as their fixture. The postmaster
  plan-cache tests drop the retired field and GUC.

## Follow-up (noted, not filed)

`stampSemiProbePrices` (R77) priced rewrite-built SEMI/ANTI NLIs, which no longer exist, so it is probably inert.
A later recon can confirm and delete it together with nlicensus's `rewrite` route.

## Gates

See the M0147-0051 entry in `.ralph/fix_plan.md`.
