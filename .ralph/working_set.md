Task: M0142-0005a (fused NLI+Memoize as a Gather-driving kind, TPC-DS
  Q34/Q73) — LANDED, task now [x] complete. Next selectable per banner:
  item 6 continues — **M0142-0016c**, then **M0142-0003i** (0003i only
  after its own prereqs). Open adjacent: M-NIGHTLY-instrumentscope-race-fix
  (pre-existing HEAD race, reproduced at base — see Blocked below);
  Q76's hash branch wants this Memoize arm (fix_plan :2979, :3015).

What landed: `partialPathDrivingKind`'s PathNestLoop lateral-probe arm
  (gatherpaths.go) unwraps ONE PathMemoize layer — Children[0] is always
  the wrapped probe (getMemoizePath, joinpathsmemoize.go:292-303) — and
  re-runs the bare-probe check on the child: PathIndexScan + IndexClauses,
  JoinInner only, calcNestloopRequiredOuter relid re-check. Whole-inner
  PathMemoize stays refused (a parameterless memoize means a producer
  changed — fail closed). setOpBranchDrivingKindIsSupported's PathNestLoop
  arm mirrors it guard-for-guard (its header claimed the mirror; the
  general arm's widening would otherwise falsify it).

Site-list correction vs the recon's sizing: the memoized NLI is the FUSED
  *NestedLoopIndexJoin{Outer, Inner: *IndexScan, InnerMemo: *Memoize}
  node — a *Memoize can NEVER sit under Join.Right and a *memoizeOp never
  under joinOp.right (createPlan panics on free-standing PathMemoize; the
  decomposed Join{Lateral} shape is bare-probe-only per R25). So the
  recon's named node/executor twins (lateralProbeIsPartialProbe /
  lateralProbeJoinPartial) would have been dead code — attempted,
  verified unreachable, reverted. The real work was a NEW sibling set:
  NestedLoopIndexJoinIsPartialCapable (parallel.go, exported — the single
  verdict every executor walk re-runs: INNER only, non-nil children,
  Inner = bare keyed probe per lateralProbeIsPartialProbe, no SAOP/range/
  bitmap; InnerMemo is irrelevant to the verdict). Arms added, all
  descending Outer literally: drivingScan, stampParallelScan,
  unstampParallelScan (both sides, its enforcement-inverse convention),
  HasShareableHashJoin, drivingScanCrossesSort (guard-for-guard;
  unreachable via findPartialSubtree today — NLI is terminatesPartial).
  Executor: attachParallelScan / attachParallelBitmapScan /
  attachParallelIndexScan (parallel_scan.go), collectShareableJoins
  (parallel_hash_build.go), collectBitmapScans (operators_gather.go) —
  each `if !optimizer.NestedLoopIndexJoinIsPartialCapable(x.plan) return
  false; return <walk>(x.outer, …)`. Claim state never crosses to the
  re-probed inner; each worker's memoizeOp/kvcache is private by
  construction (PG parity: nodeMemoize.c:1190-1260's DSM shuttles only
  instrumentation counters).

Tests: internal/optimizer/partial_nli_memoize_test.go +
  internal/executor/parallel_nli_memoize_test.go — bare+memoized
  admission, refusal matrix (non-INNER jointypes, bitmap/SAOP/unkeyed/
  ranged inner, nil plan/state), outer-only claim on all three walks,
  shared-hash + bitmap collection through the outer. Race-clean.

Key symbols: NestedLoopIndexJoinIsPartialCapable (parallel.go — executor
  calls it directly, no twin to drift), PathMemoize unwrap in
  partialPathDrivingKind + setOpBranchDrivingKindIsSupported
  (gatherpaths.go), NestedLoopIndexJoin.InnerMemo field (plan.go;
  createplannl.go:196-209 — memoized stays fused, bare decomposes).

Gates run: build+vet clean; optimizer+executor pkg tests green; new-test
  `go test -race` green; units gate all ok; tpch-spotcheck PASS
  Q12=2/Q13=34 (stamped); SF0.25 sweep PASS=96 MISMATCH=0 CKMISMATCH=0
  TIMEOUT=0 (stamped) — **Q34 + Q73 both flipped to the exact PG reference
  shapes** (Gather > NL > … > Memoize > Index Scan; 60/99 plans changed
  total, all row-correct; Q66 kept its Gather > Append pure-arm shape);
  acceptance-arm VERDICT PASS 24/24 MATCH vs /tmp/arm-0006c3-staged.txt
  (PGSHAPED=1 + GOGC=100 + GOMEMLIMIT=8GiB, stamped);
  GOOPG_PGSHAPED_DP_TRACE=1 Q34 on private :5533 clone — accepted
  producer=gather DPPATH lines incl. relids={0,1,2,3} rows=136
  total=17477.699… matching the emitted Gather cost=1182.47..17477.70.
  plan-gate 14/22 diverged = baseline drift, NOT this change — the live
  :65433 binary (built 09-19 03:11) predates the staged work, so the diff
  measured HEAD-vs-m0137-0005-rebaseline-20260915, same 14/22 the prior
  loop recorded; spot-verified Q12's divergent NL shape is exactly PG
  18.3's own plan (bare-probe flips ride the pre-existing decomposed
  Join{Lateral} path this change does not touch).

Blocked (recorded, separate task): `make race-gate` red at HEAD —
  pre-existing instrumentScope data race (worker exec-time lazy SubPlan
  Build → maybeInstrument reads the package-global scope vs
  buildUnderNilScope/buildUnderFreshScope writes under instrumentScopeMu;
  witness TestParallelLateralProbeIdentity). Reproduced at the base commit
  with this change stashed — unrelated. Already filed as
  M-NIGHTLY-instrumentscope-race-fix with
  TestExplainAnalyzeSubPlanScopeObservation as its prescribed probe.

In-flight: none.
WATCH: concurrent Devin loop commits to this branch — stage
  explicitly; foreign mods (ci/logs, .claude, .ralphrc, analysis/,
  postgres, third-party, .ralph/progress.json) never get committed.
