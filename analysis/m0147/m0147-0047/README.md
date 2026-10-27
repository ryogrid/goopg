# M0147-0047 — OFF-arm measurement (GOOPG_PARTIAL_AGG_PATHS=off)

Captured 2026-10-27 at `3a8bfaa96` with `GOOPG_PARTIAL_AGG_PATHS=off` exported to the capture servers (the flag stamp
in each `.plans.txt` reads `GOOPG_PARTIAL_AGG_PATHS=off`), through `scripts/jointree-parity-capture.sh`. The ON
default at the same sha is `analysis/m0146/m0146-0014/routing-20261027g/`.

| corpus | OFF (size rule) | ON (priced tournament) |
|---|---|---|
| TPC-H | 10 / 22 | 22 / 22 |
| TPC-DS SF0.25 | 78 / 99 | 99 / 99 |
| TPC-DS SF1 | 74 / 99 | 99 / 99 |

Only the diff summaries are kept (`off-*-diff.txt`). The OFF arm is live, and far from PG 18.3 on every corpus.
