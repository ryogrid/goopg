# M0146-0014 — S9 pass at 7fc2a5bee (2026-10-16): TPC-H 22/22, exhausted

Trigger: `7fc2a5bee` (M0122-0012 slice 13, zero-copy seq-scan tuple fetch) touched `internal/executor`.

These are fresh `jointree-parity-capture.sh` captures of all three corpora.

| corpus | capture | match | before |
|---|---|---|---|
| TPC-H | `s9u-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9u-tpcds-sf025*` | 86 / 99 | 86 / 99 |
| TPC-DS SF1 | `s9u-tpcds-sf1*` | 74 / 99 | 74 / 99 |

All first-divergence records are byte-identical to `routing-20261016t`, and every one keeps its waived route there (COSTTIE, GEQO-RNG, RELPAGES,
ESTIMATE+RELPAGES, STALE-SF1).

## Actionable records

None. No task is filed.
