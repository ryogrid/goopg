# M0146-0014 — S9 pass at b5ffee1f9 (2026-10-16): TPC-H 22/22, exhausted

Trigger: `b5ffee1f9` (M0122-0012 slice 11, the hashed aggregate's in-place group key) touched `internal/executor`.

These are fresh `jointree-parity-capture.sh` captures of all three corpora.

| corpus | capture | match | before |
|---|---|---|---|
| TPC-H | `s9s-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9s-tpcds-sf025*` | 86 / 99 | 86 / 99 |
| TPC-DS SF1 | `s9s-tpcds-sf1*` | 74 / 99 | 74 / 99 |

All first-divergence records are byte-identical to `routing-20261016r`, and every one keeps its waived route there (COSTTIE, GEQO-RNG, RELPAGES,
ESTIMATE+RELPAGES, STALE-SF1).

## Actionable records

None. No task is filed.
