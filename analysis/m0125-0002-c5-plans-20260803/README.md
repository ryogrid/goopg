# M0125-0002 commit 5 — `exprSide`, measured

2026-08-03. Design:
`docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md`
D2 row 5 / D4, execution record §"Commit 5 of 8".

## What was measured, and why the plan A/B alone is not enough here

`exprSide` has exactly ONE caller in the tree — `splitEqualityForHash`
(`planner.go:5256-5257`; verified by grep, no other live call site) — which
scans a join predicate's `=` conjuncts for one whose operands classify as
(left, right) and promotes that pair to `LeftKey`/`RightKey`. Everything else
stays on `jn.Predicate` as a per-match residual recheck.

**goopg's EXPLAIN never prints hash keys** — there is no `Hash Cond` line
anywhere in either benchmark's plan text (`grep -c 'Hash Cond'` = 0 over all
22 TPC-H and 96 SF0.5 plans). So a change in *which* conjunct becomes the hash
key is INVISIBLE to a plan-snapshot A/B unless it also flips the printed join
algorithm. This is the same instrument hole commit 3 hit (Index mutation
invisible to EXPLAIN), and it is closed the same way: a divergence probe.

| instrument | arms | result |
|---|---|---|
| TPC-H plan snapshot, 22 queries (`:65433`, fresh capped server per arm) | `plan_snapshots/m0125-0002-c5-before.txt` vs `-after.txt` | **byte-identical**; before-arm also == `m0125-0002-c4-after.txt` (lineage confirmed) |
| TPC-DS SF0.5 `EXPLAIN`, 96 queries (`:65437`, fresh capped server per arm) | `before/` vs `after/` | 95/96 identical; the one cell (`q85`) is M0125-0047 noise — see below |
| `splitEqualityForHash` divergence probe, both benchmarks, 118 planned queries | `probe/` + `m0125-0002-c5-probe.server.log` | **232 calls, 0 `C5DELTA`, 0 `C5SIDE`** |

## The probe

A measurement-only binary (throwaway worktree off HEAD, never committed)
computed the consumer's decision with BOTH `exprSide` bodies — the old 15-arm
switch and the new `walkExprRefs` driver — keeping the OLD answer on the live
path, so it plans exactly like HEAD. Two counters:

- `C5DELTA` — the `(leftKey, rightKey, ok)` triple `splitEqualityForHash`
  returns disagrees between the two bodies. **0.**
- `C5SIDE` — the raw per-operand classification disagrees, even where the
  consumer absorbs it. **0.** This is the population a future consumer change
  would draw from, so counting it separately matters.

**Positive control:** a zero delta count is only evidence if the probe ran.
Every call logs `C5CALL`; the sweeps recorded **223 calls on TPC-DS + 9 on
TPC-H = 232**. Since `splitEqualityForHash` is `exprSide`'s only caller, that
is the *complete* live decision population on these benchmarks — not a sample.
The probe arm's TPC-H snapshot is also byte-identical to the before arm,
confirming the instrument is pure observation.

**So D2 row 5's prediction is REFUTED by measurement**, as commit 4's was. The
shapes the conversion newly admits (`IS NULL`, `IS DISTINCT FROM`, `CollateExpr`,
`RowExpr`, literal-list `IN`, and the row-independent leaves) are real and unit-
pinned in `expr_side_arms_test.go`, but no `=` conjunct on either benchmark has
such a shape in an operand position today. The zero-hunk plan diffs are
load-bearing, not lucky.

## q85 — the M0125-0047 hazard fired exactly as predicted

The single differing SF0.5 cell was `q85`: the two `customer_demographics`
scans (`cd1`/`cd2`, identical estimated rows) traded positions. Commit 4 filed
this as restart-nondeterministic tie-break instability and told commits 5–8 to
confirm with a same-binary restart before attributing it. Done:

| arm | runs | first cd-scan |
|---|---|---|
| before-binary, restarted | 3 | cd2, cd2, cd2 |
| after-binary, restarted | 4 | cd2, cd2, cd2, cd2 |

All 7 restart captures are **byte-identical to each other across BOTH
binaries** (`q85probe/`, md5 `b1bc99cf`). The captured before-arm's cd1-first
ordering is the outlier, so the hunk is instrument noise and not commit 5's
effect. The protocol commit 4 wrote paid for itself on its first use.

## Gate disposition for a zero-hunk commit

D4 item 3 (timed 22-query TPC-H) and item 4 (SF0.5 answer sweep): **not
executed** — zero hunks on both benchmarks plus a zero-delta probe over the
walker's complete consumer population. `exprSide` is read-only and returns an
enum, so commit 2's metadata-loss class cannot arise. Ledger row 2026-08-03.
Both become mandatory at the first commit in the series with a genuine
non-empty diff.
