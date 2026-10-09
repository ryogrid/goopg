Task (NEXT LOOP): re-read the banner first. Owner-pending: M0146-0089 / 0084 / 0085 / 0086 / 0125 / 0126 (S2 placements),
  M0146-0068 (B8 option choice -> unblocks 27 records), SF1 cluster reload (15 RELPAGES records + Q35 SF1 + Q4/Q11 SF1
  near-tie from M0146-0134), M0146-0055 [!].
  S4: root M0146-0005 held; root M0146-0007 has ONE `none` slot left (last five: 0118 yes, 0119/0123/0124/0129 none).
  Root M0146-0014 (via 0014a): 0130..0135, 0137, 0138 Movement yes; 0136 recon (filed 0141, 0142).
  Done this loop: M0146-0138 (impl, Parent M0146-0014a) — code 5aab293b7. groupingSetsClauseItems: grouping sets ask the
  search for the first rollup's order (standard_qp_callback; ExtractGroupingRollups over GROUP BY slots) -> Q67
  worker-sorted Gather Merge; electOrderedGrouping now runs under a LIMIT fraction with untranslated candidates (pick after
  the ORDER BY Sort, as PG's final rel) -> Q18 SF1 sorted rollup. Q67 MATCH both scales; Q18 SF1 6 -> 3 categories.
Files: internal/optimizer/{querypathkeys,upperorderedgrouping}.go (+ tests),
  docs/design/0100-0149/m0146-0138-grouping-sets-query-pathkeys.md.
Key symbols: groupingSetsClauseItems, groupClauseItems, electOrderedGrouping (anyTranslated decline), ExtractGroupingRollups.
Hypothesis/Findings: the grouping step's fractional pick happens BEFORE the ORDER BY Sort — wherever an upper step's
  election declines, a LIMIT fraction may be judging startup that PG never sees. ledger rows are append-only.
  Remaining queue in file order: 0139 (hash batching, Q79), 0140 (capture `;`), 0141 (sub-problem leaf rows), 0142
  (ordered-aggregate query_pathkeys).
  Method: private clone tmp/c20a/data-sf025 on 5537 with GOOPG_PGSHAPED_DP_TRACE=1 (ANALYZE; EXPLAIN in one psql -f;
  EXPLAIN in the SAME traced session — ANALYZE is unseeded). SF1 trace: cp -a bench/tpcds/runtime_goopg/data (5.8 GB)
  to tmp/, port 5591, delete after. PG ref: psql -p 65438 -U ryo -d tpcds025 (EXPLAIN only).
  Gates: ACCEPT_BASELINE=bench/tpch/baseline-digests.txt scripts/tpch-acceptance-arm.sh <l> <out> (one run);
  REFERENCE= PLAN_ONLY=1 scripts/tpch-estimate-audit-arm.sh <label> vs analysis/leftdeep-joins/m138-new.plans.txt;
  regress A/B tmp/m138-reg.sh (baseline tmp/m138-reg-new = HEAD); join's `ss1 left join ss2 on true` row order and
  stats_ext's listing order are nondeterministic. fix_plan IDs are backslash-escaped (`M0146\-0139`).
LINEAGE: roots M0146-0055 [!] and M0146-0005 (S4) — do NOT select/file under them.
Next step: re-read the banner; then M0146-0139 (hash join build batching cost, Q79) — re-decide M0139-0007a's held-off
  spill arm (GOOPG_PG_HASH_TUPLE_SPILL_COST) with a fire set.
Top-residual: join-order=46 at SF1 fire-set census (SF0.25: 37); by route: COSTTIE 31, B8 27 (owner), RELPAGES 15 (owner)
Skip-rationale: the largest routed residuals are owner-gated (B8 decision, SF1 reload) or near-ties; the filed
  mechanism tasks are the loop's remaining actionable work.
Gates run: units, tpch-spotcheck, acceptance arm (values PASS), fire set PASS (twice), SF0.25 sweep 96/96, ea-ratchet (1),
  TPC-H plan A/B byte-identical, regress A/B 32 cases unchanged; ralph-state-guard; pgbench smoke via commit hook.
In-flight: none
