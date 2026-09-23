Task: M0145-0029 — one-relation index-path coverage (flip-triage group I).
Slice 1 LANDED fe3d1b0aa (plain restriction IndexScan path, equality prefix).

Files: internal/optimizer/pathindexrestrict.go (producer), createplanindex.go
(rewrapLeafDropping), pathindexclauses.go (indexPathClause.local); design doc
docs/design/0100-0149/m0145-0029-one-rel-index-path-coverage.md.

Findings: search filed NO plain restriction index path before (prebuilt +
equality-only bitmap). PG oracle: varchar witness = Bitmap Heap Scan in PG
too (stale test expectation); composite witness = Index Only Scan WITH quals
(goopg IOS producer is quals-less → slice 3). No corpus plan moves (default
sweep same=99, knob fire-set plans unchanged).

Next step: slice 2 or 3 — suggest slice 3 (index-only scan with index quals,
pathindexonly.go addIndexOnlyPaths + IndexOnlyScan lowering), verify with
TestIOS_CompositeInt4Int4 under a local flip (flip line: jointreepipeline.go
jointreePipelineFromEnv `v == "1"` → `v != "0"`; REVERT before commit).

NOTE: owner's banner/decision edits in .ralph/fix_plan.md are UNCOMMITTED
(M0145-0029/0030 + M0146 entries live only there). Commit fix_plan only via
an index blob built from HEAD + your own edits; your M0145-0029 bullet is in
the working tree and rides with the owner's commit.

Gates run: units, tpch-spotcheck, tpcds-sf025 (same=99), acceptance arm,
tpcds-fireset (24 fires) — all PASS.
In-flight: none.
