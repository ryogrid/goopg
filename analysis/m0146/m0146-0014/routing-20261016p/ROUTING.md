# M0146-0014 — S9 re-check at 378ad9ecf (2026-10-16): exhausted

Trigger: `378ad9ecf` (M0122-0012 slice 5, NewBoolDatum result table) touched `internal/executor`.

| corpus | capture | match |
|---|---|---|
| TPC-H | `m0122s5-census-tpch*` here (fresh `jointree-parity-capture.sh tpch` at HEAD) | 20 / 22 |
| TPC-DS SF0.25 | `analysis/m0122/m0122-0012/fireset-s5/tpcds-sf025/*candidate*` (tree = `378ad9ecf`) | 84 / 99 |
| TPC-DS SF1 | `analysis/m0122/m0122-0012/fireset-s5/tpcds-sf1/*candidate*` (tree = `378ad9ecf`) | 73 / 99 |

The first-divergence records are byte-identical to `routing-20261016o`, and all routes are unchanged. No actionable
record, so no task is filed.
