# M0146-0005 slice 116 — routing refresh after the slice-115 families

Capture: `scripts/jointree-parity-capture.sh` at HEAD `6817d5610`, both
scales. The files in this directory are:

- `goopg-<sf>.plans.txt` and `pg-<sf>.plans.txt`, the two captures;
- `diff-<sf>.txt`, the plan-parity diff;
- `first-divergence-<sf>.txt`, from `scripts/pg-plan-first-divergence.py`.

| scale | match | join-order (excl. match) |
|---|---|---|
| SF0.25 | 42 / 99 | 49 |
| SF1 | 33 / 99 | 55 |

The tool was also re-run on slice 115's captures (`../routing-20261004/`),
and the two record sets were diffed. Only the records below moved. Every
other record keeps its slice-115 route (`../routing-20261004/ROUTING.md`,
"Carried").

## Records whose slice-115 target has closed or been held

The slice-115 targets were M0146-0005dv, dw, dx, dy, dz and ea, M0146-0009o
and 0009p, and M0141-S2a-fix2r-a.

| Query | First divergence now | Family | Target |
|---|---|---|---|
| Q10 | MATCH at both scales (0005dx1/dy/ea) | — | — |
| Q47, Q57 | MATCH at both scales (0005dv) | — | — |
| Q35 | `sort-strategy`: PG Incremental Sort over a Gather Merge of `customer_address`; goopg Sort | SORT | M0146-0006 |
| Q44 SF0.25 | `parameterisation` at depth 1: the item probe nested loops cost 405943..727720 vs PG 93859..104547 (goopg's probe ≈2.1 per row vs PG 0.07) | B8 | M0145-0008ag |
| Q44 SF1 | `join-method` at depth 1: PG Merge Join vs goopg Nested Loop, downstream of the same probe cost | B8 | M0145-0008ag |
| Q69 SF0.25 | `join-order` of the two anti joins; plan totals 43430 vs 43133 (0.7%) | COSTTIE | M0146-0014 |
| Q69 SF1 | MATCH | — | — |
| Q78 SF0.25 | `sort-strategy` under Limit: goopg Incremental Sort where PG sorts | SORT | M0146-0006 |
| Q78 SF1 | `join-order` of the Merge Left Joins; their keys now match PG's (0005dz). The store\_returns anti-join estimate (30 vs 281) remains | STATS | M0146-0009 |
| Q18 SF0.25, Q22 SF0.25 | MATCH (0009o) | — | — |
| Q18 SF1 | `aggregation-strategy`: goopg MixedAggregate vs PG's sorted rollup; a cost election in the grouping-sets path set | GSETS | M0146-0020b |
| Q22 SF1 | `parallelism`: PG Parallel Hash Join vs goopg serial Hash Join under Gather (`item` relpages) | RELPAGES | owner SF1 reload |
| Q67 | `scan-type`: PG Subquery Scan on dw1 vs goopg's aggregate (now a GroupAggregate like PG's, 0009o) | SUBQSCAN | M0146-0026 |
| Q72 | `sort-strategy` at depth 3: PG Nested Loop Left vs goopg Sort (the partial subtree's reach) | PARTIAL | M0146-0027 (and 0009p, held) |
| Q4, Q11 SF0.25 | `sort-strategy` under Limit: PG Incremental Sort | SORT | M0146-0006 |
| Q4, Q11 SF1 | `aggregation-strategy`: PG HashAggregate vs goopg GroupAggregate in `year_total` | HASHSPILL | M0141-S2a-fix2r-a |

## Other records that moved since slice 115

| Query | First divergence now | Family | Target |
|---|---|---|---|
| Q26 SF1 | `sort-strategy` under GroupAggregate: PG Sort vs goopg Gather Merge | COSTTIE | M0146-0014 (as at SF0.25) |
| Q59 SF1 | `qual-placement` at the top Nested Loop. Below it, goopg's `CTE Scan on wss` estimates **6265** rows, a tenth of its own CTE's 62646; PG reads 62640. At SF0.25, where the CTE is a plain HashAggregate, the scan reads 62646. | STATS (new) | **M0146-0009q** |

## Closing M0146-0005

The banner's condition for closing M0146-0005 is that every remaining
first-divergence record is routed to a named task (item 3, M0146-internal
ordering). With this refresh:

- every non-matching record at both scales has a named target;
- the families slice 115 opened are worked or held.

M0146-0005 closes, which releases M0146-0006 (Incremental Sort election). It
is now the largest named target: Q4, Q11, Q35 and Q78 at SF0.25, plus the
slice-115 SORT carriers.
