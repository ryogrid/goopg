# M0146-0014 — S9 pass at ddc40a35c (2026-10-16): TPC-H 22/22, exhausted

Trigger: `ddc40a35c` (M0122-0012 slice 12, applyAgg cached names + in-place numeric sum) touched `internal/executor`.

These are fresh `jointree-parity-capture.sh` captures of all three corpora.

| corpus | capture | match | before |
|---|---|---|---|
| TPC-H | `s9t-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9t-tpcds-sf025*` | 86 / 99 | 86 / 99 |
| TPC-DS SF1 | `s9t-tpcds-sf1*` | 74 / 99 | 74 / 99 |

All first-divergence records are byte-identical to `routing-20261016s`, and every one keeps its waived route there (COSTTIE, GEQO-RNG, RELPAGES,
ESTIMATE+RELPAGES, STALE-SF1).

## Actionable records

None. No task is filed.
