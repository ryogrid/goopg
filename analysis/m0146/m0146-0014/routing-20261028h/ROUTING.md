# M0146-0014 — S9 re-check at 923750127 (2026-10-28): exhausted

Trigger: `923750127` (M0122-0012 C45, slice 117) changed `internal/executor`: the int-lane hash probe reads a
bare-column key in place from the probe slot. Plan-neutral by construction.

| corpus | capture | match | before (602669237) |
|---|---|---|---|
| TPC-H | `s9bc-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9bc-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9bc-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
