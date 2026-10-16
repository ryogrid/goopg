# M0146-0014 — S9 pass at 63f48ac7c (2026-10-16): post-VACUUM census, files M0146-0213

Trigger: the owner VACUUMed all three goopg corpora on `a181f6bfd` (the M0146-0206 fix), a corpus change, and
`7d1e1c76c` touched `internal/executor`. These are fresh `jointree-parity-capture.sh` captures of all three corpora
at HEAD `63f48ac7c`.

| corpus | capture | match | before (`routing-20261016p`) |
|---|---|---|---|
| TPC-H | `s9q-tpch*` | 21 / 22 | 20 / 22 |
| TPC-DS SF0.25 | `s9q-tpcds-sf025*` | 86 / 99 | 84 / 99 |
| TPC-DS SF1 | `s9q-tpcds-sf1*` | 74 / 99 | 73 / 99 |

## The corpus-VACUUM residual, re-measured

- **Resolved (now MATCH):** TPC-H Q13, SF0.25 Q23, SF0.25 Q84, SF1 Q68.
- **Still divergent: TPC-H Q16.** Its route was the corpus-VACUUM residual, whose premise no longer holds. M0146-0019
  is archived and no live task names Q16, so under S9(2) it is **actionable**.
- **SF1 Q18:** COSTTIE, unchanged (M0146-0209).

## Routes of the other records

Per-record routes are from `routing-20261013/records-final.tsv` plus later passes:
- **SF0.25:** Q4 (M0146-0185), Q16, Q38, Q39, Q61, Q69, Q72, Q77, Q78, Q87 and Q99 are COSTTIE; Q64 is GEQO-RNG; Q76
  is ESTIMATE+RELPAGES.
- **SF1:** Q4, Q7, Q11, Q13, Q23, Q31, Q48, Q53, Q56, Q62, Q63 and Q71 are STALE-SF1; Q16, Q18, Q26, Q38, Q39, Q69,
  Q72, Q77, Q78 and Q99 are COSTTIE; Q52 is RELPAGES; Q64 is GEQO-RNG; Q76 is ESTIMATE+RELPAGES.
- **Records whose first-divergence text changed since the 10-13 table**, so their waiver premise should be
  re-checked when next touched:
  - SF0.25 Q77, now aggregation-strategy at depth 5;
  - SF1 Q23, Q26, Q52, Q53, Q63, Q76 and Q77.
  They keep their route here; none is a new class.

## Actionable records by category

| category | records | count |
|---|---|---|
| scan-type | TPC-H Q16 | 1 |

**Filed: M0146-0213** (scan-type, TPC-H Q16, SHAPE-DIFF).

| | PG | goopg |
|---|---|---|
| partsupp scan | `Parallel Index Only Scan using partsupp_pk on partsupp (cost=350.43..19178.43 rows=100000 width=11)` | `Parallel Seq Scan on partsupp (cost=0.00..21257.81 rows=129032 width=544)` |
| Gather Merge | Workers Planned: 4 | Workers Planned: 3 |

The index scan has no index condition. PG's `build_index_paths` offers a qual-less index path when the index can
return every needed column (`check_index_only`), and that is what PG uses here
(`postgres/src/backend/optimizer/path/indxpath.c`). With the heap now all-visible, the index-only path is cheaper.
The worker counts also differ: 3 against PG's 4 (`compute_parallel_worker` over heap and index pages).
