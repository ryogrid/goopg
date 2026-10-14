# M0146-0014 — S9 census re-check at db93173ed (2026-10-14)

`db93173ed` (WAL summarizer S4a) added a plan node, `PgWalSummaryContents`, to `internal/optimizer`. That makes the
`ccdbe68f4` census stale under S9(1), so this is a fresh capture.

- **TPC-DS:** the fire-set gate of that commit captured both scales from the committed code, and its candidate
  plans are copied here (`diff-sf025.txt`, `diff-sf1.txt`).
  - The gate reported no changed plan against the previous HEAD at either scale.
- **TPC-H:** a fresh `scripts/jointree-parity-capture.sh tpch` (`diff-tpch.txt`).
- **First-divergence records** come from `scripts/pg-plan-first-divergence.py` (`first-divergence-*.txt`).

| corpus | match | records vs `routing-20261014d` |
|---|---|---|
| TPC-DS SF0.25 | 81 / 99 | identical |
| TPC-DS SF1 | 66 / 99 | identical |
| TPC-H | 14 / 22 | identical |

Every record is the same as in the fourth pass, so its ranking and the sixth pass's proven-mechanism verdict
stand. M0146-0194 (Q72) closed as a PG-internal near-tie; nothing else clears the bar.

No task is filed: **exhausted at `db93173ed`**. Item 10 is live.
