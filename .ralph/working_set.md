Task: M0140-0006c-2 — widen partial-SetOp admission past bare scans.
  Hash-join branch LANDED and committed (2 commits); task stays `[ ]`
  (merge / nested-loop / bitmap branches remain, each needs its own
  identity test).

Files (committed): `internal/executor/parallel_hash_build.go`
  (`collectShareableJoins` `*setOp` arm), `operators_gather.go`
  (`collectBitmapScans` `*setOp` arm), `parallel_scan.go` (leaf-pbm
  comment), `parallel_setop_claimset_test.go` (identity + collectors),
  `internal/optimizer/gatherpaths.go` (`PathHashJoin` admission arm),
  `parallel.go` (`parallelChildren` + `StripGather` `*SetOp` arms),
  `parallel_test.go` + `windowsetoppaths_test.go` (admission/gate tests).
  Design doc: `docs/design/0100-0149/
  m0140-0006c-2-join-branch-partial-setop-admission.md` (indexed).

Key symbols: `setOpBranchDrivingKindIsSupported` (probe-side recursion),
  `HasShareableHashJoin` / `HasBitmapScan` (now SetOp-descending),
  `lookupSharedHashBuild` (sharing witness).

Finding: collector is build-once sharing, NOT row identity
  (mutation-verified: neutered collector still green on rows, fires only
  the sharing assert; neutered attach dispatch gives 390/650 for 130).
  TPC-H provably unmoved (0/22 setops); TPC-DS sweep 99/99 identical.

Next step: merge-driven branch — needs admission arm + proof no walk
  must collect through a merge outer (none does today); then NL
  (outer-ParallelWorkers/Memoize/lateral audit), then bitmap (per-branch
  pbm publication). Read this loop's design doc first.

Gates run: units PASS (44 ok, exit 0); `tpch-spotcheck` PASS (Q12=2/Q13=34);
  sf025 sweep PASS=96 MISMATCH=0 PLAN-SHAPE 99/99 identical;
  `tpch-acceptance-arm` A/B VERDICT PASS 24/24 MATCH; `ea-ratchet` N/A;
  parity transitive N/A. Pre-commit pgbench smoke PASS (hook).

In-flight: none. No shared cluster touched (acceptance arms on private
  port 5583 via online clone of `:65433`, read-only).
