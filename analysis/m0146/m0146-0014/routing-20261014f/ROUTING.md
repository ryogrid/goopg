# M0146-0014 — S9 census re-check at d4c7966e9 (2026-10-14)

`d4c7966e9` (WAL summarizer S4b) changed `internal/optimizer` by adding a `Func` field to the `PgAvailableWalSummaries`
plan node, so the census at `db93173ed` is stale under S9(1).

- **TPC-DS:** that commit's fire-set gate captured both scales from the committed code (`diff-sf025.txt`,
  `diff-sf1.txt`). The gate reported no changed plan against the previous HEAD at either scale.
- **TPC-H:** a fresh `scripts/jointree-parity-capture.sh tpch` (`diff-tpch.txt`).
- **First-divergence records:** `scripts/pg-plan-first-divergence.py`.

| corpus | match | records vs `routing-20261014e` |
|---|---|---|
| TPC-DS SF0.25 | 81 / 99 | identical |
| TPC-DS SF1 | 66 / 99 | identical |
| TPC-H | 14 / 22 | identical |

The proven-mechanism verdict of the sixth pass stands, and no task is filed.

**Exhausted at `d4c7966e9`.** Item 10 is live.
