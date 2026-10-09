Task (NEXT LOOP): re-read the banner first. Owner-pending: M0146-0089 / 0084 / 0085 / 0086 / 0125 / 0126 (S2 placements),
  M0146-0068 (B8 option choice -> unblocks 27 records), SF1 cluster reload (15 RELPAGES records + Q35 SF1 + Q4/Q11 SF1
  near-tie from M0146-0134), M0146-0055 [!].
  S4: root M0146-0005 held; root M0146-0007 has ONE `none` slot left (last five: 0118 yes, 0119/0123/0124/0129 none).
  Root M0146-0014 (via 0014a): 0130..0135, 0137 Movement yes; 0136 recon (filed 0141, 0142).
  Done this loop: M0146-0137 (impl, Parent M0146-0014a) — code e26c2f481. setOpBranchPick(…, keptSubquery): a UNION ALL
  member PG keeps as a subquery RTE (SetOp.appendMemberLeft/Right) that aggregates over a Gather (aggregatesOverGather)
  offers no non-partial pick; Q66 -> MATCH at both scales. Bounded: dropping the StripGather stand-in for kept-subquery
  JOIN members too cost Q76 its Parallel Append (goopg's branch partial pick misses Q76's store member, ledgered).
Files: internal/optimizer/windowsetoppaths.go (+ _test.go),
  docs/design/0100-0149/m0146-0137-parallel-append-grouping-member.md.
Key symbols: setOpBranchPick, aggregatesOverGather, StripGather, cheapestRunnableSetOpBranchPartial.
Hypothesis/Findings: StripGather's stand-in keeps per-worker PlanCost stamps (a stripped scan prints its partial cost) —
  a tie with the branch's own partial pick lands in the non-partial list (Q76 web member, baseline). ledger rows are
  append-only. The routing census records only each query's FIRST divergence.
  Remaining queue in file order: 0138 (grouping sets), 0139 (hash batching), 0140 (capture `;`), 0141, 0142.
  Method: private clone tmp/c20a/data-sf025 on 5537 with GOOPG_PGSHAPED_DP_TRACE=1 (ANALYZE; EXPLAIN in one psql -f;
  EXPLAIN in the SAME traced session — ANALYZE is unseeded). SF1 trace: cp -a bench/tpcds/runtime_goopg/data (5.8 GB)
  to tmp/, port 5591, delete after. PG ref: psql -p 65438 -U ryo -d tpcds025 (EXPLAIN only).
  Gates: ACCEPT_BASELINE=bench/tpch/baseline-digests.txt scripts/tpch-acceptance-arm.sh <l> <out> (one run);
  REFERENCE= PLAN_ONLY=1 scripts/tpch-estimate-audit-arm.sh <label> vs analysis/leftdeep-joins/m137-new.plans.txt;
  regress A/B tmp/m137-reg.sh (baseline tmp/m137-reg-new = HEAD); join's `ss1 left join ss2 on true` row order and
  stats_ext's listing order are nondeterministic. fix_plan IDs are backslash-escaped (`M0146\-0138`).
LINEAGE: roots M0146-0055 [!] and M0146-0005 (S4) — do NOT select/file under them.
Next step: re-read the banner; then M0146-0138 (grouping sets: Gather Merge order, per-set hashed cost, hash_mem; Q67/Q18).
Top-residual: join-order=47 at SF1 fire-set census (SF0.25: 37); by route: COSTTIE 31, B8 27 (owner), RELPAGES 15 (owner)
Skip-rationale: the largest routed residuals are owner-gated (B8 decision, SF1 reload) or near-ties; the filed
  mechanism tasks are the loop's remaining actionable work.
Gates run: units, tpch-spotcheck, acceptance arm (values PASS), fire set PASS (first run regressed Q76 — rule narrowed),
  SF0.25 sweep 96/96, ea-ratchet (1), TPC-H plan A/B byte-identical, regress A/B 32 cases; ralph-state-guard; pgbench
  smoke via commit hook.
In-flight: none
