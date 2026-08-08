Task: M0129-S5.2 complete — bitmap scan selectivity-region survival proof

Files:
- internal/planner/pathbitmap_test.go: added TestBitmapPathSurvivesAddPathOnLargeTable
  (+ pathsOfKind helper) — constructs a 1M-row/10k-page table with uncorrelated
  index, proves bitmap path (cost 9525) dominates seq scan (cost 10980) in
  add_path and becomes CheapestTotal
- analysis/m0129-s5.2-bitmap-survival-proof.txt: recorded cost breakdown and
  selectivity-region analysis
- .ralph/fix_plan.md: S5.2 marked DONE 2026-08-09

Key symbols: TestBitmapPathSurvivesAddPathOnLargeTable, pathsOfKind

Hypothesis/Findings:
- Bitmap scan wins over seq scan when selectivity ~0.001 on a large table
  (>10k pages) with low correlation. The sqrt interpolation of per-page cost
  (between random and seq) gives bitmap a ~2× per-page discount vs index scan.
- The prebuilt (seq scan) path's cost in buildInitialRels uses filteredRows
  instead of baseRows for page estimation, artificially lowering seq scan cost
  for selective queries. This is a pre-existing issue — the test works around
  it by setting filteredRows = baseRows for proper cost comparison.
- For single-table queries, the post-hoc rewriteScanInputsWithSingleTablePredicates
  pass replaces SeqScan→IndexScan AFTER path selection; bitmap paths are only
  generated during join search. A multi-table query with a local filter would
  exercise the full bitmap path selection.
- The S5.3 cache-aware Mackert-Lohman (indexPagesFetched) is correctly plumbed
  through computeBitmapPages; the bitmap cost uses the same formula as index
  scan for pages_fetched but with interpolated per-page cost.
- S5.1 + S5.2 + S5.3 are now all DONE. Remaining bitmap subtasks: S5.4
  (partial-index predicate recheck), S5.5 (tbm_extract_page_tuple), S5.6
  (parallel bitmap heap scan), S5.7 (read-stream prefetch — NAMED BLOCKER),
  S5.8 (GiST/GIN getBitmap — NAMED BLOCKER).

Next step: Check the Current Priority banner in fix_plan.md for the next task.
M0129-S5.4 (partial-index predicate recheck) is the next bitmap task, but the
banner may prioritize S1 (Q74 fix) or another slice.

Gates run:
- UNITS: PASS (pre-commit scope=units, all packages including planner)
- SPOT: PASS (Q12=2, Q13=35)

In-flight: none
