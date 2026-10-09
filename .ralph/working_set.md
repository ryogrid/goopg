Task (NEXT LOOP): re-read the banner first. Owner-pending: M0146-0089 / 0084 / 0085 / 0086 / 0125 / 0126 (S2 placements),
  M0146-0068 (B8 option choice -> unblocks 27 records), SF1 cluster reload (15 RELPAGES records + Q35 SF1 + Q4/Q11 SF1
  near-tie from M0146-0134), M0146-0055 [!].
  S4: root M0146-0005 held; root M0146-0007 has ONE `none` slot left (last five: 0118 yes, 0119/0123/0124/0129 none).
  Root M0146-0014 (via 0014a): 0130..0135 Movement yes; 0136 recon (none) — filed M0146-0141, M0146-0142 (Parent 0136).
  Done this loop: M0146-0136 (recon, no code). Q72 SF1: initialRelRows re-estimates the join_collapse_limit sub-problem
  leaf `?0` (1623 rows vs its searched rel's 5), so the d3 probe loop (generated!) prices 71221 vs hash 64211 -> M0146-0141.
  Q95 SF0.25: serial pick is a faithful fuzzy tie (84297.99/startup 1092 vs 85104.24/startup 92); PG's winner is a
  Gather Merge sorted for count(DISTINCT), needing ordered-aggregate query_pathkeys -> M0146-0142.
Files: docs/design/0100-0149/m0146-0136-missing-candidates-q72-q95.md, analysis/m0146/m0146-0136/*.txt.
Key symbols: initialRelRows (joinsearch.go), searchedJoinInputRelOf, deriveQueryPathkeySets, presortedAggKeysOrAbsent.
Hypothesis/Findings: 0141's change moves every split JOIN chain and searched subquery leaf — expect a wide fire set.
  ledger rows are append-only. The routing census records only each query's FIRST divergence.
  Remaining queue in file order: 0137 (Parallel Append non-partial member cost, Q66), 0138 (grouping sets), 0139 (hash
  batching), 0140 (capture `;`), 0141, 0142.
  Method: private clone tmp/c20a/data-sf025 on 5537 with GOOPG_PGSHAPED_DP_TRACE=1 (ANALYZE; EXPLAIN in one psql -f;
  run EXPLAIN in the SAME traced session — ANALYZE is unseeded, plans vary between sessions). SF1 trace: cp -a
  bench/tpcds/runtime_goopg/data (5.8 GB) to tmp/, port 5591, delete after. PG ref: psql -p 65438 -U ryo -d tpcds025.
  Gates: ACCEPT_BASELINE=bench/tpch/baseline-digests.txt scripts/tpch-acceptance-arm.sh <l> <out> (one run);
  REFERENCE= PLAN_ONLY=1 scripts/tpch-estimate-audit-arm.sh <label> vs analysis/leftdeep-joins/m135-new.plans.txt;
  regress A/B tmp/m135-reg.sh (baseline tmp/m135-reg-new = HEAD code); join's `ss1 left join ss2 on true` row order and
  stats_ext's listing order are nondeterministic. fix_plan IDs are backslash-escaped (`M0146\-0137`).
LINEAGE: roots M0146-0055 [!] and M0146-0005 (S4) — do NOT select/file under them.
Next step: re-read the banner; then M0146-0137 (Parallel Append prices a non-partial member at per-worker cost, Q66).
Top-residual: join-order=48 at SF1 fire-set census (SF0.25: 38); by route: COSTTIE 31, B8 27 (owner), RELPAGES 15 (owner)
Skip-rationale: the largest routed residuals are owner-gated (B8 decision, SF1 reload) or near-ties; the filed
  mechanism tasks are the loop's remaining actionable work.
Gates run: none required (recon: analysis + docs only, no code); ralph-state-guard; pgbench smoke via commit hook.
In-flight: none
