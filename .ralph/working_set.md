# Working Set — loop end 2026-11-12 (run-2 loop #17: census IDENTICAL; M0148-0001c3a1 CLOSED, residue c3a2 filed)

- Anomaly: none (prev-loop check OK).
- S9: DUE after 4092290f9 → routing-20261112d (label s9bo, BASELINE_REV=079d14643) IDENTICAL ×3. No code commit since, so the next loop's `scripts/s9-census-due.sh` should say REUSE; obey it.
- Task: M0148-0001c3a1 — CLOSED (49bbd0613, a78d7dd43, 8cc9a4e51, 4092290f9). Residue filed as M0148-0001c3a2, directly after c3a1 in fix_plan.
  - c3a2 items: BitmapOr first, then bitmap prefix range, duplicate NullTests, backward scans, WHERE order, varchar keys.
- Findings:
  - Regress carries 23 BitmapOr lines (create_index 11, join 8, partition_join 2, select 2) and only 1 varchar Index Cond hunk.
- Next step: S9 due-check, then the topmost open M0148 child in file order (c3a2). Start with BitmapOr (PG generate_bitmap_or_paths, indxpath.c).
  - Planner: for each OR restriction conjunct whose arms each match a bitmap index path, build a BitmapOr path (cost_bitmap_or_node: sum of arm costs + 100 * cpu_operator_cost * arms; selectivity = sum, capped at 1).
  - Plan node: BitmapOr (does goopg have one already? grep optimizer/plan.go for BitmapOr / BitmapAnd).
  - Executor: union the arms' TBMs (see BitmapAnd's intersect). Recheck Cond: the whole OR clause.
  - Probe with regress create_index `stringu1 = 'TVAAAA' OR stringu1 = 'TVAAAB'` against the scratch PG (tmp/pgscratch-j4, port 5545).
- Top-residual: M0148 residues. Open children: c3a2, c3b, g1, f3, e2, h1, i2, k1; then the M0148-0002 copy suite.
- Skip-rationale (bypassed items 0-9; item 10 selected):
  - Items 0-9: M0146-0014(channel; census routing-20261112d exhausted); M0146-0042(channel); M0146-0009(channel); M0146-0007(below-item-10); M0146-0010a(owner-held [!]); M0146-0035(owner-held [!]); M0140-0007(owner-held [!]); M0142-0005(owner-held [!]); M0142-0008a-3/0008c-1a/0008c-3d/0008c-4(blocked-on M0145-0003).
  - Item 10: M-NIGHTLY open items are below-item-10 carve-outs or S2 awaiting owner placement (P0-E9..E16, E24, E25, E26). M0119 none; M0147-0000(channel); M0122-0009a/b/c/0010a(below-item-10). Then M0148.
- Gates run: census routing-20261112d (fire-set s9bo fires=none ×3, IDENTICAL). No code change this loop.
- In-flight: none.
