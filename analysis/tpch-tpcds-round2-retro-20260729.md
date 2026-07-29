# TPC-H retroactive gate + plan-snapshot discharge for the round-2 planner commits

Task: **M0124-0002** (`docs/design/tpcds-round2-fixes/README.md` §13.5 action 5)
Design: `docs/design/0124-0002-retroactive-tpch-plan-gate-discharge.md`
Date: 2026-07-29
Verdict: **DISCHARGED — no regression attributable to `9740fce9`.**

## Why this run exists

§13.4 item 4: phases 1.2/1.3 (`9740fce9`) landed while `scripts/tpch-spotcheck.sh`
reported SKIPPED — the TPC-H data dir had been overwritten by TPC-DS — and
`make plan-gate` was never run. `9740fce9` took `remapByPosMap` from 11 to 18
enumerated `Expr` kinds and gave `buildBindingsPosMap` eight opaque-leaf arms,
five pass-through descends and a decline-on-unknown `default:`. It therefore
*visits and rewrites* conjuncts it previously skipped, and it was never measured
on TPC-H. Separately, `plan_snapshots/` held nothing newer than round 5, so no
round-2 or M0125 planner commit had a baseline to diff against.

## Provenance

| item | value |
|---|---|
| cluster | `bench/tpch/runtime_goopg/data`, 127.0.0.1:65433, SF=1 |
| load | HammerDB, rebuilt by `ef4a65a5` (`build_goopg_20260727-192854.log`, lineitem=5,997,241) |
| target | `tpch@tpch` (durable DB — goopg persists `CREATE DATABASE`; the `postgres@postgres` fallback never fired) |
| arm **A** | HEAD (`40ad746a`) with `9740fce9`'s `internal/planner/bushy.go` hunks reverse-applied (−95/+1) in worktree `tmp/wt-armA` |
| arm **B** | HEAD (`40ad746a`) unmodified |
| held constant | `9740fce9`'s `internal/executor/expr.go` bounds check is present in **both** arms (`git diff --stat -- internal/executor/expr.go` = empty in arm A) — reverting it returns the Q8 crash and confounds the arm |
| GC | `GOGC=100`, `GOMEMLIMIT=12GiB` (not 18 GiB: Q21 drew a host-level OOM there) |
| cap | every server start via `scripts/goopg-test-run.sh`, scope `goopg-tpch-retro` |
| state | **S-cold**, by necessity — `ANALYZE <table>` inside db `tpch` errors "relation does not exist" (ledger `bench-reorg ANALYZE-scope`), so S-warm is unreachable on this cluster. This is the same state `tpch-spotcheck.sh` measures, and the state M0125-0003's flag changes. |
| per-query budget | 600 s (nothing came near it; longest query 216 s) |
| host | quiet — `pgrep -af ci/batch` showed only the sleeping `nightly-scheduler.sh` throughout |
| harness | `analysis/m0124-0002/run-stream.sh` (timed stream), `analysis/m0124-0002/with-server.sh` (plan half) |
| also present | `analysis/m0124-0002/run-arm.sh` + `runs/` — an earlier loop's harness for this same task, abandoned mid-arm-A. Kept, not deleted: its partial stream is a third arm-A reading and is used in §2. `run-stream.sh` is the one this report's numbers come from. |

A literal checkout of `9740fce9` was **not** used, per §D1: it predates the
cluster rebuild, and `b3493a6e`..HEAD spans four `internal/` commits including
`095e3ab5`'s new fsync GUC — an fsync change inside a timed A/B is a confound.

## 1. The 22-query A/B (round 1, A then B, same server age)

Raw: `analysis/m0124-0002/stream-A1.txt`, `stream-B1.txt`.

| query | arm A (s) | arm B (s) | B/A | rows A | rows B |
|---|---|---|---|---|---|
| Q1  | 6.78 | 7.23 | +6.6 % | 4 | 4 |
| Q2  | 3.70 | 3.51 | −5.1 % | 455 | 455 |
| Q3  | 11.23 | 11.86 | +5.6 % | 11521 | 11521 |
| Q4  | 33.69 | 34.60 | +2.7 % | 5 | 5 |
| Q5  | 53.98 | 54.54 | +1.0 % | 5 | 5 |
| Q6  | 3.79 | 3.82 | +0.8 % | 1 | 1 |
| Q7  | 34.43 | 34.71 | +0.8 % | 4 | 4 |
| Q8  | 52.67 | 53.36 | +1.3 % | 2 | 2 |
| Q9  | 202.52 | 175.02 | **−13.6 %** | 175 | 175 |
| Q10 | 24.41 | 23.21 | −4.9 % | 20501 | 20501 |
| Q11 | 2.41 | 2.45 | +1.7 % | 819 | 819 |
| Q12 | 50.62 | 50.27 | −0.7 % | 2 | 2 |
| Q13 | 9.87 | 9.96 | +0.9 % | 35 | 35 |
| Q14 | 4.08 | 4.20 | +2.9 % | 1 | 1 |
| Q15a-VIEWBODY | 3.63 | 3.69 | +1.7 % | 10000 | 10000 |
| Q15b-MAIN | 33.26 | 33.76 | +1.5 % | 1 | 1 |
| Q16 | 1.10 | 1.09 | −0.9 % | 18213 | 18213 |
| Q17 | 22.31 | 22.25 | −0.3 % | 1 | 1 |
| Q18 | 27.66 | 28.48 | +3.0 % | 10 | 10 |
| Q19 | 5.60 | 5.67 | +1.3 % | 1 | 1 |
| Q20 | 17.26 | 17.16 | −0.6 % | 76 | 76 |
| Q21 | 215.97 | 215.30 | −0.3 % | 405 | 405 |
| Q22 | 11.43 | 13.06 | **+14.3 %** | 7 | 7 |
| **stream** | **912** | **885** | −3.0 % | — | — |

`+` means arm B (today's HEAD) is *slower*. Twenty of twenty-two queries sit
inside §D4.3's ±10 % band. **Every query completed on both arms** (§D4.4 met),
and every row count is identical arm-to-arm (§D4.1).

## 2. The two queries that crossed the investigate band

§D4.3 requires a >10 % move to be investigated and explained. Two were, and
round 2 re-read both. The finding in each case is that the query's **intra-arm**
spread exceeds the inter-arm gap, so the round-1 move is not attributable to the
code under test.

**Q9** (`stream-A2-q9.txt` / `stream-B2-q9.txt`) — fresh server, Q9 alone:

| | round 1 (position 9 of a cold stream) | round 2 (alone) |
|---|---|---|
| arm A | 202.52 s | 166.30 s |
| arm B | 175.02 s | 161.20 s |
| B/A | −13.6 % | **−3.2 %** |

A **third, independent arm-A reading** exists and settles it. An earlier loop
today started this same task with its own harness and got as far as Q20 before
being redirected; that partial stream survives at
`analysis/m0124-0002/runs/armA-rep1.stream.txt` (17:37, same cluster, same arm-A
recipe) and records **Q9 = 180.96 s** at the same stream position. So arm A's own
Q9, measured three times, spans **166.30 – 202.52 s (22 %)**, and *both* arm-B
readings (175.02, 161.20) fall inside that range. The inter-arm gap is contained
by the intra-arm spread, which is the definition of unattributable.

The mechanism is stream position and OS page-cache state: round 1 ran A first, on
the coldest cache of the session — exactly the ordering artifact A/B/A/B exists
to expose. That earlier partial stream is also a useful drift check in its own
right: taken two hours before round 1 on the same arm, its other 19 queries sit
within a few percent of `stream-A1.txt` throughout.

**Q22** (`stream-A2-q22.txt` / `stream-B2-q22.txt`) — three back-to-back reads
per arm on one server:

| read | arm A (s) | arm B (s) |
|---|---|---|
| 1 | 11.94 | 11.84 |
| 2 | 9.78 | 9.63 |
| 3 | 9.97 | 9.92 |

The two arms are indistinguishable — arm B is in fact marginally *faster* at
every position — while first-read-vs-later inside each arm spans 22 %. Q22 is an
11-second query, so round 1's +14.3 % is 1.6 s absolute, well inside that.

Neither move survives a second reading. **No timing regression is attributable
to `9740fce9`** (§D4.3 satisfied; nothing approaches the >25 % blocking band).

## 3. Plan-shape A/B and the new baseline

Captured with the Makefile defaults (`PLAN_DB=tpch PLAN_USER=tpch PLAN_PORT=65433`),
which §D3 establishes are the correct post-rebuild targets — `postgres@postgres`
is stale folklore and would have captured a database with no TPC-H tables.

```
arm A up:  make plan-snapshot-capture LABEL=tpcds-round2-base   -> 22 queries
arm B up:  make plan-diff LABEL=tpcds-round2-base MODE=structural
arm B up:  make plan-snapshot-capture LABEL=tpcds-round2-head   -> 22 queries
```

Result (`analysis/m0124-0002/plan-diff-base-vs-head.txt`): **22 of 22 MATCH**,
zero hunks. The two captured files are in fact **byte-identical**
(`diff tpcds-round2-base.txt tpcds-round2-head.txt` is empty), which is a
stronger statement than the structural mode was asked for: not merely the same
shape, the same rendered plan text including costs. `9740fce9` changes *which conjuncts get remapped*, not which plan is
chosen, on every TPC-H query — so §D4.2 is satisfied in its strongest form
(no diff at all, nothing to attribute).

This also means the §1 timing table is a clean like-for-like: identical plans on
both arms, so no timing delta above could have been a plan-shape effect even in
principle.

**`plan_snapshots/tpcds-round2-head.txt` is the live baseline** and is committed.
It was captured **last**, deliberately: `plan-gate` takes the newest snapshot by
**mtime** and has no label parameter, so any later capture silently retargets the
gate for every concurrent line, M0123 included. Where a *specific* baseline
matters, use `make plan-diff LABEL=…`, never `plan-gate`.
`plan_snapshots/tpcds-round2-base.txt` is committed alongside it as the arm-A
reference this diff was taken against.

## 4. Row-anchor check

Both arms, round 1, against `bench/tpch/spotcheck_expected.env` and
`ci/batch/tpch-row-anchors.csv` (both re-pinned by `ef4a65a5`):

| anchor | expected | arm A | arm B |
|---|---|---|---|
| Q1 (structural) | 4 | 4 | 4 |
| Q3 | 11521 | 11521 | 11521 |
| Q5 (structural) | 5 | 5 | 5 |
| Q9 (structural) | 175 | 175 | 175 |
| Q10 | 20501 | 20501 | 20501 |
| Q11 | 819 | 819 | 819 |
| Q12 (spotcheck invariant) | 2 | 2 | 2 |
| Q13 (pinned) | 35 | 35 | 35 |
| Q16 | 18213 | 18213 | 18213 |
| Q18 | 10 | 10 | 10 |
| Q21 | 405 | 405 | 405 |
| Q22 (structural) | 7 | 7 | 7 |

Twelve of twelve, exact, on both arms. Q21 completed at 216 s under
`GOGC=100` + `GOMEMLIMIT=12GiB`, consistent with the anchor file's own ~231 s
note and with no OOM — the 18 GiB failure mode did not recur.

## 5. Retro-file: §8 step 7's missing artifact for phase 2.1 (RC-1b)

§13.1 records a protocol violation §13.5 does not list: RC-1b (`5db0a067`,
"push MHJ single-source filters AFTER the bindings remap") produced a TPC-H
before/after that "exists only in the commit message and ledger row; no
`analysis/` artifact was produced". This section is that artifact. Its evidence
is **transcribed, not re-measured** — the original A/B is not reproducible now
that the fix is in HEAD — and its provenance is `5db0a067`'s commit message plus
deferral-ledger row `2026-07-27 | tpcds-round2 RC-1b`:

> TPC-H (rebuilt cluster): before/after 22-query runs — ZERO row-count
> differences, zero >1.3× timing drift; spotcheck PASS (Q12=2, Q13=35), re-run
> green on the merged tree after upstream's `27d2dae8` fast-forward.

Two things today's run adds to it independently. First, the same cluster and the
same instrument still produce zero row diffs across all 22 queries and all 12
anchors, on a HEAD that contains RC-1b — so RC-1b's row-level claim holds at
HEAD, not only at the moment it landed. Second, RC-1b's timing claim was stated
as a **1.3× drift bound**, which is far looser than §D4.3's 10 %/25 % bands; the
`tpcds-round2-head` snapshot now gives that class of change a plan-shape
instrument it did not have, so a future RC-1b-family commit can be held to the
tighter bar. RC-1b's own residuals (Q47 downstream defect, Q72 wrong→slow) are
unchanged by this run and remain owned by their ledger rows and by M0125.

## 6. Disposition

- §D4.1 row counts identical on both arms — **met**.
- §D4.2 plan diff: 22/22 MATCH, no hunks to attribute — **met**.
- §D4.3 noise band: two queries crossed 10 %; both re-read; both attributed to
  intra-arm variance larger than the inter-arm gap; nothing near 25 % — **met**.
- §D4.4 nothing completing in A fails in B — **met** (22/22 both arms).
- §D5 not triggered: no regression found, so no ledger row for a defect and no
  M0125 blocker.

Phases 1.2 and 1.3 are retroactively gated. `plan_snapshots/tpcds-round2-head.txt`
now exists, which is the artifact M0125-0002 / -0004 / -0005 and M0125-0003
stage 2 diff against.

**One deliberate reduction, filed as a deferral.** §D1 specifies A/B/A/B over the
full 22-query stream. Round 1 was the full stream on both arms; round 2 was
narrowed to the two queries whose round-1 move crossed the investigate band (Q9,
Q22) rather than replaying all 44 remaining query-runs. The reduction is targeted
at exactly the queries the protocol would have used round 2 to adjudicate, and
the other twenty moved ≤6.6 % — but it is a narrower drift defence than designed,
and a *uniform* drift affecting all queries equally would be invisible to it. The
stream totals (912 s vs 885 s, −3.0 %) are the only cross-check on that, and they
rest on one reading each. Recorded in `.ralph/deferral_ledger.md`.
