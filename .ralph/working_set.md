Task: M0140-0006c-2 slice B (merge branch admission) — LANDED + committed
  this loop: impl 0a5885bf6, docs b44ef1443. Task stays [ ] — the bitmap
  slice remains. Next selectable per banner: item 5 continues (0006c-2
  bitmap slice, then 0006c-3).

What landed: `setOpBranchDrivingKindIsSupported`
  (internal/optimizer/gatherpaths.go:~624) gains `case PathMergeJoin` —
  mirrors partialPathDrivingKind's merge arm guard-for-guard (E-20 Cut 3:
  RequiredOuter==0, 2 children, recurse Children[0] = the OUTER side;
  no jointype guard, runtime recheck via mergeJoinIsPartialCapable).
  Plus explicit `JoinAlgoMerge -> x.left` arms in attachParallelBitmapScan
  / attachParallelIndexScan (parallel_scan.go) replacing the
  probeSideIsLeft(BuildLeft=false) coincidence. No collector work — hash
  below merge outer is never collected (same E-20 deferral as top level).
Files: gatherpaths.go, windowsetoppaths_test.go (2 acceptance + 6
  refusals; merge-branch / merge-outer cases moved out of the bad-hash /
  bad-NL refusal maps — Jointype zero IS parser.JoinInner),
  parallel_scan.go, parallel_setop_claimset_test.go (merge identity test,
  1/2/4 workers; branch query must be comma/WHERE form — JOIN..ON plans
  the non-search fast path and ignores enable_* flips), checkpointer_test.go
  (folded flake fix: IMMEDIATE-arm 20ms absolute bound -> relative vs
  paced run; structural progresses==0 + FlushAll already prove bypass,
  buildPacer is nil for spread=false), design doc "Update 2026-09-19b",
  fix_plan ~L3025 bullet, README row tail.
Hypothesis/Findings: completeness-only slice — zero Merge Join
  Parallel Append heads in bench/tpcds/plans-pg; sweep PLAN-SHAPE 99/99
  identical. Mutation-verified: neutered arm -> exactly the 2 acceptance
  tests fail. Acceptance-arm A/B method: baseline binary from
  /tmp/goopg-wt-0006c2b worktree (pre-change, sha e27ea687 bit-identical
  rebuild); BOTH arms at GOOPG_PGSHAPED_DP=1 + GOOPG_ANALYZE_SEED=
  20260905 (PGSHAPED=0 hits the known 600s Q9 pathological plan) and
  GOGC=100 + GOMEMLIMIT=8GiB (the arm's GOGC=off grows unbounded —
  mem_guard killed the first baseline at 75% RAM).
Key symbols: setOpBranchDrivingKindIsSupported, partialPathDrivingKind,
  attachParallelScan/BitmapScan/IndexScan, mergeJoinIsPartialCapable,
  planMergeForced, planTreeHasMergeUnderGather.
Next step: bitmap branch (final 0006c-2 slice — needs per-branch pbm
  publication in prebuildBitmap + leaf claim sets cs.setOpLeft/Right.pbm
  + newLeafParallelClaimSet bitmap state; AND a partial-bitmap producer
  does not exist yet so an end-to-end identity test is untestable — flag
  honestly), then 0006c-3 (mixed partial/non-partial arm, Q5).
Gates run: build clean; optimizer + executor pkg tests green; mutation
  check PASS; tpch-spotcheck PASS Q12=2/Q13=34; SF0.25 sweep PASS=96
  MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 PLAN-SHAPE 99/99;
  acceptance-arm VERDICT PASS 24/24 MATCH; units all ok (post flake
  fix); pgbench smoke PASS x2; all 3 gate stamps code_tree=
  efec6026fe3e867f = pre-docs index.
In-flight: none.
WATCH: concurrent Devin loop commits to this branch — stage
  explicitly; foreign mods (ci/logs, .claude, .ralphrc, analysis/,
  postgres, third-party) never get committed.
