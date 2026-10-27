# M0146-0014 — S9 re-check at 08a69974e (2026-10-27): exhausted

Trigger: `08a69974e` (M0122-0012 slice 105) changed `internal/executor` after the `8432307f1` pass: the bpchar blank trim
drops whole 8-byte words of blanks (`bcTrimRight`).

| corpus | capture | match | before (8432307f1) |
|---|---|---|---|
| TPC-H | `s9ap-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9ap-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9ap-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
