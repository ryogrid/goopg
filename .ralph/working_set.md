Task: M0122-0012 slice 9 — constant-divisor numeric decode MEASURED INSIDE THE NOISE, not landed (546ac9418, docs
  only). Movement: none (measured non-improvement).
Files: docs/design/0100-0149/0122-0012-vector-filter-pipeline-recon.md ("Slice 9"); .ralph/fix_plan.md.
  No engine code changed; the experiment (numeric_storage.go arm + randomized test) was discarded before commit.
Key symbols: nodes.NumericInt64FromStoredPayload (bits.Div64 at numeric_storage.go ~325).
Hypothesis/Findings:
  - perf annotate put ~20% of NumericInt64FromStoredPayload on the DIVQ. A constant-divisor arm (exactly equivalent)
    measured ~1180 ms against HEAD's ~1180 ms over six rounds: the latency was already hidden by out-of-order
    execution.
  - The scan-qual micro-optimization route is EXHAUSTED: slices 6, 8 and 9 are three consecutive measured
    non-improvements, after slices 2-5 took the scan from ~1970 to ~1095 ms.
  - The host ran ~1180 ms for HEAD this loop against ~1110 earlier (load average ~1.2); always A/B in the same window.
Next step: re-read the banner. S9: routing-20261016q stays fresh while no planner/executor/cost path or corpus changes;
  the exhausted note at ca138d51c stands (only docs commits since).
  - Item 10 → M0122-0012 (still the topmost item-10 task). Do NOT tweak the scan qual again.
  - Profile a different workload first: TPC-H Q1 (aggregation over lineitem) or Q9 (hash joins) on a private TPC-H
    clone. scripts/lib/tpch-private-clone.sh tpch_private_clone_snapshot, port 5586; keep any command naming
    :65433 apart from DDL.
  - Pick a lever only if perf annotate shows a concrete cause; otherwise record and move on.
Top-residual: scan-type=1 (TPC-H Q16) RESOLVED by owner rebuild — partsupp_pk 5170→3078 (PG 3082),
  lineitem_pk 41512→23067 (PG 23101); Q16 plans Parallel Index Only Scan, digest matches PG. Next S9
  census re-ranks from the rebuilt corpus.
Skip-rationale: items 0-9 bypassed by the S9 exhausted note at ca138d51c. Open items-0-9 tasks:
  M0146-0014(channel), M0146-0042(channel),
  M0146-0007/0012b/0067/0175(below-item-10),
  M0146-0153/0154/0160/0161/0162/0163/0164/0166/0167/0168/0173/0205(below-item-10),
  M0141-S2b-3b/S2b-4e/S7-exec-d(below-item-10), M0141-S2b-4(blocked-on M0141-S2b-4e),
  M0142-0008a-3/0008c-1a/0008c-3d/0008c-4(blocked-on M0145-0003/0010).
Gates run: the experiment's nodes tests passed (code discarded); committed tree is docs-only; pgbench smoke (hook)
  PASS.
In-flight: none. The SF1 clone was removed.
Owner items: none. RESOLVED 2026-10-16: M0146-0196 closed (REINDEX resolves connection-db names,
6a17a5154), :65433 restarted on that build, `REINDEX INDEX partsupp_pk` + `REINDEX INDEX lineitem_pk`
executed (REINDEX INDEX deliberately, not TABLE — preserves the insert-built FK indexes' PG-like
sizes). Verified: Q16 Parallel Index Only Scan, 4 workers, digest == PG; spotcheck PASS.
