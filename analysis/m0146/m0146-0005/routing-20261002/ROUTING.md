# M0146-0005 slice 114 — first-divergence routing, TPC-DS SF0.25

Capture: the M0146-0005dg fire-set candidate at HEAD `ef8285523`
(`goopg-sf025.plans.txt`, `pg-sf025.plans.txt`, `diff-sf025.txt`).

The triage worked from plan text only: four read-only passes plus
mechanical detectors (`/tmp/bmnorm2.py`, `/tmp/phnorm.py`). Families named
"new" are filed as M0146-0005dh..dq.

Q36, Q70 and Q86 are capture errors (the classifier's verdict class, not
plan records). Q8 has no structural divergence (cost residue only).

| Query | First divergence (summary) | Family | Routed to |
|---|---|---|---|
| Q1 | ctr1 ⋈ store: Hash vs NL+Materialize; goopg charges no correlated-SubPlan filter cost | SUBPLAN-COST | M0146-0005di |
| Q2 | CTE wswscs: serial HashAgg vs partial HashAgg over Parallel Append; Gather Merge over CTE Scans (PG: parallel-restricted) | PARTIAL / PSAFE | M0146-0027; M0146-0005dh |
| Q4, Q11 | top Sort vs Incremental Sort over a Merge Join of CTE scans; every join order ties | COSTTIE / SORT | M0146-0006 |
| Q5, Q26, Q45 | Gather Merge+Sort vs Sort+Gather; ties on both sides | COSTTIE | M0146-0014 (waived-tie list) |
| Q6, Q32, Q92 | goopg decorrelates the scalar-aggregate SubPlan | SUBPLAN | M0145-0008y [!] |
| Q10, Q35 | OR of hashed SubPlans: PG costs the AlternativeSubPlan by its non-hashed arm and places it at the customer scan | SUBPLAN-COST | M0146-0005di |
| Q14 | CTE Scan cross_items reports 212 rows where the CTE estimates 1 | STATS | M0146-0009i |
| Q16 | 1-loop pkey probe priced as if amortised (0.12..0.15 vs PG ~8) | B15 | ledger take3-B-15-blocked-2 |
| Q17, Q25, Q29 | probe order: goopg cs_item_sk probe vs PG's 2-col store_sales_pkey probe (2.05 vs 1.31) | B8 | owner-kept multiplier (OWNER DECISIONS 2026-09-24) |
| Q19, Q40 | GroupAgg←Sort←Gather vs Finalize/Partial GroupAgg | PARTIAL | M0146-0027 |
| Q23 | Hash Semi Join vs PG 18 Hash Right Semi Join + unique-ified CTE inner | RIGHT-SEMI / UNIQ | M0146-0005dj; M0146-0005dk |
| Q30, Q34, Q46, Q68, Q73, Q76, Q81, Q85 | Bitmap/hash where PG uses a plain index probe (2x probe price) | B8 | owner-kept multiplier |
| Q33, Q39, Q58, Q61, Q65, Q77, Q99 | both shapes within ~1% | COSTTIE | M0146-0014 (waived-tie list) |
| Q37, Q42, Q52 | looped-probe pricing / descent term / bulk-load relpages (slice 112) | B15 / B8 / RELPAGES | B-15; multiplier; M0146-0009h |
| Q38, Q87 | no per-worker Unique below Gather Merge (create_partial_distinct_paths) | PARTIAL | M0146-0027 |
| Q41 | correlated-SubPlan qual run in a parallel worker; subplan scan priced CPU-only | PSAFE | M0146-0005dh |
| Q44, Q67 | Subquery Scan + Filter where PG has a WindowAgg Run Condition (0 Run Conditions in goopg's capture) | RUNCOND | M0146-0005dn |
| Q47, Q49, Q57 | active-window order differs from select_active_windows | WINDOW-ORDER | M0146-0005dm |
| Q54 | PG's parameterised Append (index-scan children) | PARAM-APPEND | M0146-0005dp |
| Q59 | `wss_1.d_week_seq - 52` not an EC member, so the derived clause is missing | EC-EXPR | M0146-0005do |
| Q60 | looped store_sales_pkey probe 2x | B8 | owner-kept multiplier |
| Q64, Q78 | Sort vs Incremental Sort (presorted key not carried) | SORT | M0146-0006 |
| Q66 | goopg's Parallel Append over non-partial GroupAggs costs max(child) | PARTIAL | M0140-0006a-c |
| Q69 | Hash Right Anti Join missing | RIGHT-ANTI | M0146-0005dj |
| Q71 | PG keeps the `*SELECT* n` Subquery Scan over each Append arm | SUBQSCAN | M0146-0026 |
| Q72 | no left-join removal (unreferenced catalog_returns, unique key) | JOIN-REMOVAL | M0146-0005dl |
| Q75 | Parallel Hash Left vs PG Parallel Hash Right Join | PAR-RIGHT-JOIN | M0146-0005dj |
| Q79 | hash join charged no batch spill (PG-faithful sizing knob held off) | HASHSPILL | M0139-0007a (held knob) |
| Q83 | semi-join inner not unique-ified (JOIN_UNIQUE_INNER) | UNIQ | M0146-0005dk |
| Q95 | NL Semi Join over an unindexed CTE inner with a parameterised inner join | NLSEMI | M0146-0005dq |

Tally: B8 15 · COSTTIE 12 · SUBPLAN / SUBPLAN-COST 6 · PARTIAL 7 · SORT 4 ·
new families 13 records over 10 tasks · B15 / STATS / RELPAGES / SUBQSCAN /
HASHSPILL the rest.

Cross-cutting anomalies (ledgered):

- An InitPlan / CTE / SubPlan cost is not charged to the parent node (Q1,
  Q30, Q57, Q58, Q64, Q75). Folded into M0146-0005di.
- A Sort reports more rows than its input (Q57, Q65, Q67).
- A Parallel Append or join totals less than its own input (Q2, Q14, Q76).
- Q39's top Sort Key prints `inv1.*` where PG prints `inv2.*`. This is
  rendering, routed to M0146-0042.

Q78's merge-order concern from the triage was checked: the SF0.25 sweep
checksum matches the PG oracle (`ck=c06cf981a7819a37`).
