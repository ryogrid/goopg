# M0147-0049 — OFF-arm measurement (GOOPG_INDEXKEY_HARVEST=off)

Captured 2026-10-27 at `4ac79cdee` (planner code as of `b2665b45c`) with `GOOPG_INDEXKEY_HARVEST=off` exported to the
capture servers, through `scripts/jointree-parity-capture.sh`. The TPC-DS `.plans.txt` flag stamps read
`GOOPG_INDEXKEY_HARVEST=off`; the TPC-H lane prints no stamp. The ON default at the same planner sha is
`analysis/m0146/m0146-0014/routing-20261027h/`.

| corpus | OFF | ON | plan-text lines differing OFF vs ON |
|---|---|---|---|
| TPC-H | 22 / 22 | 22 / 22 | 0 |
| TPC-DS SF0.25 | 99 / 99 | 99 / 99 | 0 |
| TPC-DS SF1 | 99 / 99 | 99 / 99 | 0 |

After M0147-0013 confined the harvest to IN/EXISTS sublinks, the OFF arm moves no corpus plan. The 2026-09-05
"not plan-neutral" A/B (pinned-stats TPC-H SF1 runtimes) is superseded on the plan side.
