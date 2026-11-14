# Working Set — loop end 2026-11-15 (census routing-20261115d IDENTICAL; c3a9 slice 5 attempted, reverted; P0-E28/E29/E30 filed)

- Anomaly: none (prev-loop check OK).
- Nightly triage: AI-20261115-011812-001 already filed. Nothing new.
- S9: DUE → routing-20261115d (s9dh, BASELINE_REV=30ef7ad0e) IDENTICAL ×3, committed b37c40505. No code landed after it.
- Task: M0148-0001c3a9 slice 5 (LEFT / FULL / SEMI / ANTI partitionwise joins).
  - NOT landed; patch at tmp/c3a9-slice5-outer.patch (untracked; describes the allowed sjinfo types, passing sjinfo to sizeJoinRel/addPaths, and skipping the inner tableoid fill for SEMI/ANTI).
  - With the patch: partition_join 1879→1852, partition_aggregate 584→581, and the 2-way LEFT JOIN witness matches PG.
  - But executor TestSortKeyChaseKeepsTheKeysRelation failed: its fixture's plan now reaches a shape with WRONG ROWS.
- Findings — three S2 defects filed with escalation blocks (fix_plan, before P0-E27), all pre-existing (reproduce with partitionwise join off):
  - P0-E30: `(A JOIN B) LEFT JOIN C … WHERE <qual on A>` returns A's columns in B's place on plain tables. The plan keeps `Filter: (t1.b = 0)` on the outer join and prints t3 as t2. Root cause of the slice-5 failure.
  - P0-E29: `(A LEFT JOIN B) FULL JOIN C … WHERE coalesce(a,0)…` fills B's columns with A's values (plain tables too). Possibly the same mislayout.
  - P0-E28: a whole-row reference to a NULL-extended rel is `(,,)`, not NULL.
- Next step: P0-E30 blocks slice 5 and is S2 (owner placement). Per the banner, continue with the next selectable M0148 task in file order: c3a10 (index clauses on appendrel members). Re-apply the slice-5 patch only after P0-E30 is fixed.
- Top-residual: M0148 residues. Open: c3a9 (slice 5 blocked-on P0-E30; slice 6 N-way), c3a10, c3a8, c3a7, c3a11 (parked), c3b, g1, f3, e2, h1, i2, k1; then the M0148-0002 copy suite.
- Skip-rationale (bypassed items 0-9; item 10 selected):
  - Items 0-9: M0146-0014(channel; census routing-20261115d done this loop); M0146-0042(channel); M0146-0009(channel); M0146-0007(below-item-10); M0146-0010a(owner-held [!]); M0146-0035(owner-held [!]); M0140-0007(owner-held [!]); M0142-0005(owner-held [!]); M0142-0008a-3/0008c-1a/0008c-3d/0008c-4(blocked-on M0145-0003).
  - Item 10: M-NIGHTLY open items are below-item-10 or S2 awaiting owner (P0-E9..E16, E24..E30). Then M0148 in file order.
- Gates run: census s9dh IDENTICAL. Slice-5 trial: units FAILED (TestSortKeyChaseKeepsTheKeysRelation → P0-E30); spotcheck and sf025 PASS on the trial build. Reverted, so no code gates on the committed state (bookkeeping only).
- In-flight: none. Scratch PG (5545) RUNNING. goopg probe server (5533) stopped.
