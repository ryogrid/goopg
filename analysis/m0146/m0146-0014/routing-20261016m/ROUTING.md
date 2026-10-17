# M0146-0014 — S9 re-check at 12ba97662 (2026-10-16): exhausted

Trigger: `12ba97662` (M0122-0012 slice 2) touched `internal/executor`. It is a perf-only change to scan-qual evaluation.

| corpus | capture | match |
|---|---|---|
| TPC-H | `m0122-census-tpch*` here (fresh `jointree-parity-capture.sh tpch` at HEAD) | 20 / 22 |
| TPC-DS SF0.25 | `analysis/m0122/m0122-0012/fireset/tpcds-sf025/m0122-0012-tpcds-sf025-candidate*` (tree = `12ba97662`) | 84 / 99 |
| TPC-DS SF1 | `analysis/m0122/m0122-0012/fireset/tpcds-sf1/m0122-0012-tpcds-sf1-candidate*` (tree = `12ba97662`) | 73 / 99 |

The `first-divergence-{tpch,sf025,sf1}.txt` records are byte-identical to `routing-20261016l`. All routes are as
recorded there: M0146-0019 corpus VACUUM, COSTTIE and the other waivers, and M0145-0003 for SF1 Q69.

## Actionable records

None. No task is filed.
