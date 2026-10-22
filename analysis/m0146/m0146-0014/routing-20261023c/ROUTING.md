# M0146-0014 — S9 re-check at d164c7319 (2026-10-23): exhausted

Trigger: `d164c7319` (M0122-0012 slice 64, string literals compile to constants and a plain string pair skips the
character-string ambiguity test) touched `internal/executor` after the `ecbfe6696` pass.

| corpus | capture | match | before (ecbfe6696) |
|---|---|---|---|
| TPC-H | `s9c-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9c-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9c-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
