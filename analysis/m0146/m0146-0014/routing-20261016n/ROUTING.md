# M0146-0014 — S9 re-check at 2be3922a4 (2026-10-16): exhausted

Trigger: `2be3922a4` (M0122-0012 slice 3, by-value integer deform arm) touched `internal/executor`.

| corpus | capture | match |
|---|---|---|
| TPC-H | `m0122s3-census-tpch*` here (fresh `jointree-parity-capture.sh tpch` at HEAD) | 20 / 22 |
| TPC-DS SF0.25 | `analysis/m0122/m0122-0012/fireset-s3/tpcds-sf025/*candidate*` (tree = `2be3922a4`) | 84 / 99 |
| TPC-DS SF1 | `analysis/m0122/m0122-0012/fireset-s3/tpcds-sf1/*candidate*` (tree = `2be3922a4`) | 73 / 99 |

The first-divergence records are byte-identical to `routing-20261016m`, and all routes are unchanged. No actionable
record, so no task is filed.
