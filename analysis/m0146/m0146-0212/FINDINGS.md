# M0146-0212 — TPC-DS SF0.25 Q84 after M0146-0211: findings (2026-10-16)

**The question.** M0146-0211 moved goopg's SF0.25 Q84 from Gather Merge (PG's shape on `:65438`) to Sort over Gather.
Is that a goopg defect?

**PG's choice depends on the visibility map, not on a costing term.** A PG 18.3 debug build (`tmp/pgdbg`) was loaded
with Q84's six tables from `:65438` (tpcds025) and analysed.

- **Before autovacuum.** EXPLAIN ran right after the ANALYZE, before autovacuum had run (autovacuum at 04:53:45,
  EXPLAIN at about 04:53:40). PG planned **Sort over Gather**: Limit 8274.06, Gather 6122.24..8273.84, Nested Loop
  7272.64.
- **After autovacuum.** The tables now had relallvisible = relpages: customer 2854/2896, customer_demographics
  21828/21856, store_returns 1196/1232. PG planned **Gather Merge**: 8145.99..8147.36, Nested Loop 7145.88. The
  cheaper index-only probes move the tie. The `create_ordered_paths` probe lines are in
  `pgdbg-ordered-candidates-vacuumed.txt`.

**goopg follows the same switch.** The goopg SF0.25 corpus has never been vacuumed (relallvisible 0). That is the
M0146-0019 owner residual.

- **At HEAD:** Sort over Gather (`q84-goopg-head.plan.txt`), the shape PG plans on unvacuumed tables.
- **After VACUUM on a private clone:** Gather Merge, 8147.43..8148.68 (`q84-goopg-vac.plan.txt`), PG's shape.
- **Why it matched before.** The pre-0211 Gather Merge was an accident of the removed one-armed Project charge.

**Disposition:** a corpus-VACUUM residual (M0146-0019), not a defect. M0146-0211's change is consistent with PG on the
same data.
