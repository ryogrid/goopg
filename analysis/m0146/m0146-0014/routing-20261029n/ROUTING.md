# M0146-0014 — census at 45b925ffd (2026-10-29): exhausted

Trigger: `45b925ffd` changed `internal/optimizer`. The planner's escape check now sees an outer reference inside an IN
list or `= ANY (…)` operand. This fixes a wrong-results regression from `7ad6a9d6b`. The census was captured on the
staged tree before the commit. The plan text is byte-identical to `routing-20261029m` on all three corpora, and the
fire-set gate found no changed query on SF0.25 or SF1.

| corpus | capture | match | before (ee57789ff, routing-20261029m) |
|---|---|---|---|
| TPC-H | `s9w14-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w14-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w14-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
