Task: M0145-0029 — one-relation index-path coverage (flip-triage group I).
Landed: slices 1, 2a, 2b, 3, 4; slice 5 re-run; index_update_stats; planner
toggles + eqsel isunique; SAOP + second-column range + NULL-key guard
(98f68622d). Group I remaining: owner's indexProbeCostMultiplier call, and
fixture/expectation edits that ride the flip commit (M0145-0008).

Files (this loop): executor operators_index.go rescanSAOP (bounds on column
1), operators_explain.go SAOP Index Cond, operators_storage.go
indexScanPredicate SAOP arm, operators_lockrows.go EPQ locality; optimizer
pathindexrestrict.go (SAOP + restrictionRangeOnColumn(…,1),
indexUnboundKeysNotNull, boundIndexColumns), pathindexonly.go
(consumingIndexClauses guard), createplanindex.go createSAOPIndexScanPlan.

Findings:
- ROOT CAUSE of the filed trailing-NULL wrong-results bug: the byte-key btree
  stores NO entry with a NULL key column (collectBTreeEntries). Jointree
  restriction producers now decline indexes with unbound nullable key
  columns; rule-based / bitmap / parameterised / ordered producers still
  exposed (bug entry updated with the root cause; real fix = NULL encoding).
- isunique for never-analysed tables needs estimated tuples (ledgered).

Next step: per banner — group I is down to owner-gated and flip-commit items,
so consider M0145-0029 done-pending-owner and move to M0145-0030 (group B:
adjudicate 11 behavioural flip-triage tests, plus the knob-default script
audit). Re-read the banner first.

Gates run: units, tpch-spotcheck (Q12=2 Q13=33), tpcds-sf025 (PASS=96,
same=99), acceptance arm (identical), tpcds-fireset (knob plans identical),
TestPort_RegressSuite, TestPort_Isolation (only the known intermittent
EvalPlanQual) — PASS.
In-flight: none.
