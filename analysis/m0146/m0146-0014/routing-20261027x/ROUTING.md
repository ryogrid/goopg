# M0146-0014 — S9 re-check at 8432307f1 (2026-10-27): exhausted

Trigger: `8432307f1` (M0122-0012 slice 104) changed `internal/executor` after the `986df5900` pass (`6f2772917` is
ralph-only, `2eeabf36f` touched initdb, xlog and cmd): a sorted single rollup emits its groups in close order.

| corpus | capture | match | before (986df5900) |
|---|---|---|---|
| TPC-H | `s9ao-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9ao-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9ao-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
