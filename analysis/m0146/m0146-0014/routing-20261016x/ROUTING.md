# M0146-0014 — S9 pass at 3e6a14e3a (2026-10-16): TPC-H 22/22, exhausted

Trigger: `3e6a14e3a` (M0122-0012 slice 16, applyAgg by-pointer AggregateCall) touched `internal/executor`.

These are fresh `jointree-parity-capture.sh` captures of all three corpora.

| corpus | capture | match | before |
|---|---|---|---|
| TPC-H | `s9x-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9x-tpcds-sf025*` | 86 / 99 | 86 / 99 |
| TPC-DS SF1 | `s9x-tpcds-sf1*` | 74 / 99 | 74 / 99 |

All first-divergence records are byte-identical to `routing-20261016w`, and every one keeps its waived route there (COSTTIE, GEQO-RNG, RELPAGES,
ESTIMATE+RELPAGES, STALE-SF1).

## Actionable records

None. No task is filed.
