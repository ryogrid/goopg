# M0146-0014 — S9 re-check at a53a0c466 (2026-10-23): exhausted

Trigger: `a53a0c466` (M0122-0012 slice 78: pathkey usefulness looks each key up once) touched
`internal/optimizer` after the `a5693f85c` pass.

| corpus | capture | match | before (a5693f85c) |
|---|---|---|---|
| TPC-H | `s9q-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9q-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9q-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
