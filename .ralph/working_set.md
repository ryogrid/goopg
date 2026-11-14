# Working Set — loop end 2026-11-15 (census routing-20261115e IDENTICAL; M0148-0001c3a10 slice 2 LANDED d0e22ef1a)

- Anomaly: the previous-loop check flagged claude_output_2026-11-15_05-31-28. It was a 5-second run that died on an API error ("Credit balance is too low") before doing any work; the real previous loop committed its baton. Nothing lost.
- Nightly triage: AI-20261115-011812-001 already filed. Nothing new.
- S9: DUE → routing-20261115e (s9di, BASELINE_REV=1ead3cd80) IDENTICAL ×3, committed dc95331e1.
  - d0e22ef1a touches internal/optimizer, so the next census is likely DUE (BASELINE_REV=eefb34976, the head consumed by routing-20261115e).
- Task: M0148-0001c3a10 slice 2 landed as d0e22ef1a.
  - pathindexonly.go addOneIndexOnlyPath: an index-only probe carries the index ordering whenever it has clauses (was: range/array only); the backward twin follows.
  - Plain-table `where c = 3 order by a [desc] limit 1` now matches PG's Index Only Scan [Backward].
- Files: internal/optimizer/pathindexonly.go; internal/executor/partitionwise_join_test.go (TestIndexOnlyEqualityPrefixOrdering); design doc slice-2 section + README row.
- Findings:
  - The executor test fixture declines index paths over nullable key columns (no NULL-keyed index entries in-process), so fixture tables need NOT NULL key columns. The probe server (capable cluster) does not.
  - Partitioned min/max (PG: InitPlan → Limit → Append → Index Only Scan Backward … (c = 3) AND (a IS NOT NULL)) still needs index-only child paths (child rels run no index-only producer; they need a needed-column set) and an ordered Append over child rels (partitions_are_ordered, reversed for DESC).
- Next step: run the census if DUE, then c3a10 slice 3: in addChildIndexPaths run addOneIndexOnlyPath with the parent rel's needed columns; add an ordered Append (child paths whose pathkeys match the query pathkeys, children in bound order, reversed for DESC) in addAppendRelChildPaths. Witness: `select a from pa where c = 3 order by a desc limit 1` and `max(a)`.
- Top-residual: M0148 residues. Open: c3a10 (slices 3+), c3a9 (slice 5 blocked-on P0-E30; patch tmp/c3a9-slice5-outer.patch), c3a8, c3a7, c3a11 (parked), c3b, g1, f3, e2, h1, i2, k1; then the M0148-0002 copy suite.
- Skip-rationale (bypassed items 0-9; item 10 selected):
  - Items 0-9: M0146-0014(channel; census routing-20261115e done this loop); M0146-0042(channel); M0146-0009(channel); M0146-0007(below-item-10); M0146-0010a(owner-held [!]); M0146-0035(owner-held [!]); M0140-0007(owner-held [!]); M0142-0005(owner-held [!]); M0142-0008a-3/0008c-1a/0008c-3d/0008c-4(blocked-on M0145-0003).
  - Item 10: M-NIGHTLY open items are below-item-10 or S2 awaiting owner (P0-E9..E16, E24..E30). Then M0148 in file order → c3a10.
- Gates run (all PASS): census s9di IDENTICAL; units, tpch-spotcheck, sf025 99 PASS same=99, fire-set c3a10s2 fires=none ×3, acceptance PASS; regress same-suite A/B identical.
- In-flight: none. Scratch PG (5545) RUNNING. goopg probe server (5533) stopped.
