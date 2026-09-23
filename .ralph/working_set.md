Task: M0145-0029 — one-relation index-path coverage (flip-triage group I).
Landed: slices 1, 2a, 2b (ea8fb4fce), 3, 4; slice 5 re-run; index_update_stats.
Open: SAOP-prefix + range (TestSAOPWithConjunctMoves); owner multiplier call.

Files (this loop): IndexScan.RangePrefix (plan.go); executor
operators_index.go lookupRangeBounds; producer pathindexrestrict.go
restrictionRangeOnColumn; lowering createplanindex.go createRangeIndexScanPlan;
consumers: operators_explain.go formatIndexCond, operators_storage.go
indexScanPredicate, unnest.go (walkPlanExprs, clonePlanReplacingOuter),
walk_export.go, subplan_lower_walk.go, subquery_parallel.go,
considerparallel.go, cardinality.go, planner.go (rebaseNLIProbeKeys,
tryPromoteIndexOnlyScan refuses), nl_index_join.go (indexOnlyNLIInner refuses);
pathbitmap.go matchBitmapIndexQuals gapless.

Findings: live flip server vs PG 18.3 — SELECT/count/subquery/UPDATE/DELETE
through prefix+range all identical. Before the IOS-promotion guard a
subquery-wrapped count returned 9476 rows for PG's 2. Separate latent
jointree-arm panic (gapped bitmap clause list) fixed. Session enable_* GUCs
looked ignored by the one-rel search on the live flip server (disabled=0 on
every path after SET enable_seqscan/bitmapscan = off) — NOT yet investigated;
verify and file before the flip.
Also open: filed wrong-results bug (composite prefix probes skip
trailing-NULL rows) in fix_plan beside the command-tag sweep bugs.

Next step: verify the enable_* observation (flip binary, SET enable_seqscan
= off, trace DPPATH disabled counts; compare legacy arm) and file it; then
SAOP-prefix + range, or the owner's multiplier answer.

Gates run: units, tpch-spotcheck (Q12=2 Q13=33), tpcds-sf025 (PASS=96,
same=99), acceptance arm (identical), tpcds-fireset (25 fires, knob plans
byte-identical), TestPort_RegressSuite — all PASS.
In-flight: none.
