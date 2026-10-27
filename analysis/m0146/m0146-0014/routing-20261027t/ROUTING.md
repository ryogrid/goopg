# M0146-0014 — S9 re-check at d5e071c67 (2026-10-27): exhausted

Trigger: `d5e071c67` (M0122-0012 slice 98) changed `internal/optimizer` after the `34ccf73db` pass:
truncate_useless_pathkeys' kept length is memoised per key list.

| corpus | capture | match | before (34ccf73db) |
|---|---|---|---|
| TPC-H | `s9ak-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9ak-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9ak-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
