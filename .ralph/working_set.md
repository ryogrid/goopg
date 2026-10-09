Task (NEXT LOOP): re-read the banner first. Owner-pending: M0146-0089 / 0084 / 0085 / 0086 / 0125 / 0126 (S2 placements),
  M0146-0068 (B8 option choice -> unblocks 27 records), SF1 cluster reload (15 RELPAGES records + Q35 SF1), M0146-0055 [!].
  S4: root M0146-0005 held; root M0146-0007 has ONE `none` slot left (last five: 0118 yes, 0119/0123/0124/0129 none).
  Root M0146-0014 (via 0014a): 0130 yes, 0131 yes.
  Done this loop: M0146-0131 (impl, Parent M0146-0014a) — code a949c38e5. The search + grouping already built PG's
  Q35 candidate (GroupAgg over Incremental Sort presorted on ca_state); the ORDERED rel dropped it because
  groupingEmissionPathkeys translated only Sort/GatherMerge children. Now also PathIncrementalSort + presorted
  PathPrebuilt (node twin aggregateEmissionPathkeys already did). Second gap exposed: rewriteExistsToAnyNode had no
  IncrementalSort/Memoize/Result arm — Q35's OR of EXISTS ran per row (684 ms -> >600 s); fixed, 3.5 s.
  Q35 SF0.25 -> MATCH; SF1 blocked by RELPAGES (customer_address ~955 pages < min_parallel_table_scan_size).
Files: internal/optimizer/{upperorderedgrouping,exists_to_any}.go + tests upperordered_test.go / exists_to_any_test.go,
  docs/design/0100-0149/m0146-0131-ordered-grouping-input-under-limit.md.
Key symbols: groupingEmissionPathkeys, electOrderedGrouping, addGroupingPaths searchcand arm, rewriteExistsToAnyNode.
Hypothesis/Findings: "unwinnable path = untested path" again — a newly electable shape exposed a fail-open walker.
  Anchors for the rest: considerparallel.go computeParallelWorker (0132), upperorderedinput.go inputNodePathkeys
  (0133), pathKeyEqual (0134), querypathkeys.go groupClauseItems + partialaggupper.go no-split hashed arm (0138).
  Method: private clone tmp/c20a/data-sf025 on 5537 with GOOPG_PGSHAPED_DP_TRACE=1 (ANALYZE; EXPLAIN in one psql
  -f); pprof via GOOPG_PPROF_ADDR=127.0.0.1:6177 (6060 is taken). SF1 trace: cp -a bench/tpcds/runtime_goopg/data
  (5.8 GB, offline) to tmp/, port 5591, delete after. PG reference: psql -p 65438 -U ryo -d tpcds025 (EXPLAIN only).
  Gates: acceptance arm in ONE run = ACCEPT_BASELINE=bench/tpch/baseline-digests.txt scripts/tpch-acceptance-arm.sh
  <l> <out> (it builds; NO_BUILD only to reuse); TPC-H plan A/B: REFERENCE= PLAN_ONLY=1
  scripts/tpch-estimate-audit-arm.sh <label>, diff vs analysis/leftdeep-joins/m131-new.plans.txt; regress A/B:
  tmp/m131-reg.sh, baseline tmp/m131-reg-new = HEAD. Regress join's `ss1 left join ss2 on true` row order and
  stats_ext's stats-object listing order are nondeterministic. fix_plan IDs are backslash-escaped (`M0146\-0132`).
  Amend a message without picking up the driver's staged files: git commit --amend --only -F <msg>.
LINEAGE: roots M0146-0055 [!] and M0146-0005 (S4) — do NOT select/file under them.
Next step: re-read the banner; then take M0146-0132..0140 in file order (item 3, root 0014 has budget) — 0132
  (appendrel members skip the min_parallel_table_scan_size cutoff, Q5 SF0.25) is next; verify with a trace first.
Top-residual: join-order=48 at SF1 fire-set census (SF0.25: 39); by route: COSTTIE 31, B8 27 (owner), RELPAGES 15 (owner)
Skip-rationale: the largest routed residuals are owner-gated (B8 decision, SF1 reload) or near-ties; the filed
  mechanism tasks are the loop's remaining actionable work.
Gates run: units, tpch-spotcheck, acceptance arm (values PASS), fire set PASS (first run FAILED: Q35 timeout, fixed
  by the walker arms), SF0.25 sweep 96/96, ea-ratchet (1), TPC-H plan A/B byte-identical, regress A/B 32 cases;
  ralph-state-guard; pgbench smoke via commit hook.
In-flight: none
