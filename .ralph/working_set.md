Task: M0145-0029 — one-relation index-path coverage (flip-triage group I).
Slices 1 (fe3d1b0aa), 2a (fc716f1f8), 3 (6d50210f4), 4 (729027e28) LANDED;
2b and 5 open.

Files: internal/optimizer/pathindexrestrict.go (restrictionEqualityPrefix,
restrictionLeadingRange, restrictionLeadingSAOP), pathindexclauses.go
(indexPathClause.op/saop/local), pathindexonly.go (IOS with clauses),
createplanindex.go (createRangeIndexScanPlan, createSAOPIndexScanPlan).
Design doc docs/design/0100-0149/m0145-0029-one-rel-index-path-coverage.md.

Findings: knob-arm Q45 SubPlan rows 18000 -> 10 (PG 10) via the SAOP path,
cost-only fire. Group-I witnesses mostly fail on fixture artefacts
(no-stats 1-row tables elect seq; IOS tests need GOOPG_INDEX_PROBE_MULT=1).
FILED BUG (fix_plan, beside the command-tag sweep bugs), now widened:
EVERY composite prefix probe (IndexScan Key/short Keys/SAOPKeys and
IndexOnlyScan) skips rows whose trailing key column is NULL — wrong results.
Suspect compositeUpperBound padding (internal/executor/pgindex_btree.go:122).

Next step: per banner continue M0145-0029 — slice 5 (re-run group I under a
local flip; per test, update stale expectations to PG's shape with oracle
evidence, or give fixtures real stats / pin enable_* as PG regress does), or
2b. Local flip: jointreepipeline.go:27 `v == "1"` → `v != "0"`; REVERT
before commit. Optimizer tests may set the package var jointreePipeline.

Gates run: units, tpch-spotcheck (Q12=2 Q13=33), tpcds-sf025 (PASS=96,
same=99), acceptance arm (identical), tpcds-fireset (25 fires incl. Q45,
introduced=none) — all PASS.
In-flight: none.
