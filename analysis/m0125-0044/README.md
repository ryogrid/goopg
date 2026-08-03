# M0125-0044 — evidence

Design: `docs/design/0125-0044-groupby-alias-slot-collapse.md`.
Filing evidence (the A/B that proved the defect pre-existing and FROM-order
independent) lives in `analysis/m0125-0034b/`.

## The bisection

All four probes run on the SF0.5 clusters — goopg `:65437` db `postgres`, PG
oracle `:65438` db `tpcds05` user `ryo`. Each is the same 5-relation join with
`date_dim` under two or three aliases.

| probe | before the fix | after |
|---|---|---|
| `nogroupby_correct.sql` — no GROUP BY | **already correct** (`1998 \| 1993 \| 1993`) | unchanged |
| `groupby_collapse.sql` — `GROUP BY 1,2,3` | wrong: `y1 = y2 = y3` on every row | matches PG |
| same with spelled-out `GROUP BY d1.d_year, …` | wrong, identically | matches PG |
| `groupby_collapse_computed.sql` — `d1.d_year + 0` | wrong: `y1 = y2` | matches PG |
| `../m0125-0034b/alias_a.sql` (the filing repro) | wrong | matches PG column-for-column |

The first row is the load-bearing one: **the join, the scan layout and the
alias bindings were never wrong.** Only the aggregate surface was. The last row
is the acceptance probe.

A plain three-alias self-join with no aggregate (`d1.d_date_sk = …`, three
different `d_year`s projected) was correct before the fix too — checked so that
"aliases collapse" could not be blamed on binding in general.

## Gate reports (`gate/`)

One binary (`tmp/goopg-m0125-0044-bin`), tree `d50c0b4a` + this change, S-cold,
300 s/query.

| report | queries | result |
|---|---|---|
| `sweep-20260731-175607.txt` | Q64 solo probe | PASS 33 s, 2 rows, ck=31f0342ff9d55c4a |
| `sweep-20260731-175702.txt` | Q1–Q33 | PASS=32 TIMEOUT=0 SKIP=1 |
| `sweep-20260731-181352.txt` | Q34–Q66 | PASS=31 TIMEOUT=1 (Q65) SKIP=1 |
| `sweep-20260731-183306.txt` | Q67–Q99 | PASS=30 TIMEOUT=1 (Q78) SKIP=2 |

Total **PASS=93 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=2 SKIP=4**.

Diffed cell by cell against HEAD `d50c0b4a`
(`analysis/m0125-0034b/gate/sweep-2026073*.txt`, 99 cells): **exactly one cell
moved**, `Q64 MISMATCH → PASS`. The other 98 are identical in status. Q65 and
Q78 were already TIMEOUT at HEAD — they are the pre-existing performance class,
not a regression from this change.

## Other gates

- TPC-H plan-diff: 22/22 MATCH vs `plan_snapshots/m0125-0043-after.txt`; new
  snapshot `plan_snapshots/m0125-0044-after.txt`. Note the older
  `tpcds-round2-head` baseline diffs 22/22 against ANY current binary — it was
  captured with no table statistics (`grep -c stats` → 0, vs 85 in the
  same-era baseline), so its divergence is an environment delta and says
  nothing about a code change.
- `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=35, 31.5 s).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS.
- `go test ./internal/planner/... ./internal/executor/ ./internal/parser/` PASS.
