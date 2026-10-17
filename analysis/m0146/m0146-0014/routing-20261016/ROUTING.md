# M0146-0014 — S9 census pass at 24ca1e37a (2026-10-16)

Stale since `routing-20261015`: optimizer commits (f1fd9a8a9, 7e9900a35, 6bd814c64, 24ca1e37a) and the owner's
TPC-H reload (419dc5c9e).

Captures at code equal to HEAD `24ca1e37a`:

| corpus | capture | match |
|---|---|---|
| TPC-H | `analysis/m0146/m0146-0197/skip/m0197s-tpch*` (reloaded corpus) | 15 / 22 |
| TPC-DS SF0.25 | `.../skip/m0197s-sf025*` | 80 / 99 |
| TPC-DS SF1 | fresh: `s9-sf1*` here | 67 / 99 |

Routes come from `routing-20261013/records-final.tsv`, checked against today's fix_plan:

- **Waived (non-actionable):** the COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES and STALE-SF1 records. SF0.25 Q4
  is the owner's pending COSTTIE flip.
- **Routed to a live task:** Q56 and Q60 (Merge Append) → M0141-S2b-4e.
- **Re-opened:** SF1 Q32 and Q92 (route M0146-0176 is closed; SF1 still diverges) and SF1 Q54 (route M0146-0005dp
  is closed).

## Actionable records by category

| category | records | count |
|---|---|---|
| join-method | SF0.25 Q43, SF1 Q68, TPC-H Q18 | 3 |
| scan-type | SF0.25 Q23, TPC-H Q13, Q16 | 3 |
| parameterisation | TPC-H Q2, SF1 Q32, Q92 | 3 |
| qual-placement | TPC-H Q20, Q21, SF1 Q54 | 3 |
| sort-strategy | SF1 Q18, TPC-H Q9 | 2 |
| aggregation-strategy | SF1 Q45 | 1 |

Four categories tie at 3. The tie goes to the differ's category order, which puts join-method first.

**Filed: M0146-0198** (join-method, SF0.25 Q43 preferred per S9(3)).

- **SF0.25 Q43.** goopg estimates `store` under `s_gmt_offset = -5` at 1 row where PG estimates 12. It therefore
  joins `store_sales ⋈ store` first (18495 rows) and nested-loops `date_dim`. PG joins `store_sales ⋈ date_dim`
  first (1110 rows) and hash-joins `store`.
- **TPC-H Q18** (a different shape). PG hash-joins a parallel-scanned `customer` last; goopg joins `customer`
  earlier, with a serial scan.
