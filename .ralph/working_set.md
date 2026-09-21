(idle — nothing in flight)

# Loop #81 result — M0145-0020 CLOSED measured-no-gap; its premise is refuted

Banner: OWNER GO sequence 0009 [x] → 0019 [x] → 0019a [x] → **0020 [x] this
loop**. Recon: no `internal/`/`cmd/` file touched (C1).
Design: `docs/design/0100-0149/m0145-0020-examine-simple-variable-cte-arm.md`.

## Refutation 1 — upstream punts on all three named fires
`examine_simple_variable`'s CTE arm has three exits AHEAD of the recursion:
- `selfuncs.c:5843` `setOperations` → **Q74 `year_total`** (UNION ALL of two
  grouped selects);
- `selfuncs.c:5876-5883` `groupClause`, `isunique` only for ONE grouping
  column → **Q31 `ws`** (3 keys) and, one level down, **Q39 `inv`**.
A FAITHFUL port returns no stats for exactly the columns the task wanted them
for. goopg's `resolveBaseColumn` `*CTEScan` arm already stops at the same
boundary — nothing to port.

## Refutation 2 — the measured cause is the BODY estimate, not the qual
PG does NOT collapse this CTE scan. Q39 `inv`, SF0.25:
| | goopg | PG |
|---|---|---|
| HashAggregate input | 11703 | 11606 |
| grouped output (after `cov>1` HAVING) | **20** | **3869** |
| `CTE Scan on inv` (after `d_moy=1`) | 1 | **19** |
BOTH engines apply the same default 0.005 to the qual. 3869×0.005=19 survives;
20×0.005=0.1 clamps to 1. PG's 3869 = `11606 × 0.3333` — every row its own
group, then `DEFAULT_INEQ_SEL`.

## Filed: M0145-0020a — grouped-output cardinality under-shoots ~193x
**Separate the two factors FIRST** (group count vs HAVING selectivity) — this
loop did not, and changing the one that is already right would be tuning
toward a plan (R6).

## ESCALATION — the owner's sequencing does not hold
The GO filed 0020 as **M0145-0012's prerequisite**. It is not one: 0012's
`rows<=1` fallback is load-bearing because of the body estimate. **0012 resumes
after M0145-0020a**, not after 0020. Ledgered; the loop did not re-order the
banner.

## Gates
Recon, zero production diff → no value gates (C1). state guard OK; pgbench
smoke via the commit hook. Used the existing `tmp/pg18-optdebug` cluster
(`:5560`, `tpcds025`) — already running, left running — and loop #80's
committed SF0.25 capture. No new lane started.

## Next loop
Banner's GO sequence is now exhausted (0009/0019/0019a/0020 all `[x]`). Next
per the banner: **0018's fresh E1 re-verification re-runs**, then the
flow-completion chain **0004 → 0005 → 0007 → 0008**. M0145-0021/0022/0023
(harness) may run any time. NOTE 0018 still needs the owner's option re-take
from loop #79 before its relaxation can execute.

## Owner escalations — three open
partition_aggregate's inventory row; template1 namespace collision (A vs B);
M0145-0018's option choice (loop #79 evidence) — and now 0012's re-sequencing.
