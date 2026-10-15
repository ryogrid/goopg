# M0146-0014 — S9 census pass at 08ea8482e (2026-10-16): exhausted

Captures are at code equal to the last planner commit, `1d2a14aa6` (M0146-0211):

| corpus | capture | match |
|---|---|---|
| TPC-H | `analysis/m0146/m0146-0211/m0211-tpch*` | 20 / 22 |
| TPC-DS SF0.25 | `analysis/m0146/m0146-0211/fireset/tpcds-sf025/m0211-tpcds-sf025-candidate*` | 84 / 99 |
| TPC-DS SF1 | `analysis/m0146/m0146-0211/fireset/tpcds-sf1/m0211-tpcds-sf1-candidate*` | 73 / 99 |

Two records changed since `routing-20261016k`: SF1 Q54 is now MATCH (M0146-0210), and SF0.25 Q84 is now divergent.

## Routes

- **SF0.25 Q84** (sort-strategy) belongs to the M0146-0019 corpus-VACUUM residual (M0146-0212). In a PG debug build,
  PG plans the same Sort over Gather on the unvacuumed tables, and goopg plans PG's Gather Merge after VACUUM.
- **Unchanged:**
  - scan-type: TPC-H Q13/Q16 and SF0.25 Q23 belong to M0146-0019.
  - SF1 Q68 belongs to M0146-0019.
  - SF1 Q18 is COSTTIE (M0146-0209).
  - SF1 Q69 belongs to the D1-sublink class (M0145-0003).
  - SF1 Q76 and the rest are waived (COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES, STALE-SF1).

## Actionable records

None. The pass files no task; the exhausted note in M0146-0014 lists every open items-0-9 task with its gate.
