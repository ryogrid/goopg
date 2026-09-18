Task: M0141-S2b-13 — restore PG `COSTS_EQUAL` semantics in the serial
  pathlist comparator + `partialaggupper.go` SORTED-before-HASHED arm order
  (Kind: impl, Parent: M0141-S2b-12). DONE, committed this loop. Selected as
  the open descendant of banner item 3's S2b-6-resume lineage.

Files (committed): `internal/optimizer/path.go` (comparePaths rewritten as a
  direct port of `add_path`'s pairwise adjudication, pathnode.c:475-610 —
  COSTS_DIFFERENT keeps both, parameterised paths pretend NIL pathkeys,
  COSTS_BETTER* outer-rel/rows/parallel-safe guards, COSTS_EQUAL →
  parallel_safe → rows → 1.0000000001 tight-fuzz → keep-old),
  `internal/optimizer/partialaggupper.go` (no-split arm now inserts SORTED
  family before HASHED, matching can_sort-before-can_hash),
  `internal/optimizer/{path,groupingpaths,pathindexordered,upperordered}_test.go`,
  `internal/executor/{explain_alias_source_keys,parallel_hash_path_consumer}_test.go`
  (re-asserted to PG outcomes; C19f fixture recalibrated —
  MaxParallelWorkersPerGather 4 + CPUTupleCost 1.0 — because PG divides only
  cpu cost, not disk, for parallel paths),
  `docs/design/0100-0149/m0141-s2b-13-costs-equal-restore.md`,
  `docs/design/README.md`, `analysis/planner-refactor-take3/
  c20a-estimator-census-20260915/ea-baseline.txt` (re-pinned 70→76),
  `.ralph/fix_plan.md` (S2b-13 closed; S2b-14 triage + S2b-15 gather
  row-stamp filed), `.ralph/working_set.md`, `.ralph/deferral_ledger.md`.

Key symbols: `comparePaths`/`comparePathCostsFuzzily`/`addToPathlist`
  (path.go), `addPartialGroupingPaths` no-split arm (partialaggupper.go),
  `electOrderedGrouping` (upperorderedgrouping.go:202 — declines at <2
  legitimately-surviving candidates).

Gates: units PASS; tpch-spotcheck PASS (Q12=2/Q13=34) + stamp;
  tpch-acceptance-arm 24/24 vs HEAD + stamp; tpcds-sf025 sweep PASS=96
  MISMATCH=0 CKMISMATCH=0 ERROR=0 SKIP=3 + stamp (64 plans changed,
  154s→149s); TPC-H parity match 7→8 (same-epoch HEAD also 8, verdict sets
  identical); TPC-DS parity match 2=2 (Q9/Q41); ea-ratchet 70→76 — all 13
  NEW are relset-key churn (estimator untouched), re-pinned per M0142-0012
  precedent, re-run PASS; Q74 sanity 1.8s SF0.25, no NL resurrection.

Movement: TPC-DS aggregation-strategy divergences 71→44, TPC-H 8→3; TPC-H
  match 7→8; adverse drift within the predicted ±3 band (TPC-H join-order
  +2/join-method +1/scan-type +1; TPC-DS qual-placement +3/parameterisation
  +1/rendering +1).

Open follow-ups (both filed in fix_plan):
  - M0141-S2b-14 (recon): triage the 13 NEW ea-ratchet findings — inline
    classification says all are re-keyed known-class estimates, not new
    mechanisms; verify per-query against `bench/tpcds/plans-pg/Q*.txt`.
  - M0141-S2b-15 (impl, small): `makeGatherPath`/`makeGatherMergePath`
    always stamp `computeGatherRows(sub)`; PG's `cost_gather` stamps
    `rel->rows` for scan/join rels (`override_rows=false`,
    allpaths.c:3091-3112) and only calls `compute_gather_rows` for
    upper-rel gathers — off-by-one visible only inside fuzzy-tie rows
    tie-breaks and EXPLAIN `rows=`.
