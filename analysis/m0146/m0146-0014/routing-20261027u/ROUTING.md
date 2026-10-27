# M0146-0014 — S9 re-check at 06d18e112 (2026-10-27): exhausted

Trigger: `06d18e112` (M0122-0012 slice 99) changed `internal/optimizer` after the `d5e071c67` pass: the truncation
memo is keyed by key-list contents.

| corpus | capture | match | before (d5e071c67) |
|---|---|---|---|
| TPC-H | `s9al-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9al-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9al-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
