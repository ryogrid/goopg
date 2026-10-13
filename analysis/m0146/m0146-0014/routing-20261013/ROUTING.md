# M0146-0014d — parity-closure sweep, routing refresh 2026-10-13

Capture: the M0146-0172 fire-set candidate captures at code `7e6d8b13f` (HEAD `6f4cc161d`; the later commits are docs
only), both scales (`goopg-<sf>.plans.txt`, `pg-<sf>.plans.txt`, `diff-<sf>.txt`). First-divergence records come from
`scripts/pg-plan-first-divergence.py` (`first-divergence-<sf>.txt`).

| scale | match | divergent | change since 2026-10-09 (`routing-20261009`) |
|---|---|---|---|
| SF0.25 | 70 / 99 | 29 | match 49 → 70 |
| SF1 | 44 / 99 | 55 | match 37 → 44 |

## Method

- **Classification against the previous routing.** `records-provisional.tsv` marks each record CARRY (same
  first divergence as on 2026-10-09), MOVED (the divergence moved) or NEW. SF0.25 has 20 CARRY, 8 MOVED and
  1 NEW; SF1 has 40, 11 and 4.
- **Stale carries.** Carried routes to B8 (the x2 probe multiplier, retired by M0146-0159) and B-15 (the
  btcostestimate batch, ported by M0146-0172) are stale and were re-analysed with the MOVED and NEW records.
  That is 13 SF0.25 records, done in two read-only plan-text passes (`agent-groupA.md`, `agent-groupB.md`),
  each with a cost-number justification per record.
- **SF1 is measured on a stale load.** The goopg SF1 cluster (2026-07-25) stores `char(n)` unpadded and
  `numeric` as text, so its heaps differ from PG's by 10–13% (M0146-0172, M0146-0174). The SF1 records were
  routed as follows:
  - a query that also diverges at SF0.25 takes the SF0.25 route;
  - a query that matches PG at SF0.25 (28 records) is the owner residual **STALE-SF1**: re-measure after the
    reload. Each such record keeps its prior route as a note, because some (Q4/Q11 hash spill) may be
    genuine scale effects.
- `records-final.tsv` holds the route for every record. Reconciled against the latest routing (`routing-20261010b`): Q58's record has returned to its 2026-10-09 form (measured COSTTIE then), and Q70's previous route M0146-0149 has landed.

Acceptance bar (M0146-0014): **no unnamed first-divergence record.** All 84 records (29 + 55) route to a live
task or a named owner residual.

## Routes (SF0.25; SF1 shared records take the same route)

| route | records | queries |
|---|---|---|
| COSTTIE — waived residual (M0146-0014) | 17 | Q16 (0.006%), Q19, Q26, Q33, Q38, Q39, Q40, Q45, Q58, Q61, Q69, Q72, Q77, Q78, Q85 (0.001%), Q87, Q99 |
| GEQO-RNG — waived residual (M0146-0014) | 1 | Q64 (exact tie among one-row probes, 0.04%) |
| 0014d-F1 — correlated SubPlan conjunct unpriced above the join | 2 | Q32, Q92 |
| 0014d-F2 — full Sort offered beside Incremental Sort | 1 | Q60 |
| 0014d-F3 — index-only-scan heap-fetch term / int4 index padding | 1 | Q24 |
| 0014d-F4 — Hash Semi Join over a pulled subquery rel | 1 | Q70 |
| M0146-0042 — rendering (Var deparse through SubqueryScan; PARAM_EXEC referent) | 2 | Q5, Q6 |
| RELPAGES — owner residual (item 1242 vs PG 1284 pages; hio.c bulk extension, M0146-0009h ledger) | 2 | Q42, Q52 |
| ESTIMATE+RELPAGES — owner residual (`IS NULL` sample estimate + item relpages) | 1 | Q76 |
| PARAM-APPEND — M0146-0005dp \[!\] | 1 | Q54 |
| STALE-SF1 — owner residual, re-measure after the SF1 reload (SF1 only) | 28 | Q3 Q4 Q7 Q11 Q13 Q14 Q22 Q23 Q30 Q31 Q35 Q37 Q46 Q47 Q48 Q53 Q55 Q56 Q62 Q63 Q71 Q75 Q79 Q80 Q82 Q84 Q89 Q96 |

## Findings beyond the records

F1–F5 are recorded, unfiled, in M0146-0014's S4 escalation: the lineage budget on M0146-0014 is exhausted, so only the owner can re-open it and file them.

- **0014d-F5.** A Parallel Append priced below its own first child: Q76 SF0.25's Parallel Append costs
  14166.13 over a 17941.59 child, where PG's `cost_append` sums partial children to 37614.25.
- **Noted, not filed separately:**
  - **Q5's 2-key Hash Right Join** adds about 4049 over its inputs, where PG adds about 945. That points at
    `approx_tuple_count` using one clause, or a missed inner-unique branch; it is in the 0014d-F3/F5 probe
    queue, see `agent-groupB.md`.
  - **int4 primary-key indexes** price at about 458 tuples per page, past PG's maximum for 16-byte tuples;
    this is part of 0014d-F3.
