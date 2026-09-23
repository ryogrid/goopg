Task: M0145-0029 — one-relation index-path coverage (flip-triage group I).
Slices 1 (fe3d1b0aa) and 3 (6d50210f4) LANDED; slices 2, 4, 5 open.

Files: internal/optimizer/pathindexrestrict.go (plain restriction producer,
restrictionEqualityPrefix, restrictionIndexSelectivity), pathindexonly.go
(consumingIndexClauses, restrictionPathIsIndexOnly, IOS with clauses),
createplanindex.go (IOS lowering Key/Keys; rewrapLeafDropping), plan.go
(IndexOnlyScan embeds searchedTree). Design doc
docs/design/0100-0149/m0145-0029-one-rel-index-path-coverage.md.

Findings: under a local flip, TestIOS_* (4) and TestArrayIndexOnlyScan…
elect PG's IOS shape only at GOOPG_INDEX_PROBE_MULT=1; at the shipped 2 the
bitmap wins (PG IOS 8.17 vs bitmap 8.18; goopg 16.27 vs 12.27; the multiplier
doubles the index heap fetch but not the bitmap heap page; owner-owned knob).
TestIndexOnlyDeformColdAndVisible: 4 analysed rows, seq wins as in PG → stale.
The varchar witness is a bitmap in PG too → stale. No corpus plan moved.

Next step: slice 2 — range bounds (< <= > >= BETWEEN) as index quals on the
plain restriction producer (extend restrictionEqualityPrefix with one trailing
range column → IndexScan LowKey/HighKey/LowOp/HighOp) and the bitmap producer
(matchBitmapIndexQuals). Witness: TestSAOPWithConjunctMoves partly needs slice
4 (SAOP). Local flip line: jointreepipeline.go jointreePipelineFromEnv
`v == "1"` → `v != "0"`; REVERT before commit.

fix_plan.md: the owner committed its edits (0076c2455); edit it normally.

Gates run: units, tpch-spotcheck (Q12=2 Q13=33), tpcds-sf025 (PASS=96,
same=99), acceptance arm (identical), tpcds-fireset (24 fires, introduced=none)
— all PASS; pgbench smoke PASS on commit.
In-flight: none.
