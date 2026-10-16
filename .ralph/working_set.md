Task: M0122-0012 slice 6 — operand leaf copy MEASURED, NOT landed (d8b561537, docs only). Movement: none (measured
  non-improvement).
  - Also this loop: S9 re-check at 378ad9ecf was exhausted (45e82cda8, routing-20261016p; records byte-identical).
Files: docs/design/0100-0149/0122-0012-vector-filter-pipeline-recon.md ("Slice 6"); .ralph/fix_plan.md.
  No engine code changed. The experiment was discarded before commit.
Key symbols: evalFastExpr (ExprBinaryOp arm), seqScanOp.evalQual / evalPrefilter, filterOp batch path (operators.go).
Hypothesis/Findings:
  - The wrapper variant (all operands via a helper) was −2.5% on store_sales but slower on TPC-H Q6 (AND chain).
  - The leaf-only variant was inside the noise (store_sales 1136/1096/1134 vs 1111/1106/1047 ms; TPC-H flat).
  - The remaining evalFastExpr cost is tree-walk control flow (dispatch, recursion), not Datum copies.
  - TPC-H single-shot timings are ±15% noisy (same binary: Q1 4.38 vs 3.65 s); judge on repeated A/B only.
Next step: re-read the banner.
  - The S9 note at 378ad9ecf STILL HOLDS: no planner/executor commit since (d8b561537 is docs only), so item 10 is
    live without a re-census.
  - M0122-0012 slice 7: batch qual evaluation inside seqScanOp (the SeqScanOp entry in unimplemented_feat.json).
    Write the design first: which node kinds batch, how the prefilter's partial deform interacts, and the
    currentTID read-ahead rule (recon doc section "Correction 2026-09-23"). Then implement behind the same
    admission rule as filterOp's batch path.
Top-residual: corpus-VACUUM residual (M0146-0019, owner) — 6 records (TPC-H Q13/Q16, SF0.25 Q23/Q84, SF1 Q68/Q18).
  OWNER ACTION DONE 2026-10-16: all three goopg corpora re-vacuumed on a181f6bfd (the M0146-0206 fix) —
  tpch :65433 8/8 tables, tpcds :65436 SF1 25/25, :65437 SF0.25 25/25; relallvisible <= relpages everywhere
  (was 0 on TPC-DS). The residual's premise is now stale — the next plan capture runs on the vacuumed corpus.
Skip-rationale: items 0-9 bypassed by the S9 exhausted note at 378ad9ecf. Open items-0-9 tasks:
  M0146-0014(channel), M0146-0042(channel), M0146-0007/0012b/0067/0175(below-item-10),
  M0146-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0196(below-item-10),
  M0146-0205(below-item-10 S2, owner-confirmed), M0141-S2b-3b/S2b-4e/S7-exec-d(below-item-10),
  M0141-S2b-4(blocked-on M0141-S2b-4e),
  M0142-0008a-3/0008c-1a/0008c-3d/0008c-4(blocked-on M0145-0003/0010).
  Closed by owner 2026-10-16: M0146-0206 (a181f6bfd), M0146-0178 (RELPAGES residual), M0141-S3..S6
  (superseded by M0146-0003), four M0122-era S2 defects + M0146-0205 placed below item 10, M0146-0211
  keep-confirmed (R3).
Gates run: experiment gates (units, tpch-spotcheck, acceptance arm, sf025 99/99 same=99, fireset fires=none,
  ea-ratchet, TestPort_RegressSuite) all PASSED on the wrapper variant, which was discarded anyway. The committed
  tree is docs-only; pgbench smoke (hook) PASS.
In-flight: none.
Owner items: none — all cleared 2026-10-16 (corpus VACUUM done, placements recorded, stale rows closed, tmp/
  pruned 97%→74%).
