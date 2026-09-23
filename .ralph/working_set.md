Task: M0145-0029 — one-relation index-path coverage (flip-triage group I).
Slices 1 (fe3d1b0aa), 2a (fc716f1f8), 3 (6d50210f4) LANDED; 2b, 4, 5 open.

Files: internal/optimizer/pathindexrestrict.go (restrictionEqualityPrefix,
restrictionLeadingRange, rangeIndexSelectivity), pathindexclauses.go
(indexPathClause.op/local), pathindexonly.go (IOS with clauses),
createplanindex.go (createRangeIndexScanPlan, rewrapLeafDropping). Design
doc docs/design/0100-0149/m0145-0029-one-rel-index-path-coverage.md.

Key symbols: addRestrictionIndexPaths, addOneRestrictionIndexPath,
consumingIndexClauses, restrictionPathIsIndexOnly, createIndexScanPlan.

Findings: range bounds now work on the leading column (single-col index drops
the Filter; composite keeps a recheck). IOS witnesses need
GOOPG_INDEX_PROBE_MULT=1 to beat the bitmap (owner-owned multiplier).
NEW BUG FILED (fix_plan, next to the command-tag sweep bugs): composite
index-only PREFIX probe skips trailing-NULL entries — wrong results on the
default pipeline (SELECT a FROM r2 WHERE a = 10 → 3, PG 4). Suspect
compositeUpperBound padding (pgindex_btree.go:122).

Next step: per banner, continue M0145-0029 — slice 4 (SAOP `col IN (…)` as
restriction index quals → IndexScan.SAOPKeys; witness TestSAOPWithConjunctMoves
under a local flip) or 2b. Local flip line: jointreepipeline.go:27
`v == "1"` → `v != "0"`; REVERT before commit. Optimizer tests can set the
package var jointreePipeline directly (see TestRestrictionRangeIndexScan…).

Gates run: units, tpch-spotcheck (Q12=2 Q13=33), tpcds-sf025 (PASS=96,
same=99), acceptance arm (identical), tpcds-fireset (24 fires,
introduced=none; knob plans unchanged) — all PASS.
In-flight: none.
