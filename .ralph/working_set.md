# Working Set — loop end 2026-11-14 (census routing-20261114n IDENTICAL; c3a5 (a) slice 4 LANDED e316ca78b)

- Anomaly: none (prev-loop check OK).
- Nightly triage: items 0. Nothing filed.
- S9: DUE → routing-20261114n (s9cs, BASELINE_REV=39bb0f4e8) IDENTICAL ×3, committed d44658c98.
  - e316ca78b touches the planner and executor, so the next census is likely DUE.
- Task: M0148-0001c3a5 item (a) slice 4, landed as e316ca78b.
  - Files: pathindexclauses.go (trailingQuals flag), pathindexonly.go (trailingColumnQuals, trailingClauseColQuals, clausesTrailingSAOP admits a NullTest lead, IOS cost position fix), createplanindex.go (2 lowering sites), executor index_trailing_saop.go (no-Elems predicate), operators_explain.go (withTrailingSAOPCond(nRange…), boundCount, nullTestCondParts), test TestTrailingColumnQualsBehindLeadingBound.
  - Findings:
    - Plans, costs and rows are identical to PG for IndexScan and IOS shapes.
    - Movement: none for the second slice in a row (create_index two hunks closer, count unchanged). Next slice should aim at a counted move.
    - parted_minmax still needs, in order:
      1. rewriteMinMaxAggregates (planner.go ~12730-13200) is rule-based. Its IOS arm refuses a WHERE over another column (wherePredSafeForIOS), so `min(a) where b = 5` → Sort + Seq Scan. PG's build_minmax_path plans `SELECT a … WHERE a IS NOT NULL AND <where> ORDER BY a LIMIT 1` through the search. The plain-query search now produces PG's exact IOS (verified: `0.28..96.96`).
      2. The varchar relabel `(b)::text` in normalizeColumnIndexKey (PG prints `b = '12345'::text` in Index Cond).
      3. The one-member appendrel alias.
      4. Multi-member min/max (ordered Append per InitPlan).
    - Bitmap trailing quals and redundant leading bounds are ledgered (bitmap may move create_index count).
- Next step: (1) — reroute min/max through the search.
  - Look for an existing subquery-planning entry (a scalar SubqueryExpr planner) to plan the synthesized SELECT; keep the rule arms as fallback only if the search declines.
  - A/B the aggregates + inherit regress suites (min/max plans everywhere).
- Top-residual: M0148 residues. Open children: c3a5, c3a6, c3a7, c3a8, c3b, g1, f3, e2, h1, i2, k1; then the M0148-0002 copy suite.
- Skip-rationale (bypassed items 0-9; item 10 selected):
  - Items 0-9: M0146-0014(channel; census routing-20261114n done this loop); M0146-0042(channel); M0146-0009(channel); M0146-0007(below-item-10); M0146-0010a(owner-held [!]); M0146-0035(owner-held [!]); M0140-0007(owner-held [!]); M0142-0005(owner-held [!]); M0142-0008a-3/0008c-1a/0008c-3d/0008c-4(blocked-on M0145-0003).
  - Item 10: M-NIGHTLY open items are below-item-10 or S2 awaiting owner (P0-E9..E16, E24, E25, E26). Then M0148 in file order → c3a5.
- Gates run (all PASS):
  - units, tpch-spotcheck, sf025 same=99;
  - fire-set c3a5a4 fires=none ×3; acceptance 24 MATCH;
  - TestPort_Isolation (first run hit go test's 10m default timeout; re-run with -timeout 55m ok in 520s);
  - regress A/B tmp/regress-c3a5a4 vs tmp/regress-c3a5a3.
- In-flight: none. Scratch PG (5545) RUNNING. goopg probe server (5533) stopped. Restarting it hits P0-E24: drop leftover partitions.
