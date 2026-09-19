Task: M0140-0006c-2 slice C (bitmap branch admission) — LANDED + committed;
  task now [x] complete (all four branch kinds admitted: hash / NL / merge /
  bitmap). this loop: impl b8e349802, docs 0d29c96fb. Next selectable per
  banner: item 5 continues (0006c-3 — mixed partial/non-partial SetOp arm,
  Q5).

What landed: `setOpBranchDrivingKindIsSupported`
  (internal/optimizer/gatherpaths.go:~627) gains `case PathBitmapHeapScan`
  — unconditional admit mirroring the top-level partialPathDrivingKind
  bitmap arm (dormant until a producer files a partial bitmap path; same
  dormancy the top-level arm carries). `prebuildBitmap` restructured into
  bitmapPrebuildTargets / appendBitmapPrebuildTarget (parallel_scan.go):
  a *setOp tree publishes each branch's collected bitmap to that branch's
  OWN leaf claim set (cs.setOpLeft/setOpRight.pbm, keyed off so.plan
  .Left/.Right via optimizer.HasBitmapScan) — without it every worker's
  branch bitmap attaches nothing (attachAll's return is ignored by
  design) and scans its own whole bitmap: the N-copies defect. Flat path
  keeps the exactly-one rule; nil SetOp plan / plan-tree mismatch /
  0-or->1 collected publish nothing (fail closed). Parameterized NLI
  probe bitmaps unreachable (collectBitmapScans has no
  *nestedLoopIndexJoinOp arm). Leaf pbm needed no new field — nil until
  prebuild by design; stale "nil forever / refused at planner" comments
  corrected in parallel_scan.go, operators_gather.go, gatherpaths.go,
  windowsetoppaths_test.go.
Tests: windowsetoppaths_test.go — 2 acceptance (bare bitmap branch;
  bitmap spines under hash-probe/merge-outer/NL-outer/nested-merge) +
  bitmap cases removed from refusal maps. parallel_setop_claimset_test.go
  — TestBitmapPrebuildTargetsPerBranch (8 cases: L/R/both/scans-only/
  mismatch/nil-plan/flat/multi-per-branch) +
  TestGatherOverSetOpBitmapBranchIdentity: the JOIN SEARCH emits a real
  NestedLoop(BitmapHeapScan outer, SeqScan inner) for an equality qual
  `d.grp = 3` on a low-ndistinct indexed column WITH hand-installed
  stats (bare scans take the non-search fast path and ignore enable_*
  flips; matchBitmapIndexQuals only matches EQUALITY conjuncts — range
  quals keep sel 1.0). Serial-vs-parallel identity at 1/2/4 workers, 130
  rows, bitmap branch verified under Gather. Mutation-verified: neutered
  arm -> exactly the 2 acceptance tests fail (5 assertions).
Hypothesis/Findings: completeness-only — zero Bitmap Heap Scan Parallel
  Append heads in bench/tpcds/plans-pg; sweep PLAN-SHAPE 99/99 identical.
  The earlier recon prediction ("identity test blocked on a producer")
  was wrong — join search + spliced Gather exercises the real
  prebuild/publish/attach machinery end-to-end. Remaining honest gap:
  no PLANNER-produced partial bitmap path reaches SetOp admission
  (ledgered e10-gathermerge-bitmap-untested-e2e). Follow-ups: M0142-0005a
  (Memoize under Q76 probe), 0006c-3 (Q5 mixed arm).
Key symbols: bitmapPrebuildTarget(s), appendBitmapPrebuildTarget,
  prebuildBitmap, newLeafParallelClaimSet, collectBitmapScans,
  HasBitmapScan, matchBitmapIndexQuals, probeSideIsLeft.
Gates run: build clean; optimizer + executor pkg tests green; mutation
  check PASS; tpch-spotcheck PASS Q12=2/Q13=34; SF0.25 sweep PASS=96
  MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 PLAN-SHAPE 99/99;
  acceptance-arm VERDICT PASS 24/24 MATCH (both arms PGSHAPED=1 +
  GOGC=100 + GOMEMLIMIT=8GiB — PGSHAPED=0 hits the 600s Q9 pathological
  plan); units all ok; pgbench smoke PASS x2; all 3 gate stamps
  code_tree=bcb92b69b28c = staged index (code-only tree).
In-flight: none.
WATCH: concurrent Devin loop commits to this branch — stage
  explicitly; foreign mods (ci/logs, .claude, .ralphrc, analysis/,
  postgres, third-party, .ralph/progress.json) never get committed.
