Task: M0140-0006c-3 (mixed partial/non-partial SetOp append, Q5) — LANDED,
  task now [x] complete. Item 5's chain (0006a → 0006b → 0006c + 0006c-2 +
  0006c-3) is fully closed. Next selectable per banner: item 6 —
  **M0142-0005** (per-worker Memoize under Gather; re-scoped recon exists,
  `docs/design/0100-0149/m0142-0005-recon-*.md`), then M0142-0016c, then
  M0142-0003i.

What landed: `addPartialSetOpPath` (windowsetoppaths.go) files PG's two
  parallel-aware Append arms specialised to the two-child shape — pure arm
  (allpaths.c:1538-1577, M0140-0006b's) then the MIXED arm
  (allpaths.c:1408-1453 per-child pick + :1588-1627 `pa_subpaths`): per
  branch the cheaper of cheapest partial vs cheapest parallel-safe TOTAL
  path, strict-`<` tie to nonpartial; ≥1 claimed-whole required, a branch
  offering neither kills the arm (`pa_subpaths_valid=false`). Markers:
  `Path.SetOpLeftNonPartial/SetOpRightNonPartial` → `SetOp.LeftNonPartial/
  RightNonPartial`. Costing: `appendNonPartialCost` (LPT, costsize.c:2168-
  2243) + `cost_append`'s mixed arm + cpu_tuple_cost; workers = max partial
  workers, 2-child floor, capped; when both arms file the mixed path takes
  the pure arm's Rows (pathnode.c:1417-1419's partial_rows).

Three findings the scoping recon didn't size:

  (1) `createSetOpPaths` had to accept `*Gather`/`*GatherMerge` winners —
      once a partial SetOp can win, the upper-rel `PathGather` over it IS
      the winning path; the tail check now returns `Node` and accepts all
      three.

  (2) Substitution hazard (fixed, latent pure-arm defect too): replacing a
      branch node wholesale with a searched-rel path drops boundary
      wrappers → wrong arity/rows (measured: `Project(cols=1)>Join(cols=2)`
      vs union's 1-col output). `spliceBranchEmission`
      (createplansimple.go) rebuilds the wrapper chain over the emission —
      descends *Project/*Filter/*Sort, descends-and-drops *Gather/
      *GatherMerge (nested parallelism is illegal), *Memoize is the
      emission point (typed *IndexScan child); `PathPrebuilt` children skip
      the splice. The NONPARTIAL pick is the branch's own serial plan —
      `newPrebuiltPath` seed over `StripGather(branchNode)` gated by
      `statementIsParallelSafe` (PG: gather paths are `parallel_safe=false`,
      `get_cheapest_parallel_safe_total_inner` lands on the non-gather
      serial alternative) — row-exact by construction, never spliced. The
      PARTIAL pick is admissible only when every boundary wrapper is
      per-worker-safe+descendable {Project,Filter,Sort} or dropped by the
      splice {Gather,GatherMerge} — `setOpBranchPartialChainOK`; Limit/
      Distinct/Aggregate/Memoize/unsearched refuse the partial (PG's
      partial_pathlist never carries them either) but the branch can still
      be claimed whole.

  (3) PLACEMENT — the late parity correction: PG's mixed arm lives ONLY in
      `add_paths_to_append_rel` (appendrels — flattened FROM/CTE
      union-alls, inheritance fan-outs), NEVER in `generate_union_paths`
      (prepunion.c files the pure arm alone for statement-level setops).
      First cut filed it unconditionally → TPC-DS Q66's top-level UNION ALL
      picked up an all-claimed `Gather > Append` PG cannot produce (SF0.25
      plan-diff channel caught it). Fix: `ps.ParallelStatementOK` passed in
      as `topLevel` — mixed arm files only in NESTED scopes, where the
      SetOp stands in for an appendrel PG would have built (goopg has no
      pull_up_union_all analog, so nested union-alls reach this function).
      Pure arm files at both levels (PG does). Measured: plan-diff went
      "Q66 changed" → `same=99 changed=0`. Residual documented in the
      design doc: a nested union-all PG would NOT flatten (LIMIT in
      subquery) still gets the arm — the marker sees nesting, not
      flattenability; no corpus query hits it.

Executor: `claimedWhole atomic.Bool` on each branch leaf claim set;
  `attachAll` wires the SAME flag to every participant's *setOp arm;
  `nextStreaming` CAS-claims before draining — winner drains its private
  branch serially, losers mark exhausted + close their unused child.
  `stampParallelScan` skips claimed-whole branches (no scan stamping);
  `drivingScan` treats a claimed-whole branch as satisfied (no driving
  scan needed — `gatherChildPlan` doesn't panic); `bitmapPrebuildTargets`
  excludes claimed-whole branches (sole claimer runs its own bitmap
  serially); shared-hash prebuilds unaffected (leader builds, claimer
  probes via ctx.SharedHashBuilds). `partialPathDrivingKind`'s PathSetOp
  arm is marker-aware; `unstampParallelScan` already covers *SetOp.

Tests: windowsetoppaths_test.go — pure-arm cost/worker-floor tests re-pinned
  on searched-marked fixtures; mixed-arm pricing, strict tie→nonpartial,
  all-claimed worker floor, branch-with-neither refusal, chain admissibility
  (TestSetOpBranchPartialChainOK), mixed Rows inheriting pure Rows, and the
  placement pair (MixedArmSuppressedAtTopLevel + PureArmStillFilesAtTopLevel).
  parallel_setop_claimset_test.go — claimed-whole + all-claimed identity at
  workers 1/2/4 (exact multiset, no replay/drop); anti-drift claimedWhole
  case in parallel_gather_merge_claimset_test.go.
  parallel_setop_gather_test.go — TestGatherOverSetOpPlannerWinnerIdentity:
  real planner-produced `Gather > SetOp` over comma-join branches,
  serial-vs-parallel row identity, 1-col output intact (splice witness).

Key symbols: setOpBranchPick, setOpBranchPartialChainOK, appendNonPartialCost,
  spliceBranchEmission, StripGather, statementIsParallelSafe, PathPrebuilt /
  newPrebuiltPath / seedPathForNode, SetOpLeftNonPartial/SetOpRightNonPartial
  (path.go) ↔ LeftNonPartial/RightNonPartial (plan.go), claimedWhole
  (parallel_scan.go), nextStreaming CAS (operators_setop.go),
  searchedRelOf / boundaryWalkChildren, ps.ParallelStatementOK (topLevel
  marker; planSelectWithParent clears it for nested scopes).

Gates run: build+vet clean; optimizer+executor pkg tests green; `go test
  -race -run 'SetOp|Gather' ./internal/executor` green; units gate all ok;
  tpch-spotcheck PASS Q12=2/Q13=34 (stamped); SF0.25 sweep PASS=96
  MISMATCH=0 CKMISMATCH=0 ERROR=0 PLAN-SHAPE 99/99 identical (stamped);
  acceptance-arm VERDICT PASS 24/24 MATCH vs /tmp/arm-0006c2b2-base.txt
  (PGSHAPED=1 + GOGC=100 + GOMEMLIMIT=8GiB, stamped); plan-gate 14/22
  diverged = baseline drift, NOT this change — zero SetOp/UNION/Append
  lines in the entire diff, baseline m0137-0005-rebaseline-20260915
  predates ~100 internal commits.

In-flight: none.
WATCH: concurrent Devin loop commits to this branch — stage
  explicitly; foreign mods (ci/logs, .claude, .ralphrc, analysis/,
  postgres, third-party, .ralph/progress.json) never get committed.
