Task (NEXT LOOP): re-read the banner first. Owner-pending: M0146-0089 / 0084 / 0085 / 0086 / 0125 / 0126 (S2 placements),
  M0146-0068 (B8 option choice -> unblocks 27 records), SF1 cluster reload (15 RELPAGES records + Q35 SF1 + Q4/Q11 SF1
  near-tie from M0146-0134), M0146-0055 [!].
  S4: root M0146-0005 held; root M0146-0007 has ONE `none` slot left (last five: 0118 yes, 0119/0123/0124/0129 none).
  Root M0146-0014 (via 0014a): 0130..0135 all Movement yes.
  Done this loop: M0146-0135 (impl, Parent M0146-0014a) — code a6704d1d8. get_joinrel_parampathinfo's regenerated EC
  clause (req member = unparameterised probe-side member) for the parameterised hash join: paramJoinFilterClauses ->
  Path.ParamFilter -> qual cost + Join Filter bound by the nested loop (paramSink entry with a bind callback, called
  by createNestLoopParamJoinPlan). Q95 SF1 -> [scan-type] only; results identical at both scales.
Files: internal/optimizer/{paramjoin,createplanjoin,path}.go, paramjoin_filter_test.go,
  docs/design/0100-0149/m0146-0135-param-hashjoin-ec-filter.md.
Key symbols: paramJoinFilterClauses, paramFilterClause, Path.ParamFilter, paramProbeNode.bind.
Hypothesis/Findings: restrictInfoList.ecMembers carries PG's ec_members order (for the parameterised-outer case).
  The routing census records only each query's FIRST divergence. ledger rows are append-only.
  Next tasks: 0136 is RECON (Q72 SF1 d3 probe, Q95 SF0.25 partial leaf — DP-trace on clones, no code), then 0137
  (Parallel Append non-partial member cost, Q66), 0138 (grouping sets), 0139 (hash batching), 0140 (capture `;`).
  Method: private clone tmp/c20a/data-sf025 on 5537 with GOOPG_PGSHAPED_DP_TRACE=1 (ANALYZE; EXPLAIN in one psql -f).
  SF1 trace: cp -a bench/tpcds/runtime_goopg/data (5.8 GB) to tmp/, port 5591, delete after. PG ref: psql -p 65438
  -U ryo -d tpcds025 (EXPLAIN only).
  Gates: ACCEPT_BASELINE=bench/tpch/baseline-digests.txt scripts/tpch-acceptance-arm.sh <l> <out> (one run);
  REFERENCE= PLAN_ONLY=1 scripts/tpch-estimate-audit-arm.sh <label> vs analysis/leftdeep-joins/m135-new.plans.txt;
  regress A/B tmp/m135-reg.sh (baseline tmp/m135-reg-new = HEAD); join's `ss1 left join ss2 on true` row order and
  stats_ext's stats-object listing order are nondeterministic. fix_plan IDs are backslash-escaped (`M0146\-0136`).
LINEAGE: roots M0146-0055 [!] and M0146-0005 (S4) — do NOT select/file under them.
Next step: re-read the banner; then M0146-0136 (recon: missing candidates in TPC-DS Q72 SF1 and Q95 SF0.25).
Top-residual: join-order=48 at SF1 fire-set census (SF0.25: 38); by route: COSTTIE 31, B8 27 (owner), RELPAGES 15 (owner)
Skip-rationale: the largest routed residuals are owner-gated (B8 decision, SF1 reload) or near-ties; the filed
  mechanism tasks are the loop's remaining actionable work.
Gates run: units, tpch-spotcheck, acceptance arm (values PASS), fire set PASS, SF0.25 sweep 96/96, ea-ratchet (1),
  TPC-H plan A/B byte-identical, regress A/B 32 cases; ralph-state-guard; pgbench smoke via commit hook.
In-flight: none
