Task: M0122-0012 slice 16 — applyAgg takes its AggregateCall by pointer LANDED (3e6a14e3a, pushed). Movement: none
  (perf only). Also this loop: S9 re-check exhausted at 8883a6145 (67c5a5091, routing-20261016w; unchanged).
Files: internal/executor/operators_join_agg.go (applyAgg signature; 3 per-row loops use &o.plan.Aggs[i]);
  internal/executor/operators_window.go (2 callers pass &call); join_agg_compile_test.go, parallel_agg_combine_test.go;
  recon doc "Slice 16"; .ralph/fix_plan.md; analysis/m0122/m0122-0012/accept-s16*.
Key symbols: applyAgg(*optimizer.AggregateCall), finishAgg (still by value, once per group), aggEvalList.
Hypothesis/Findings:
  - AggregateCall is 320 B; the per-row loops copied it twice per aggregate (16× per Q1 row). Q1 ~1290 → ~1225 ms.
  - Q1 over slices 10-16: ~1.91 → ~1.22 s, now level with PG 18.3 (~1.23 s). Q1 is no longer a gap.
  - The evalFastExpr hot lines are the by-value returns at the BinaryOp arm (exprnode.go:434/461/470) and the
    MaterializedSlot column return (372/374). Slice 6 measured leaf-copy variants as noise; don't retry them.
  - Other by-value structs on hot paths may hide the same cost. Grep per-row loops for `range` over plan slices of
    large structs, and measure unsafe.Sizeof (a tmp/ main package works).
Next step: re-read the banner. S9: 3e6a14e3a touched internal/executor, so run a fresh census (expected unchanged:
  22/22, 86/99, 74/99; diff against routing-20261016w) and record an exhausted note. Then item 10 → M0122-0012:
  Q1 is at parity, so profile a different TPC-H query (Q9 hash join, Q18 aggregation, or the slowest query vs PG in
  the acceptance arm, analysis/m0122/m0122-0012/accept-s16) on a private clone (scripts/lib/tpch-private-clone.sh,
  port 5586). Build the HEAD binary BEFORE editing, stage before the stamped gates, and run a 3-round fresh-server A/B.
Top-residual: owner directive 2026-10-16 un-waived the residual classes (COSTTIE, GEQO-RNG, RELPAGES,
  ESTIMATE+RELPAGES, STALE-SF1) — S9(2)/(5) in the banner. ~36 formerly-waived non-MATCH records are
  actionable; owner pre-seeded M0146-0214 (GEQO pg_prng port), M0146-0215 (Q76 estimate/width),
  M0146-0216 (physical-parity relpages/relallvisible census vs :65438). Owner also verified the SF1
  corpus is already fresh-format (relpages identical to PG; char padding present) — STALE-SF1 premise
  dissolved, NO reload needed. Next pass: run a fresh S9 census (HEAD advanced past the note's sha and
  corpus state changed: relallvisible now set on all corpora) and re-rank the freed records.
Skip-rationale: items 0-9 bypassed by the S9 exhausted note at 8883a6145. Open items-0-9 tasks:
  M0146-0014(channel), M0146-0042(channel), M0146-0007/0012b/0067/0175(below-item-10),
  M0146-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205(below-item-10),
  M0141-S2b-3b/S2b-4e/S7-exec-d(below-item-10), M0141-S2b-4(blocked-on M0141-S2b-4e),
  M0142-0008a-3/0008c-1a/0008c-3d/0008c-4(blocked-on M0145-0003/0010).
Gates run: units PASS; tpch-spotcheck PASS; acceptance arm PASS (values identical); tpcds-sf025 PASS=99 same=99;
  TestPort_RegressSuite PASS; pgbench smoke PASS (commit hook). S9 census: fresh captures, 3 corpora.
In-flight: none. The private TPC-H clone and the A/B binaries were removed.
Owner items: none open.
