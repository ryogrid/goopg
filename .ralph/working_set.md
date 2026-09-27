Task: M0146-0027 slice 4 — runnable SetOp branch pick + PHJ-probe claim wiring (DONE, committed this loop)

Files: internal/optimizer/windowsetoppaths.go (+_test.go), internal/executor/parallel_scan.go, internal/executor/parallel_setop_claimset_test.go, docs/design/0100-0149/m0146-0027-sorted-partial-gather-merge.md, analysis/m0146/m0146-0027/slice4/, .ralph/fix_plan.md

Findings:
- Q6 routed (not fixed): goopg decorrelates correlated scalar avg() where PG keeps SubPlan — owned by M0145-0008y, blocked on M0146-0012.
- Q71 was a two-layer defect: (a) setOpBranchPick embedded PartialPathlist[0] blindly — legs lead with executor-refused ParallelHash partials, so the PathSetOp could never be gathered; now picks cheapest runnable via setOpBranchDrivingKindIsSupported. (b) attachAll's *setOp arm early-returned before wiring ParallelHash build sides on the probe path to the setOp → every participant fed the whole build into the shared table (2403=3x801 repro). Fixed via attachParallelHashBuildSides(op).
- Result: Q71 290 rows ck=e9f1fcd7c28a1f8f oracle-exact; Q14/Q76 → Gather(Parallel Append); Q55 gains Finalize→GatherMerge→Partial spine. Census parallelism 8→6.
- Residuals still open under M0146-0027: Q71 join-order (needs ledgered per-branch PHJ build), Q12/Q20/Q73 Gather Merge, Q62/Q99 Group-head epsilon, Q17/Q25/Q29 placement residue, Q77 scan-type.

Gates run: tpch-spotcheck PASS, tpcds-sf025 sweep PASS=96/0-mismatch, tpch-acceptance-arm 24 MATCH PASS, tpcds-fireset PASS (fires Q14/Q71/Q76 both arms), units PASS. Stamps code_tree c514b566dd262c7a…

Next step: none in flight — next loop re-reads the banner; M0146-0027's remaining residuals are mostly cost/routing (M0146-0007 territory) plus the ledgered per-branch PHJ build state.

In-flight: none
