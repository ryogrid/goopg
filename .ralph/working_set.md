Task: M0122-0012 slice 7 — batch qual evaluator LANDED (7d1e1c76c, pushed), no production caller yet. Movement: none.
Files: internal/executor/{exprnode.go (evalFastBinaryApply + inline fastBinarySimple/fastIntOverflow),
  exprnode_batch.go (evalFastExprBatch, exprBatchScratch), exprnode_batch_test.go};
  docs/design/0100-0149/0122-0012b-scan-batch-qual.md (+README row); .ralph/fix_plan.md. The pending patch was removed.
Key symbols: evalFastExprBatch, exprBatchScratch, evalFastBinaryApply, fastBinarySimple, seqScanOp.Next prefilter block
  (operators_storage.go ~2100-2180), evalQual/evalPrefilter (scan_prefilter.go).
Hypothesis/Findings:
  - Microbenchmark (store_sales qual, 64 rows): per-row 11.6 µs, batch 8.4 µs. End-to-end ceiling ~12% of the scan.
  - Last loop's "fireset blocked" was a misread: internal/executor/ is outside the fire-set scope. The real rejection
    was a code_tree mismatch after peer commit a181f6bfd landed mid-gate. Memory saved.
Next step: re-read the banner.
  1. S9 pass: the census (routing-20261016p) is STALE twice over (owner corpus VACUUM on a181f6bfd, and 7d1e1c76c
     touched internal/executor). Run fresh `jointree-parity-capture.sh` for tpch, tpcds-sf025 and tpcds-sf1. If the
     SF1 capture refuses (bench/tpcds/runtime_goopg/data live — owner pid 1702919 on :65436, still up at 10:25), S9(1)
     makes item 10 live for that loop. Probe: `scripts/jointree-parity-capture.sh tpcds-sf1 x tmp/x` → rc=3 FATAL.
  2. If item 10 is live: slice 8 wires evalFastExprBatch into seqScanOp per design §4. Phases: page-mode prefix
     deform, batch eval, survivors finished. Admission: exclude serializable, GiST/GIN SSI, sampler, enum/ACL
     scans. Invariants: currentTID = emitted row, borrowRows, cancellation, early-error re-judge. Gates: + isolation
     family. Measure the SF1 store_sales filtered scan (tmp/m0122-time.sql, clone on 5591), A/B against HEAD.
Top-residual: unknown until the post-VACUUM census (SF0.25 was 86/99 in a post-VACUUM fire-set capture).
Skip-rationale: items 0-9 bypassed because the S9 census could not run (SF1 capture refused: source live), so
  item 10 was live this loop per S9(1). Open items-0-9 tasks:
  M0146-0014(channel), M0146-0042(channel), M0146-0007/0012b/0067/0175(below-item-10),
  M0146-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0196(below-item-10),
  M0146-0205(below-item-10), M0141-S2b-3b/S2b-4e/S7-exec-d(below-item-10),
  M0141-S2b-4(blocked-on M0141-S2b-4e), M0142-0008a-3/0008c-1a/0008c-3d/0008c-4(blocked-on M0145-0003/0010).
Gates run: units PASS; tpch-spotcheck PASS; acceptance arm PASS (values identical); tpcds-sf025 PASS=99 same=99;
  ea-ratchet PASS; TestPort_RegressSuite PASS (same code tree, cached); pgbench smoke PASS. Fire-set not required.
In-flight: none.
Owner items: RESOLVED 2026-10-16 — the :65436 SF1 server (pid 1702919) and the :65437 SF0.25 server (both
  /tmp/goopg-bin-vac0206, started by the owner for the corpus VACUUM) were stopped after CHECKPOINT; both
  postmaster.pid files are gone, so the offline cp -a clone sources are free again (SF1 + SF0.25 captures and
  the fire-set SF1 lane unblocked). Corpus vacuum results persist in the datadirs.
