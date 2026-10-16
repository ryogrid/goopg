# M0146-0014 — S9 pass at f28bad61d (2026-10-16): TPC-H 22/22, exhausted

Trigger: `f28bad61d` (M0122-0012 slice 14, inlined big-numeric flag test) touched `internal/executor`.

These are fresh `jointree-parity-capture.sh` captures of all three corpora.

| corpus | capture | match | before |
|---|---|---|---|
| TPC-H | `s9v-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9v-tpcds-sf025*` | 86 / 99 | 86 / 99 |
| TPC-DS SF1 | `s9v-tpcds-sf1*` | 74 / 99 | 74 / 99 |

All first-divergence records are byte-identical to `routing-20261016u`, and every one keeps its waived route there (COSTTIE, GEQO-RNG, RELPAGES,
ESTIMATE+RELPAGES, STALE-SF1).

## Actionable records

None. No task is filed.
