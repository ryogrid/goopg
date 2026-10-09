Task (NEXT LOOP): re-read the banner first. Owner-pending: M0146-0089 / 0084 / 0085 / 0086 / 0125 / 0126 (S2 placements),
  M0146-0068 (B8 option choice -> unblocks 27 records), SF1 cluster reload (15 RELPAGES records + Q35 SF1), M0146-0055 [!].
  S4: root M0146-0005 held; root M0146-0007 has ONE `none` slot left (last five: 0118 yes, 0119/0123/0124/0129 none).
  Root M0146-0014 (via 0014a): 0130 yes, 0131 yes, 0132 yes, 0133 yes.
  Done this loop: M0146-0133 (impl, Parent M0146-0014a) — code c12f5dc92. inputNodePathkeys had no *DistinctOn arm
  (EXPLAIN `Unique`); now descends a non-hashed one to its child (create_upper_unique_path copies subpath->pathkeys;
  distinctOnOp streams adjacent-key). Q49 both scales -> Incremental Sort (Presorted Key ('web'::text)) as PG,
  [join-order, scan-type] left (unrouted: the census recorded only the first divergence). Q54's DISTINCT leaf now
  publishes its order: the store join moved Hash -> NL + county/state Join Filter (PG's shape; categories unchanged).
Files: internal/optimizer/upperorderedinput.go (+ _test.go), docs/design/0100-0149/m0146-0133-unique-keeps-input-pathkeys.md.
Key symbols: inputNodePathkeys, DistinctOn (Hashed), distinctOnOp.
Hypothesis/Findings: after `git commit --amend`, re-read the hash before citing it; ledger rows are append-only (pre-commit
  guard rejects edits to an existing row — record corrections in a new row). The routing census records only each
  query's FIRST divergence — fixing it leaves the next one unrouted until the next M0146-0014 sweep.
  Anchors for the rest: pathKeyEqual (0134), querypathkeys.go groupClauseItems + partialaggupper.go no-split hashed
  arm (0138).
  Method: private clone tmp/c20a/data-sf025 on 5537 with GOOPG_PGSHAPED_DP_TRACE=1 (ANALYZE; EXPLAIN in one psql -f).
  pprof: GOOPG_PPROF_ADDR=127.0.0.1:6177. SF1 trace: cp -a bench/tpcds/runtime_goopg/data (5.8 GB) to tmp/, port 5591,
  delete after. PG ref: psql -p 65438 -U ryo -d tpcds025 (EXPLAIN only).
  Gates: ACCEPT_BASELINE=bench/tpch/baseline-digests.txt scripts/tpch-acceptance-arm.sh <l> <out> (one run);
  REFERENCE= PLAN_ONLY=1 scripts/tpch-estimate-audit-arm.sh <label> vs analysis/leftdeep-joins/m133-new.plans.txt;
  regress A/B tmp/m133-reg.sh (baseline tmp/m133-reg-new = HEAD); join's `ss1 left join ss2 on true` row order and
  stats_ext's stats-object listing order are nondeterministic. fix_plan IDs are backslash-escaped (`M0146\-0134`).
LINEAGE: roots M0146-0055 [!] and M0146-0005 (S4) — do NOT select/file under them.
Next step: re-read the banner; then M0146-0134 (pathkeys match through equivalence classes, Q64) — trace first.
Top-residual: join-order=48 at SF1 fire-set census (SF0.25: 39); by route: COSTTIE 31, B8 27 (owner), RELPAGES 15 (owner)
Skip-rationale: the largest routed residuals are owner-gated (B8 decision, SF1 reload) or near-ties; the filed
  mechanism tasks are the loop's remaining actionable work.
Gates run: units, tpch-spotcheck, acceptance arm (values PASS), fire set PASS, SF0.25 sweep 96/96, ea-ratchet (1),
  TPC-H plan A/B byte-identical, regress A/B 32 cases; ralph-state-guard; pgbench smoke via commit hook.
In-flight: none
