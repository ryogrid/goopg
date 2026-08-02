# M0125-0002 commit 4 — `visitColumnRefsForTable`, measured

2026-08-03, QUIET host (nightly batch `20260803-013955` ended 03:52, scheduler
asleep ~22 h) — the first commit in the series measured without co-load.
Design: `docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md`
D2 row 4 / D4, execution record §"Commit 4 of 8".

## What was measured

D2 row 4 called this walker "a first-order shape mover": it feeds `tableForCol`
(its ONE live consumer), which decides local-filter partitioning
(`partitionConjunctsForJoinPlanning`) and join-edge left/right classification.
The conversion admits the same newly-visited same-scope shapes as commit 3
plus this walker's own headline: the old `InExpr` arm returned before visiting
ANYTHING when `Plan != nil`, so `col IN (subquery)` contributed no index and
the conjunct read as "no table".

| instrument | arms | result |
|---|---|---|
| TPC-H plan snapshot, 22 queries (`:65433`, fresh capped server per arm) | `plan_snapshots/m0125-0002-c4-before.txt` vs `-after.txt` | **byte-identical**, both == `post-mhj-retire` lineage (== `m0125-0002-c3-after.txt`) |
| TPC-DS SF0.5 `EXPLAIN`, 96 queries (`:65437`, fresh capped server per arm) | `head/` vs `c4/` | **96/96 byte-identical** |
| `tableForCol` divergence probe, both benchmarks, 118 planned queries | `probe/` arm + `tpch-probe-snapshot.txt` | **0 `C4DELTA` lines** |

## The probe

A measurement-only binary (throwaway worktree off HEAD + the converted
`bushy.go`, never committed) computed `tableForCol` with BOTH walker bodies —
the restored 12-arm switch and the new `walkExprRefs` driver — and logged
`C4DELTA old=<t> new=<t> expr=<T>` to the server log on any disagreement.
Unlike commit 3 (where Index mutation was invisible to EXPLAIN), this walker's
entire effect IS structural and printable — but the probe still closes the gap
between "the plans printed the same" and "the sole consumer decided the same",
at ~15 min cost. Zero deltas ⇒ the zero-hunk diffs are load-bearing.

## q85 — restart-nondeterministic alias order (filed `M0125-0047`)

The probe arm's ONLY differing cell, `q85`, is an alias tie-swap: the two
`customer_demographics` scans (`cd1`/`cd2`, identical estimated rows) traded
positions vs the head/c4 arms. Not a walker effect (0 `C4DELTA`); confirmed
pre-existing by restarting the SAME `tmp/goopg-c4-after` binary 3× — runs 1–2
printed cd2-first, run 3 cd1-first. PG's planner is deterministic, so this is
both a PG-divergence and an instrument hazard for every EXPLAIN A/B in this
repo: commits 5–8 should treat a Q85-only alias-swap hunk as suspected noise
and re-check with a same-binary restart before attributing it.

## Gate disposition for a zero-hunk commit

- D4 item 3 (timed 22-query TPC-H) and item 4 (SF0.5 answer sweep): **not
  executed** — zero hunks + a zero-delta probe on the sole consumer; the
  walker is read-only, so commit 2's metadata-loss class cannot arise. Ledger
  row 2026-08-03. Both become mandatory at the first commit with a non-empty
  diff.
- Baseline label: the A/B above diffs its own two arms (staleness-immune);
  `post-mhj-retire` remains the label later commits should name.
