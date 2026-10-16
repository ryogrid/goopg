# M0146-0014 — S9 pass at 8883a6145 (2026-10-16): TPC-H 22/22, exhausted

Trigger: `8883a6145` (M0122-0012 slice 15, in-place int operand promotion) touched `internal/executor`.

These are fresh `jointree-parity-capture.sh` captures of all three corpora.

| corpus | capture | match | before |
|---|---|---|---|
| TPC-H | `s9w-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w-tpcds-sf025*` | 86 / 99 | 86 / 99 |
| TPC-DS SF1 | `s9w-tpcds-sf1*` | 74 / 99 | 74 / 99 |

All first-divergence records are byte-identical to `routing-20261016v`, and every one keeps its waived route there (COSTTIE, GEQO-RNG, RELPAGES,
ESTIMATE+RELPAGES, STALE-SF1).

## Actionable records

None. No task is filed.
