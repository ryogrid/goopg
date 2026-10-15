Task: M0146-0197 — parameterised index path per eligible index. Leading-prefix half LANDED 7e9900a35 (pushed);
  task stays [ ] for the skip-scan half (pickIndexSkipRun still single-index).
  - Inert on today's corpora: TPC-H 12->12, SF0.25 80->80, plan text byte-identical; fire-set SF1 A/B no change.
  - Gates: units, tpch-spotcheck, acceptance arm PASS (23 MATCH + Q5 EXPECTED-ERROR), sf025 PASS=99, fire set PASS.
Context (M0146-0195 family):
  - 7b356ec04 dedup-allocation leak fix; f1fd9a8a9 M0146-0178 slice 2 (index pages via catalog database).
  - Clone measurement: compact fk indexes in use → TPC-H 12 -> 14 (Q3/Q5/Q7 MATCH, Q10 lost).
  - ESCALATION be711c43e RESOLVED by owner 2026-10-15 evening: :65433 now serves goopg-bin built at 7e9900a35
    (sha256 8c47fc59…; pid 561965, readlink-verified); INSERT reload redone — idx_lineitem_orderkey_fkidx
    117050->16641 (PG 16468), order_customer_fkidx 28617->2220 (PG 4121). Q5 completes in ~1s, digest ==
    PG :65432; baseline Q5 re-pinned from XFAIL to the real value.
NEXT LOOP:
  1. Re-read banner; check :65433's serving binary + fk-index occupancy (relfilenode 16452, read-only parse) for the
     owner's redo.
  2. Item 3 file order: M0146-0195 [ ] (owner-blocked on the reload redo for its measurement), M0146-0197 [ ]
     skip-scan half (committable; likely inert too — measure first), M0146-0178 [ ] residuals (worker-count sites /
     TableRealPages need a catalog threaded through ParallelSettings/upperRels).
     Pick by impact: M0146-0178's worker-count residual or 0197's skip half; or run an S9 pass if both are inert.
Files: internal/optimizer/{nl_index_join.go,pathparamindex.go,pathparamindex_test.go}, design m0146-0197.
Key symbols: leadingPrefixProbeKeys, eligibleLeadingPrefixIndexes, addParamIndexPathFor, pickIndexSkipRun.
Hypothesis/Findings:
  - The bloated-index costs are the TPC-H Q3/Q5/Q7 driver; the engine-side fixes are in, the corpus is not rebuilt.
Next step: check the owner's reload redo; else take M0146-0178's worker-count residual (catalog into
  ParallelSettings.BlocksForIndex) or 0197's skip half, measuring before/after on a private clone.
Top-residual: actionable join-method=4 (TPC-H Q2/Q3/Q7, SF0.25 Q43) — owner reload gates Q3/Q7
Skip-rationale: Q3/Q7 blocked-on owner-reload :65433 (M0146-0195); Q2 / SF0.25 Q43 not yet diagnosed.
Gates run: units rc=0, tpch-spotcheck PASS, tpch-acceptance-arm PASS, sf025 PASS=99, fire set PASS, pgbench (hook).
In-flight: none.
Owner items: (DONE: :65433 restarted on 7e9900a35 + INSERT reload redone — fk indexes compact, Q5 fixed and
  re-pinned) place the four S2 defects; XLogInsert/XLogFlush rename; M0122-0008; M0122-0009a/b/c, M0122-0010a and
  filed follow-ups; COSTTIE flips; plan-shaping GUC defaults.
