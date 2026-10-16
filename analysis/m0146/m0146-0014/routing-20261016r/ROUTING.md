# M0146-0014 — S9 pass at 774354fe1 (2026-10-16): TPC-H 22/22, exhausted

Triggers:
- the owner rebuilt `:65433`'s `partsupp_pk` / `lineitem_pk` on `ff0ff0ac0` (a corpus change, via the M0146-0196
  REINDEX fix);
- `774354fe1` touched `internal/executor`.

These are fresh `jointree-parity-capture.sh` captures of all three corpora.

| corpus | capture | match | before (`routing-20261016q`) |
|---|---|---|---|
| TPC-H | `s9r-tpch*` | **22 / 22** | 21 / 22 |
| TPC-DS SF0.25 | `s9r-tpcds-sf025*` | 86 / 99 | 86 / 99 |
| TPC-DS SF1 | `s9r-tpcds-sf1*` | 74 / 99 | 74 / 99 |

TPC-H Q16 now matches PG: Parallel Index Only Scan on `partsupp_pk` (M0146-0213). The TPC-DS first-divergence records
are byte-identical to `routing-20261016q`, and every one keeps its waived route there (COSTTIE, GEQO-RNG, RELPAGES,
ESTIMATE+RELPAGES, STALE-SF1).

## Actionable records

None. No task is filed.
