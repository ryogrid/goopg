Task (NEXT LOOP): re-read the banner first. Owner-pending: M0146-0089 / 0084 / 0085 / 0086 / 0125 / 0126 (S2 placements),
  M0146-0068 (B8 option choice -> unblocks 27 records + Q79 SF0.25), SF1 cluster reload (15 RELPAGES records + Q35 SF1 +
  Q4/Q11 SF1 near-tie from M0146-0134), M0146-0055 [!].
  S4: root M0146-0005 held; root M0146-0007 has ONE `none` slot left (last five: 0118 yes, 0119/0123/0124/0129 none).
  Root M0146-0014 (via 0014a/0136): last five 0135 yes, 0136 none, 0137 yes, 0138 yes, 0139 none.
  Done this loop: M0146-0139 (no code — premise refuted). Both clusters run work_mem=512MB (hash_mem 1 GB): neither engine
  batches Q79's customer build (trace pgbatches=1). Spill arm GOOPG_PG_HASH_TUPLE_SPILL_COST re-measured default-on: zero
  plan changes at both scales (HOLD reconfirmed). Q79 SF1 already MATCH; Q79 SF0.25 = B8 (probe 8.44 vs PG 4.63) + near-tie.
Files: docs/design/0100-0149/m0146-0139-hash-batching-premise-refuted.md (no code).
Key symbols: pgHashGeometry, pgHashTupleSpillCost, indexProbeCostMultiplier (B8).
Hypothesis/Findings: benchmark clusters use work_mem=512MB (PG ref too) — any plan-text reasoning that assumes PG's 4MB
  default work_mem / 8MB hash_mem is wrong for this corpus. ledger rows are append-only.
  Remaining queue in file order: 0140 (capture wrapper `;` on Q36/Q70/Q86), 0141 (sub-problem leaf rows), 0142
  (ordered-aggregate query_pathkeys).
  Method: private clone tmp/c20a/data-sf025 on 5537 with GOOPG_PGSHAPED_DP_TRACE=1 (ANALYZE; EXPLAIN in one psql -f;
  EXPLAIN in the SAME traced session — ANALYZE is unseeded). PG ref: psql -p 65438 -U ryo -d tpcds025 (EXPLAIN/SHOW only).
  To A/B a default-off knob in the fire set, flip its default in code (the env var reaches both arms) — restore after.
  Gates: ACCEPT_BASELINE=bench/tpch/baseline-digests.txt scripts/tpch-acceptance-arm.sh <l> <out> (one run);
  REFERENCE= PLAN_ONLY=1 scripts/tpch-estimate-audit-arm.sh <label> vs analysis/leftdeep-joins/m138-new.plans.txt;
  regress A/B tmp/m138-reg.sh (baseline tmp/m138-reg-new = HEAD). fix_plan IDs are backslash-escaped (`M0146\-0140`).
LINEAGE: roots M0146-0055 [!] and M0146-0005 (S4) — do NOT select/file under them.
Next step: re-read the banner; then M0146-0140 (strip the trailing `;` the capture wrapper leaves on Q36/Q70/Q86).
Top-residual: join-order=46 at SF1 fire-set census (SF0.25: 37); by route: COSTTIE 31, B8 27 (owner), RELPAGES 15 (owner)
Skip-rationale: the largest routed residuals are owner-gated (B8 decision, SF1 reload) or near-ties; the filed
  mechanism tasks are the loop's remaining actionable work.
Gates run: fire set probe with the spill arm flipped (no fires, tree restored; stamp FAIL only because the probe was
  unstaged); ralph-state-guard; pgbench smoke via commit hook. No code committed.
In-flight: none
