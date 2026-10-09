Task (NEXT LOOP): re-read the banner first. Owner-pending: M0146-0089 / 0084 / 0085 / 0086 / 0125 / 0126 (S2 placements),
  M0146-0068 (B8 option choice -> unblocks 27 records), SF1 cluster reload (15 RELPAGES records + Q35 SF1 + Q4/Q11 SF1
  near-tie from M0146-0134), M0146-0055 [!].
  S4: root M0146-0005 held; root M0146-0007 has ONE `none` slot left (last five: 0118 yes, 0119/0123/0124/0129 none).
  Root M0146-0014 (via 0014a): 0130..0134 all Movement yes.
  Done this loop: M0146-0134 (impl, Parent M0146-0014a) — code dd7e9df07. Five gaps for Q64's Incremental Sort:
  GEQO freshEvalCtx dropped queryPathkeys/parallelModeOK/cat/itemSpans/...; group pathkeys kept FD columns
  (pruneUselessGroupPathkeys); syntactic pathkey compare at truncation/grouping/ORDER BY (countContainedIn over
  equivalentWithin, RelOptInfo.SearchCandidateClasses); incremental-sort group estimate lacked the 2-tuple clamp;
  seed arm added a full Sort beside the Incremental Sort. SF0.25: Q4 MATCH, Q58/Q64 better. SF1: Q58/Q64 better,
  Q4/Q11 +[join-method, qual-placement] (fuzzy near-tie under SF1 drift, ledgered).
Files: internal/optimizer/{geqo,groupingpaths,incrementalsortpaths,pathkeys_useful,querypathkeys,path,upperordered,
  upperorderedgrouping}.go, eqclass_pathkeys_test.go, docs/design/0100-0149/m0146-0134-pathkeys-through-equivalence-classes.md.
Key symbols: freshEvalCtx, pruneUselessGroupPathkeys, pathkeyUsefulness.countContainedIn, SearchCandidateClasses.
Hypothesis/Findings: a DP-trace `joinsearch.prebuilt pathkeys=0` line is printed BEFORE addCTEScanPathkeys stamps the
  leaf's ordering — not evidence of a missing order. GEQO (>12 rels, e.g. Q64's cross_sales) floods the trace; use a
  temporary env-gated debug print instead (remove before staging). ledger rows are append-only. The routing census
  records only each query's FIRST divergence. Anchors for the rest: querypathkeys.go groupClauseItems +
  partialaggupper.go no-split hashed arm (0138).
  Method: private clone tmp/c20a/data-sf025 on 5537 with GOOPG_PGSHAPED_DP_TRACE=1 (ANALYZE; EXPLAIN in one psql -f).
  SF1 trace: cp -a bench/tpcds/runtime_goopg/data (5.8 GB) to tmp/, port 5591, delete after. PG ref: psql -p 65438
  -U ryo -d tpcds025 (EXPLAIN only).
  Gates: ACCEPT_BASELINE=bench/tpch/baseline-digests.txt scripts/tpch-acceptance-arm.sh <l> <out> (one run);
  REFERENCE= PLAN_ONLY=1 scripts/tpch-estimate-audit-arm.sh <label> vs analysis/leftdeep-joins/m134-new.plans.txt;
  regress A/B tmp/m134-reg.sh (baseline tmp/m134-reg-new = HEAD); join's `ss1 left join ss2 on true` row order and
  stats_ext's stats-object listing order are nondeterministic. fix_plan IDs are backslash-escaped (`M0146\-0135`).
LINEAGE: roots M0146-0055 [!] and M0146-0005 (S4) — do NOT select/file under them.
Next step: re-read the banner; then M0146-0135 (parameterised semijoin inner keeps the class's join filter, Q95) —
  trace first.
Top-residual: join-order=48 at SF1 fire-set census (SF0.25: 38); by route: COSTTIE 31, B8 27 (owner), RELPAGES 15 (owner)
Skip-rationale: the largest routed residuals are owner-gated (B8 decision, SF1 reload) or near-ties; the filed
  mechanism tasks are the loop's remaining actionable work.
Gates run: units, tpch-spotcheck, acceptance arm (values PASS), fire set PASS (twice; the first run showed Q58's
  ORDER BY arm still syntactic — fixed), SF0.25 sweep 96/96, ea-ratchet (1), TPC-H plan A/B byte-identical, regress
  A/B 32 cases; ralph-state-guard; pgbench smoke via commit hook.
In-flight: none
