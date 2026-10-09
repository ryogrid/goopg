# M0146-0060 recon (2026-10-05)

Gate tried: the single-table rule-based index producer
(`planIndexScanFromWhere`, planner.go's `isSimpleSingle &&
planIsBareSeqScanTree` arm) fires only when `planHasOuterRef(node)`.

## TPC-DS fire set

Q41 fired at both scales, with categories and matches flat. Its outer
`Seq Scan on item i1` cost moved from 180.83 to 1512.83. The costed scan now
charges the SubPlan per row, as PG does (PG: 72523824).

## Where the gate goes wrong

Fixture `dsc`: 3000 rows, `CREATE INDEX dsc_a ON dsc (a DESC)`, VACUUM and
ANALYZE.

| query | PG 18.3 | goopg, gated | goopg, gated, enable_seqscan=off |
|---|---|---|---|
| `a, c … a > 97` | Bitmap Heap Scan 4.75..22.50 | Seq Scan 54.50 | Index Scan 0.25..144.93 (PG 73.22) |
| `a … a > 97` | Index Only Scan 0.28..5.33 | Seq Scan 54.50 | Index Only Scan 0.00..0.60 |
| `a, c … a BETWEEN 3 AND 4` | Bitmap Heap Scan 22.79 | Seq Scan 62.00 | — |

The search has no bitmap path for a range restriction (`pathbitmap.go` is
equality only). Its index scan path is priced about twice PG's (B8,
M0145-0008ag). So the costed search loses to the seq scan in cases where PG
elects an index. The rule's heuristic index scan was closer to PG there.

Ten unit tests fail under the gate because they lean on the rule for tiny
fixtures. On those fixtures PG itself would seq-scan, for example a 3-row
`items` table with `id = 2`.

## Verdict

M0146-0060 is held on M0146-0061 (a range bitmap path in the search) and
M0145-0008ag (index-scan costing). The gate is backed out.
