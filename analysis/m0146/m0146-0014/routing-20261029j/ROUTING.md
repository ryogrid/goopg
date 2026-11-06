# M0146-0014 — census at 7ad6a9d6b (2026-10-29): exhausted

Trigger: `7ad6a9d6b` (M0122-0021 residual 9) changed `internal/optimizer` and `internal/executor`.
- `SublinkIsInitPlan` now classifies an uncorrelated ARRAY sublink as an initplan.
- `NodeSublinks` now walks expressions with the slot walker.

This is a planner-path change, so the census was captured on the staged tree before the commit. The captured plan
text is byte-identical to `routing-20261029i` on all three corpora. The fire-set gate found no changed query on
SF0.25 or SF1.

| corpus | capture | match | before (431cec8d7, routing-20261029i) |
|---|---|---|---|
| TPC-H | `s9w10-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w10-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w10-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
