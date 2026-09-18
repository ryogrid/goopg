Task: M0140-0006c-2 slice A (NL branch admission) — LANDED + committed
  this loop: code cbd2ea1de, docs d873bbe83. Task stays [ ] — merge +
  bitmap slices remain. Next selectable per banner: item 5 continues
  (0006c-2 merge slice, then bitmap, then 0006c-3).

What landed: `setOpBranchDrivingKindIsSupported`
  (internal/optimizer/gatherpaths.go:~620) gains `case PathNestLoop` —
  mirrors partialPathDrivingKind's NL arm guard-for-guard (JoinInner,
  RequiredOuter==0, 2 children, V5 outer, no PathMemoize inner;
  whole-inner→recurse outer, R95 probe inner→IndexClauses+relids+
  calcNestloopRequiredOuter==0). Recursion stays branch-local (merge/
  bitmap outers still refuse). NO executor change needed — the three
  attachParallel* walks already carry JoinAlgoNestedLoop.
Files: gatherpaths.go, windowsetoppaths_test.go (3 acceptance + 11
  refusals; `nestloop-branch` moved out of bad-hash refusal map —
  Jointype zero IS parser.JoinInner), parallel_setop_claimset_test.go
  (NL identity test + planTreeHasNestedLoopUnderGather), design doc
  0006c-2 "Update 2026-09-19", fix_plan ~L2992 bullet.
Hypothesis/Findings: corpus unchanged — Q76 still needs M0142-0005a
  (Memoize under probe NL) before it flips; sweep PLAN-SHAPE 99/99
  identical confirms slice A wins nothing at current costs (same as
  hash slice). Mutation-verified: neutered arm → exactly the 3
  acceptance tests fail to PathPrebuilt.
Key symbols: setOpBranchDrivingKindIsSupported, partialPathDrivingKind,
  calcNestloopRequiredOuter, nlClassifyFixture/Path (test helpers),
  latClassifyFixture/Path + latParamInner (R95 probe helpers),
  ordinaryInnerNestedLoopPartial/lateralProbeJoinPartial (exec twins).
Next step: merge branch (slice B — one arm + the probeSideIsLeft-
  coincidence flag in attachParallelIndexScan/BitmapScan), then bitmap
  (needs per-branch pbm publication AND a partial-bitmap producer —
  untestable end-to-end until one exists, flag honestly), then 0006c-3.
Gates run: build clean; go test optimizer 2.7s + executor 13.4s green;
  tpch-spotcheck PASS real run Q12=2/Q13=34 (first since :65433
  recovery); SF0.25 sweep PASS=96 MISMATCH=0 PLAN-SHAPE 99/99; units
  all ok; acceptance-arm 24/24 MATCH vs fresh HEAD baseline; mutation
  check PASS; all 3 gate stamps code_tree=ee38e23b06f6b65e = index.
In-flight: none.
WATCH: concurrent Devin loop commits to this branch — stage
  explicitly; foreign mods (ci/logs, .claude, .ralphrc, analysis/,
  postgres, third-party) never get committed.
