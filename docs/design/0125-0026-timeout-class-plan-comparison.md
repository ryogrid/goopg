# 0125-0026 — The 15-query timeout class: capture both engines' EXPLAIN, classify, and file the planner fixes

Status: **filed (work plan — no fix content here by design)**
Date: 2026-07-30 (filed by the user's interactive session, per user directive)
Milestone: M0125 (the timeout class this milestone is *named* after)
Depends on: nothing — the capture half is host-independent (EXPLAIN only, no execution)

## Problem

After the correctness class closed (0 ERROR / 0 row-mismatch / 0 CKMISMATCH at
SF0.5), the **only** remaining goopg-specific defect class is 15 queries that
time out on goopg while PG answers them in seconds. Per the git-tracked oracle
(`bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt`, `q|status|rows|ck|secs`):

| Q | oracle rows | ck | PG secs | syntax profile (measured from the query files) |
|---|---:|---|---:|---|
| Q5 | 100 | n/a | 1 | 1 CTE, ROLLUP, 3-channel UNION |
| Q8 | 0 | 1f18d650… | 1 | INTERSECT (zip-list vs customer-derived) |
| Q10 | 0 | 1f18d650… | 11 | **3× EXISTS**, no CTE |
| Q14 | 200 | n/a | 16 | 2 CTEs, **4× INTERSECT**, 12 IN/scalar subqueries, ROLLUP, ×2 phases |
| Q30 | 31 | f47a4849… | 4 | CTE + **correlated scalar agg** (`> (select avg(…)*1.2 … where ctr_state = ctr1.ctr_state)`) |
| Q31 | 19 | 2a74acfb… | 4 | 1 CTE referenced **6×** (ss/ws × 3 quarters) |
| Q35 | 100 | n/a | 0 | **3× EXISTS** — the measured RC-8 instance (see below) |
| Q47 | 100 | n/a | 2 | **added 2026-07-30** — CTE + `rank()` window + 3-way self-join on `rn = rn±1` |
| Q54 | 0 | 1f18d650… | 10 | CTE, cross-channel customer set, month-range scalar subqueries |
| Q64 | 2 | 31f0342f… | 0 | 1 big CTE self-joined (two-phase, cnt>1) |
| Q65 | 100 | n/a | 0 | two derived aggregates over store_sales, `<= 0.1*avg` |
| Q67 | 100 | n/a | 3 | **ROLLUP + window** (`rank() over partition`) over the widest agg |
| Q69 | 100 | n/a | 0 | **3× EXISTS** (Q10 family) |
| Q71 | 580 | 521a7af7… | 0 | 3-channel UNION inner join, group by hour |
| Q78 | 45 | 8f67acff… | 2 | 3 CTEs, each an **anti-join by LEFT JOIN … IS NULL** over a fact pair |
| Q81 | 100 | n/a | 15 | CTE + **correlated scalar agg** (Q30's exact shape on catalog_returns) |

goopg's side: all 15 are `TIMEOUT` at the gate's 300 s cap (merged sweep at
`50cf7c5f`), i.e. **≥ 20–300× slower than PG on the same data and query text**.

**The class is SIXTEEN as of 2026-07-30, and the sixteenth arrived by being
repaired.** The full 99-query gate at `e29faca9`
(`analysis/m0125-sf05-fullgate-20260730/`) reports `PASS=79 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=16 SKIP=4`: Q47 moved `MISMATCH → TIMEOUT` because
M0125-0013 fixed its row count (0 → 100 = oracle) and the query now does the real
work. Capture it in all three arms with the rest. But it must **not** be
pre-classified as unbounded-above: its one completion reading is `OK 142 s` at
SF=1 (M0125-0013's open bookkeeping half), so a 300 s cap on *half* the data is
not obviously a hard cut, and `0124-0001` §D6 forbids reporting a
budget-marginal member and an unbounded one as a single class. Q47's own reading
comes first; only then does it join a root-cause bucket below.
Q35 additionally has a measured floor: outer cardinality 96,562 × 8.16 s per
buffer-warm `EXISTS` evaluation ≈ **9.1 days at SF=1** (`0124-0004` §"Execution
record"), so at least one member cannot complete under ANY budget with the
current plan shape.

**What has never been done: comparing the PLANS.** Every prior instrument
compares results (row counts, checksums) or goopg's plan against its own past
(`plan-diff`). There is no artifact in the tree that puts goopg's EXPLAIN next
to PG's EXPLAIN for even one of these 15. That comparison is the cheapest
remaining source of mechanism evidence, and it requires **no execution at all**.

## Work plan (what the executing loop does, in order)

### Step 1 — Capture (host-independent; EXPLAIN only; execution FORBIDDEN)

Artifacts to `analysis/m0125-0026-timeout-plans/`.

1. **goopg, arm OFF (default flags):** start the SF0.5 server
   (`bench/tpcds/server.sh start sf05`, or `scripts/goopg-test-run.sh` with a
   private `GOOPG_CG_UNIT` — the cgroup cap is mandatory either way), then for
   each of the 15: plain `EXPLAIN` of
   `bench/tpcds/runtime_goopg/tpcds-data/queries/queryN.sql` → `goopg-off/qN.txt`.
   Precedent: `analysis/m0125-0004-q75/explain-all-base/` proved plain EXPLAIN
   works and is cheap on all 99.
2. **goopg, arm ON:** same 15 with `GOOPG_RELSIZE_FALLBACK=2` in the server's
   environment → `goopg-relsize2/qN.txt`. Shape-only, so this does NOT
   confound M0125-0003's owed four-arm TIMED study; it answers a different
   question (which of the 15 change shape when the DP gets real seeds).
3. **PG 18.3:** `psql -h 127.0.0.1 -p 65438 -U ryo -d tpcds05` (role is `ryo`,
   NOT `postgres`), plain `EXPLAIN` of the same 15 files → `pg/qN.txt`.
   PG has statistics on this cluster, so its plans carry real row estimates —
   that asymmetry is data, not a flaw: it shows the plan PG picks when it
   KNOWS the sizes.
- **`EXPLAIN ANALYZE` is forbidden on goopg for these queries** — they are the
  queries that do not finish. (On PG it is unnecessary; plain estimates
  suffice for classification.)
- Nightly co-tenancy is acceptable for this step: nothing here is timed.

### Step 2 — Classify (per query, against the Suspects below)

For each query produce one row: goopg join order / scan kinds / subplan
placement vs PG's, and WHICH structural difference plausibly owns the ≥20×
gap. The classification is **this task's output, not this document's claim** —
the Suspects section is a hypothesis list, and a query landing in "none of the
above" is a finding, not a failure.

### Step 3 — Rough cost arithmetic (the Q35 method, per class)

No timing. From known SF0.5 table cardinalities (store_sales ≈ 1.44 M,
catalog_sales ≈ 0.72 M, web_sales ≈ 0.36 M, customer 100 k, item 102 k —
re-derive exact counts from the load logs or `count(*)` on PG, NOT on goopg),
estimate per-node work as `outer_rows × per-rescan_cost` (rescan shapes) or
`|build| × |probe|` (join-order shapes), and state per class **why 300 s is
unreachable** — e.g. Q35's 96,562 × full-fact-scan makes the budget short by
~3 orders of magnitude. The number needs one significant digit, not a model.

### Step 4 — File the fix tasks (M0125-0027+), one per CLASS

Each with: member queries, the plan evidence (file paths from step 1), the
arithmetic from step 3, the suspected code site, and acceptance =
**first-ever completion checked against the oracle row** (rows + ck where the
oracle has one; Q35's `35|OK|100|0` is already designated M0125-0003's
acceptance row — coordinate, don't duplicate). Timed verification belongs to
the fix tasks and needs a quiet host; classification does not. Propose a
selection order in the Current Priority banner (largest class first), and
leave the banner's existing order for -0002/-0003/-0005 untouched.

## Suspects (hypothesis list — commentary on where the bodies likely are)

**(a) RC-8: correlated subplan → nested-loop `Filter`, full fact rescan per
outer row.** The one measured mechanism (Q35). Q10/Q69 are the same 3×EXISTS
shape over the same customer/demographics outer; Q30/Q81 are the scalar-agg
variant (`> (select avg(…)*1.2 …)` correlated on state). Expect goopg's
EXPLAIN to show the subquery as a Filter on a scan rather than PG's hashed
SubPlan / semi-join. `0124-0004` §D4's decision rule stands: if
`CacheMisses ≈ Calls`, the indicated fix is **hashed SubPlan caching, not
decorrelation** — the fix task must not pre-commit to decorrelation.
Candidate members: Q10 Q30 Q35 Q69 Q81 (5 of 15).

**(b) Join order chosen with no cardinality signal.** At S-cold the bushy DP
seeds every relation `rows=1` (`ac9bf911`'s own words: "no cardinality signal
whatsoever"), and `reorderCommaFromByCardinality`
(`internal/planner/joinorder.go`) bails entirely when any table lacks a row
count — so join order degenerates toward FROM order. Arm ON vs arm OFF diffs
isolate exactly this: **a query whose shape changes under
`GOOPG_RELSIZE_FALLBACK=2` and starts resembling PG's is (b); one whose shape
does not move needs a different explanation.** This is the overlap with
M0125-0003 — the capture tells us which members -0003 alone might fix, for
free, before its timed study runs.

**(c) RC-5: every TPC-DS base relation is costed UNFILTERED.**
`shouldAttachBeforeMHJ` requires a `SmallDimension`, and that set is
hardcoded to `region`/`nation` (`internal/planner/local_filters.go:154`,
`internal/initdb/open.go:2941`) — no TPC-DS relation can ever qualify, so
single-table predicates (`d_year = 2000`, state lists, `i_category IN (…)`)
do not shrink MHJ build sides at costing time. Suspect a broad *aggravator*
across all MHJ-bearing members rather than a sole cause anywhere.

**(d) CTE referenced N times = body planned/executed N times?** Q31 references
its one CTE six times; Q14 builds `cross_items`/`avg_sales` and consumes them
across three INTERSECT channels × two phases; Q64 self-joins its CTE; Q78
stacks three anti-join CTEs. If goopg inlines or re-derives the CTE body per
reference instead of materializing once (verify in
`internal/planner/with.go` — it was touched only +9 lines in the whole
window), the multiplier alone can eat the budget. The EXPLAIN will show the
body's subtree repeated.

**(e) No LIMIT/TopN pushdown through window/rollup.** Q67 is
`rank() ≤ 100` over a windowed ROLLUP of the widest aggregation in the suite;
Q65 is `≤ 0.1*avg` over two full derived aggregates. If goopg sorts/aggregates
the full space before applying the rank filter where PG streams a bounded
sort, these two are (e). Members: Q67, Q65, possibly Q5's ROLLUP tail.

Unassigned on purpose: Q8, Q54, Q71 — plausible fits exist ((b)/(c)), but the
capture should say, not this list.

## Relation to M0125-0003 / -0005

Complementary, not competing: -0003 fixes the DP's **inputs** (relation
sizes); this line fixes **shapes** the size fallback cannot reach
(rescan-per-outer-row, CTE multiplication, missing pushdown). The arm-ON
capture is the cheap experiment that splits the 15 between the two lines.
Nothing here runs a timed arm, so -0003's "stage 2 timed arm must be read
before stage 3 lands" ordering is unaffected.

## Constraints

- Any goopg server start goes through the cgroup cap; never `pkill -f goopg`;
  stop via `goopg stop -D` / `server.sh stop sf05`.
- The tree carries uncommitted loop WIP (`sf05_ensure_bin` binary-identity
  stamping in `scripts/tpcds-sf05-regression.sh`, `GOOPG_BIN` override in
  `bench/tpcds/env_tpcds.sh`) — the capture uses `server.sh`/psql directly and
  neither depends on nor disturbs that WIP.
- The fix tasks filed by step 4 are planner changes: they inherit the standing
  bar for plan-shape commits (plan-diff `LABEL=tpcds-round2-head`, timed
  22-query TPC-H run on a quiet host, full SF0.5 gate).
