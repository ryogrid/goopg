# Milestone 0125 — TPC-DS timeout class & planner expression-walker extinction

**Status:** planned
**Filed:** 2026-07-28
**Reference plan:** `.ralph/fix_plan.md` (M0125 section)
**Parent audit:** `docs/design/tpcds-round2-fixes/README.md` §13 — implements §13.5 actions **2, 3, 4**
**Prerequisites:** M0124-0002 (the plan baseline every A/B diffs against) and M0124-0005
(value checksums, since two tasks here cannot be accepted on row counts). See
"The M0125-0004 scheduling conflict" below for the one case where M-NIGHTLY forces an
exception. **M0125-0012 … -0015 are not gated by M0124-0005** — each differs from PG in
rows or raises an ERROR. **M0125-0006/-0007/-0008 are the opposite**: they return PG's row
count with wrong values, so they are exactly the class M0124-0005 exists for. See "Scope,
and how it grew" below.
**Branch:** `tpcds-fix2`

## Goal

Close the three engine-side actions §13.5 names, under a constraint round 2 never had to
face: **goopg's planner sits on a measured trade-off curve, and every item here moves along
it.**

## Scope, and how it grew (2026-07-28 → 2026-07-29)

This milestone was filed against §13.5's three engine-side actions. It has since absorbed
**ten further tasks** that §13.5 could not have named, because they did not exist as
observations when it was written. Recording the growth here so the milestone is not read as
having drifted:

| wave | tasks | why they were not in §13.5 |
|---|---|---|
| filed 2026-07-28 | M0125-0001 … -0005 | §13.5 actions 2, 3, 4 — the original scope |
| **M0124-0001 sweep, chunks 11–13** | M0125-0006 (Q87), -0007 (Q16/Q94/Q95), -0008 (Q94) | Found by **value** comparison. §13.3's protocol classified a cell by status and row count only, so this class was structurally invisible to it |
| **M0124-0006 attribution** | M0125-0009 (10 queries), **-0010** (4 queries, NEW) | Same cause — 23 of 99 `OK`/`OK` equal-row-count cells diverged by value; 18 were real defects |
| **M0125-0009 acceptance run** | M0125-0011 (Q97) | A residual isolated only after -0009 removed the collapse masking it |
| **2026-07-29, this milestone doc** | **M0125-0012 (Q8), -0013 (Q47), -0014 (Q49), -0015 (Q51)** | The last four TPC-DS defects with **no owning task**. See below |
| **USER directives, 2026-07-30** | M0125-0026 (plan capture); **M0125-0028 … -0031 (the warm-statistics programme)** | Filed by the user's interactive session. -0026 adds the missing instrument (goopg-vs-PG EXPLAIN). -0028 … -0031 flip the milestone's standing premise: statistics become durable (restart-surviving, with an explicit user waiver on PG-faithfulness for the persistence mechanism), `ANALYZE <table>` works in per-DB databases, the bench clusters get warm stats + CHECKPOINT — and all later measurement assumes WARM statistics. -0031 then owes the timeout class **elimination**, not just a verdict. Design: `docs/design/0125-0028-warm-stats-programme.md` |

### Why Q8 / Q47 / Q49 / Q51 moved from "ledger row" to "task"

An earlier revision of this document listed **Q47's downstream windowed self-join defect,
Q49's one-row gap and Q51** under **Out of scope**, on the reasoning that "all get a ledger
row from M0124-0003 so they are not orphaned by this milestone's completion". Two things
have since made that insufficient:

1. **A ledger row is a backlog entry, not a schedule.** M0119 consumes
   `.ralph/deferral_ledger.md` as a work queue, and M0124-0003's own finding was precisely
   that *a deferral that never reaches the queue is a deferral that was never scheduled*.
   Being in the ledger is a necessary condition for being worked, not a sufficient one.
2. **The ledger row itself asks for tasks.** `tpcds-round2 q47-q49-q51` (2026-07-29) closes
   with "**All three are separate fix_plan items, not one**" — RC-1b moved Q47's CTE body
   and left Q49 and Q51 untouched, which *disproves* the single-family attribution that had
   let them be tracked as one line. Keeping them as one ledger row contradicted the finding
   that produced it.

**Q8 was never in the out-of-scope list.** It simply had no task, despite being the sole
unresolved member of round 1's nine goopg-only errors and one of only two `ERROR` cells in
the SF=1 sweep. That is an omission, not a decision.

**Acceptance consequence — measured, not assumed.** All four differ from PG in **row
count** or raise an **ERROR**, so unlike M0125-0006/-0009/-0010 they do not depend on
M0124-0005's checksum for acceptance. What does **not** follow is that the standing gates
see them. Checked against `bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260729-093056.txt`
(HEAD) and `ci/batch/tpcds-row-anchors.csv`:

| | SF0.5 gate at HEAD | nightly anchors |
|---|---|---|
| Q8 | **sees it** — `ERROR 12s` | **absent** |
| Q47 | **sees it** — `MISMATCH 43s goopg=0 oracle=100` | **absent** |
| Q49 | `PASS 25 rows`, checksum matches — **blind** | **absent** |
| Q51 | `PASS 100 rows`, `ck=n/a` — **blind** | **absent** |

The anchor CSV pins 61 queries and holds **none of these four**, so closing any of them
means **adding** an anchor, not re-pinning one. And **Q49 and Q51 flipped MISMATCH → PASS at
SF0.5 the moment M0125-0009 landed** (`sweep-20260729-004730` at `7a7a2639` vs
`sweep-20260729-033758` at `3fbce36a`) — a side effect no completion note or ledger row
records, and neither has been re-measured at SF=1 since. That is why -0014 and -0015 are
written as "re-measure, then resolve or classify" rather than as fixes.

Q47 and Q51 additionally carry `ck=n/a` in the SF0.5 oracle (a `LIMIT` over a non-total
`ORDER BY`), so **value** acceptance for those two is SF=1 only.

All four remain planner/executor changes, so `make plan-diff` applies — with M0125-0004's
`r5-default` fallback until M0124-0002 lands the `tpcds-round2-head` label.

This paragraph is the single home for the gate-visibility claim; other sections reference it
rather than restating it. Restating it is how the earlier draft ended up asserting, in the
same commit that recorded M0125-0011's measured refutation of exactly this reasoning, that
all four were anchor-visible.

## The constraint this milestone is organised around

- `analysis/tpch-evolution-round4-parallel-query-20260722.md` **§2/§5** — enabling
  statistics fixed TPC-H **Q5 22.8×** (415.2 → 18.2 s) and regressed **Q22 128×, Q4 79×,
  Q8 53×, Q2 26×, Q12 4.4×**, taking the serial stream **1162 → 1307 s** (12 % *slower*).
- `analysis/tpch-evolution-round5-int64-hashjoin-20260724.md` **§6** — the cost-driven
  join-order planner is **4 wins / 6 regressions / 12 neutral**: it repairs Q2 18.8× and
  Q8 4.1×, and creates star-query regressions by dropping MultiHashJoin — Q5 and Q21 hang,
  Q9 times out, Q10 11.4×, Q18 4.3×, Q7 1.9×. It ships **off by default** for that reason.

> **A row-count gate cannot see this class.** Every query that completed in round-5 §6
> returned **identical rows** while running 1.9× to non-completing slower.
> `scripts/tpch-spotcheck.sh` is a Q12/Q13 row-count gate. Plan-shape commits here need a
> **timed** 22-query TPC-H power run plus a **label-pinned** plan diff.

Round-5's *absolute* seconds are not a valid baseline either: the round-5 fix bundle cut the
stream 1086 → 325 s while changing no plans and no rows, so only signs and ratios transfer.
M0124-0002's arm B is the baseline.

## A coupling that was investigated and found NOT to exist

An earlier draft of this milestone claimed `localizeExprToLeaf` was reached ungated by
`estimateBaseRelInfo` (`internal/planner/cardinality.go:145`) and was dormant only because
`baseRows` is 0 on an S-cold server — making M0125-0003 the thing that "wakes" an
unconverted walker, and M0125-0002 a hard prerequisite for it.

**That is false, and the record is kept here so it is not re-derived.** The `local`
argument comes from `locals.byBinding[i]`, and `locals` is populated **only** inside
`if shouldAttachBeforeMHJ(ctx.bindings)` (`internal/planner/bushy.go:158-169`).
`estimateBaseRelInfo` returns early on `local == nil` (`cardinality.go:142`) *before*
`baseRows` is consulted. So with the gate closed the walker is unreachable at that site
regardless of relation sizes; and when the gate **is** open, `attachRelationLocalFilters`
(`bushy.go:219`) already calls the same walker on the same predicates today.

Consequence: **M0125-0003's stages do not depend on M0125-0002.** The two tasks are
independent, and the ordering below is by cost and measurability alone.

## Required Design Docs

| Task | Content | §13.5 | Design doc |
|---|---|---|---|
| **M0125-0001** | `internal/planner/exprwalk.go` child-slot primitive + drivers + `go/ast` exhaustiveness gate + the §2.6 pins. No call site converted. | 4a | `0125-0001-exprwalk-driver-and-exhaustiveness-gate.md` |
| **M0125-0002** | Convert the seven remaining §2.4 walkers, one per commit, lowest blast radius first. | 4b | `0125-0002-walker-conversion-and-mhj-composition-risk.md` |
| **M0125-0003** | `tableRows` fallback modelled on `table_block_relation_estimate_size`, behind `GOOPG_RELSIZE_FALLBACK` defaulting **off**, staged by consumer. | 2 | `0125-0003-relsize-fallback-and-tpch-stats-tradeoff.md` |
| **M0125-0004** | Q75: push single-side quals onto inner-join **inputs**, scoped so it cannot re-open the `shouldAttachBeforeMHJ` Q8/Q21 regression. | 3 | `0125-0004-q75-join-residual-evaluation-order.md` |
| **M0125-0005** | The `GOOPG_RELSIZE_FALLBACK` default flip — its own commit, its own decision, its own evidence. | 2 (rider) | `0125-0005-relsize-fallback-default-flip.md` |
| **M0125-0006** | Set-operation chains re-associate right when branches are parenthesised (Q87). Fix in the parser: `Parenthesized` must describe the node as it stood at the closing paren. | — (sweep) | `0125-0006-setop-chain-associativity.md` |
| **M0125-0007** | Date input rejects unpadded month/day and the comparison path fails **silently** (Q16/Q94/Q95). Shared PG-faithful field decoder over per-site `time.Parse` layouts. | — (sweep) | `0125-0007-pg-faithful-date-field-decode.md` |
| **M0125-0008** | `EXISTS` + `NOT EXISTS` on one outer relation yields a **non-subset** result (Q94): adding a conjunct grows the result 11 → 25. Semi/Anti residual ↔ source-table mapping. | — (sweep) | `0125-0008-semi-anti-conjunction-residual.md` |
| **M0125-0009** | `parserExprKey`'s `expr:%T` fallback keyed on the Go TYPE NAME; structural key + two-part exhaustiveness gate. **§9 covers M0125-0010** (the FROM-subquery `Project` remap binding by function name). | — (sweep) | `0125-0009-parser-expr-key-structural.md` |
| **M0125-0011** | FULL OUTER JOIN drops all but the first conjunct of its `ON` (Q97). | — (sweep) | `0125-0011-full-outer-join-on-conjunct-drop.md` |
| **M0125-0012** | Q8: a `ColumnRef` below a FROM-subquery `Project` keeps its OUTER-scope index. Extend the subquery-scope remap past `Project` targets, **composing with** M0125-0010's verify-then-repair narrowing. | — (round 1) | `0125-0012-q8-subquery-scope-index-remap.md` |
| **M0125-0013** | Q47: the second defect, in the windowed self-join layers above the CTE body RC-1b repaired. Also settles the 8.4 × runtime question the two primary sources disagree on. | — (ledger) | `0125-0013-q47-q49-q51-three-distinct-defects.md` § Q47 |
| **M0125-0014** | Q49: 30 rows vs PG's 34 (exactly 3 × 10). `rank()` peer-ties ruled out; candidates are `decimal(15,4)` ratio precision and the outer-join-plus-comma-join shape. | — (ledger) | `0125-0013-q47-q49-q51-three-distinct-defects.md` § Q49 |
| **M0125-0015** | Q51: 0 rows vs PG's 100, third distinct defect, budget-marginal on the `OK` side (13 s headroom). | — (ledger) | `0125-0013-q47-q49-q51-three-distinct-defects.md` § Q51 |

## Order

The order below covers the **original five**. The sweep-discovered and ledger-adopted tasks
(-0006 … -0015) are interleaved by the rule stated after it.

1. **M0125-0004 (Q75)** — first. Smallest, a failure this programme caused, and a **live CI
   break**: query 75 is in the nightly TPC-DS qualifying set with `Q75,100,pinned` at
   `ci/batch/tpcds-row-anchors.csv:46` and **no** `expected-failures.csv` entry.
2. **M0125-0001** — inert (no call site), hard prerequisite for 0002.
3. **M0125-0003** — all stages; flag-off throughout, so the whole task is inert. Front-loads
   §13.5's highest-value item and the finding that would most change this milestone's
   cost/benefit: if real sizes do not move the timeout class, the eight-commit walker
   programme is no longer worth its gate budget.
4. **M0125-0002** — the expensive one. Re-run 0003's C1/C2 arms afterwards if plan shape moved.
5. **M0125-0005** — last, and only on evidence.

### Interleaving rule for the correctness tasks (-0006 … -0015)

These are **wrong-answer and error defects, not plan-shape work**, so they do not compete
with -0001/-0002/-0003 for the 12–20 h gate budget and should be taken whenever the
plan-shape track is blocked on M0124-0002. Within them, order by *how much diagnosis is
already banked*, cheapest first — the recommended sequence, with the two completed ones
shown for continuity:

1. ~~**M0125-0009**~~ (done) → ~~**M0125-0010**~~ (done) → ~~**M0125-0011**~~ (done) — each
   was a one-line root cause with the evidence already collected.
2. **M0125-0014 (Q49)** and **M0125-0015 (Q51)** — **first, and cheapest, because each is
   one measurement.** Both flipped to `PASS` at SF0.5 when M0125-0009 landed and neither has
   been re-measured at SF=1 since (see "Acceptance consequence" above). One SF=1 observation
   each — Q49 ~80 s, Q51 ~590 s — either closes the task as *measured-and-already-fixed* or
   produces the first HEAD-valid reproduction anyone has of it. Doing them before the harder
   items also stops the milestone from carrying two tasks whose stated premises are stale.
3. **M0125-0013 (Q47)** — the only one of the four with a concrete, still-valid resume point
   (start below the CTE at the `v1`→`v2` window layers, reproducible for the first time
   because RC-1b made the input non-empty; SF0.5 reproduces the row gap in 43 s). It also
   owes a **step-0** `EXPLAIN` diff settling a documented contradiction between two primary
   sources about its 8.4 × runtime move — step 0 because the diff is only interpretable
   before the fix moves plan shape.
4. **M0125-0006 (Q87)** and **M0125-0007 (Q16/Q94/Q95)** — mechanism fully root-caused,
   fix location named; -0007 is a codec change and therefore also owes the full regress-port
   suite (Hard-won Rule #5).
5. **M0125-0012 (Q8)** — **prefer taking this after M0125-0001.** The fix extends a
   subquery-scope remap to new node kinds; routing those through the driver is the whole
   point of -0001, and hand-rolling a fifth walker re-creates the copy-paste family
   -0001/-0002 exist to delete. It must also compose with -0010's verify-then-repair
   narrowing rather than revert it.
6. **M0125-0008 (Q94's second defect)** — last. Needs -0007 landed first to isolate it, and
   is a Semi/Anti residual ↔ source-table mapping change, i.e. Hard-won Rule #2 territory.

If Q49 or Q51 survives its step-0 measurement, it re-enters this list **after** -0012: at
that point it is the least-informed item in the set, and Q51 in particular costs ~590 s per
observation with only 13 s of budget headroom.

### The M0125-0004 scheduling conflict, resolved explicitly

M-NIGHTLY preempts by its own charter and will raise Q75 as a nightly anchor failure. But
M0125-0004's own Gate requires `make plan-diff LABEL=tpcds-round2-head` — a label
`plan_snapshots/` does not contain until **M0124-0002** creates it — and its acceptance is by
value, which needs **M0124-0005**. Following both rules literally deadlocks.

Resolution, in priority order:

- If M0124-0002 and M0124-0005 have landed, M0125-0004 runs with its full gate.
- If M-NIGHTLY forces Q75 **before** them, land it against the **`r5-default`** snapshot label
  with the SF0.5 gate at row-count only, and record in the commit message that the full gate is
  **outstanding**. Re-run both the label-pinned plan-diff and the checksum acceptance once
  M0124-0002/-0005 land; the task is not `[x]` until they pass.
- Q75 must **not** land inside M0124-0001's 8–10 h sweep window: `0124-0001` D1 requires the
  sweep commit to be an ancestor of every M0125 commit, and a code change mid-sweep voids the
  sweep. Coordinate on the sweep, not on the nightly.

**Gate budget, stated because it is large.** M0125-0002 is 7–8 commits ×
{units + label-pinned plan-diff + timed 22-query TPC-H + pre-commit pgbench} ≈ 12–20 h,
across two clusters (65433, 65437). The SF0.5 sweep (~1 h) runs on the first and last commit
and on any commit whose plan-diff shows a hunk — not on all eight.

## Definition of Done

1. `internal/planner/exprwalk.go` exists; the exhaustiveness test fails when a 33rd `Expr`
   type is added **anywhere in package `planner`** without a slot entry (proven with a
   throwaway type). The gate parses the *package*, not `plan.go` alone: the `exprNode()`
   marker is unexported, which closes the set to other **packages** but not to other **files**
   in this one.
2. The seven §2.4 walkers route through the driver and carry a `default:` arm, **and
   `remapByPosMap` is re-based as the eighth commit** — it is the walker §0 names as the
   defect and it still lacks a `default:`. "The class is extinct" is not claimed regardless,
   since `walkColumnRefsImpl` and the `shiftColumnRefs` closure stay out of scope with a
   ledger row.
3. Each walker-conversion commit has an `analysis/` per-query TPC-H table and a plan-diff
   verdict, with every hunk enumerated and justified in the commit message.
4. `GOOPG_RELSIZE_FALLBACK` exists, defaults off, and a unit test asserts it does **not**
   fire when `Stats.RowCount > 0`.
5. `analysis/tpch-relsize-fallback-<date>.md` records the four-arm matrix per stage, with the
   watch list of round-4 §5's five regressed queries pre-registered before the run.
6. Q75 returns PG's row count **and** its `all_sales` CTE aggregates equal PG's per year and
   column, with `zerogroups = 1` preserved.
7. **The four adopted defects are closed by value at SF=1**, each against
   `analysis/tpcds-sf1-goopg-20260728.md`'s measured row:
   - **Q8** — no `ERROR`, rows = PG's 0, **and** the discriminating probe passes. "0 rows"
     alone is *not* acceptance: PG also returns 0 at SF=1, so an empty result is
     indistinguishable from the bug. A relaxed-predicate variant of the same
     INTERSECT-in-FROM shape, non-empty on PG, must match by value on both engines and ship
     as a unit test.
   - **Q47** — 100 rows = PG and values equal PG; and the 8.4 × runtime move is *attributed*
     (`EXPLAIN` diff against set A), with the verdict written back into whichever of
     `RESULTS.md` / `tpcds-sf1-goopg-20260728.md` currently disagrees.
   - **Q49** — 34 rows = PG **at SF=1**, values equal. "25 rows at SF0.5" is **not** an
     acceptance signal: HEAD already satisfies it. Closing by measurement (SF=1 already
     matches PG) is a valid outcome; record it, and UPDATE the ledger rows to name
     M0125-0009 as the fix.
   - **Q51** — 100 rows = PG **at SF=1**, values equal, **and the measured runtime
     recorded**; if the fix pushes it past 600 s, raise the budget for the acceptance run
     rather than reporting a regression (`analysis/tpcds-sf1-goopg-20260728.md` §5).
     Same measurement-closure clause as Q49. `ck=n/a` in the SF0.5 oracle, so SF0.5 cannot
     value-accept it.
8. **Nightly anchors ADDED, not re-pinned.** `ci/batch/tpcds-row-anchors.csv` pins 61
   queries and contains **no row for Q8, Q47, Q49 or Q51**, so none of them is protected
   against re-breaking today. Each task adds its anchor on close. Do **not** assume a
   row-count change is automatically visible — M0125-0011's measured negative result showed
   SF0.5 is a *regression* gate, not a *detection* gate, for join-residual defects, and
   Q49/Q51 are the same shape of surprise from the other direction (they went green at
   SF0.5 without anyone noticing).
9. Design docs indexed; milestone index row updated; status `accepted`.
10. **The timeout class has an explicit verdict.** This is the class the milestone is
    *named* after, and DoD 4/5 do not require a single one of the 17 to be resolved —
    M0125-0005 is defined so that "measured, and deliberately not flipped" is a successful
    completion. So state the outcome rather than letting it lapse: on completion of
    M0125-0003/-0005, re-measure all 17 goopg-only timeouts and classify each as
    **(a) resolved** or **(b) unresolved and handed to a named successor task or ledger
    row**. Two constraints on that re-measurement, both already established:
    **Q18 and Q35 are budget-marginal** — a verdict flip is a re-rolled coin, not a win
    (`analysis/tpcds-sf1-goopg-20260728.md` §5); and **Q72 and Q64 are "unbounded AND
    unvalidatable"** — if either completes, it must be validated on **row count**, not on
    the fact that it finished, because their pre-existing row gaps became unobservable when
    they entered the timeout class.
    **Raised 2026-07-30 (user directive):** M0125-0031 lifts this DoD from *verdict* to
    **elimination** — zero goopg-only TIMEOUTs at the SF0.5 gate under the warm-statistics
    premise (-0028 … -0030). The (a)/(b) classification above remains the intermediate
    deliverable, and both re-measurement constraints still apply.

## Out of scope

- **Phase 6.2**, the greedy join-order fallback for `n > 12` (`bushy.go:93`). Not in §13.5;
  review finding B3 showed it does not fix Q64 alone — `query64.sql`'s FROM references the
  `cs_ui` CTE, so `tryBushyDP` also declines at the non-scan-leaf gate. Ledger row; reopen
  after M0125-0005.
- **Opening `shouldAttachBeforeMHJ`'s `SmallDimension` gate** (§7.3 RC-5). Ledger row from
  M0124-0003; reopen criterion is "after M0125-0002 **and** M0125-0005". Changing the
  walkers and the gate that masks them at once is the one experiment guaranteed to be
  uninterpretable.
- **`pg_class.reltuples` rendering 0.** §7.1 lists it as a consequence, but it reads
  `t.Stats.RowCount` directly (`internal/catalog/catalog.go:6977`), so a planner-side
  fallback cannot fix it and must not promise to.
- ~~Persisting `reltuples`/`relpages` (`pq-P10` option (a)) — still the alternative to 6.1.~~ —
  **MOVED INTO SCOPE 2026-07-30** as **M0125-0029** by user directive, which also waives the
  PG-faithfulness bar for the persistence mechanism (goopg's `pg_class` is virtual, so
  `reltuples` has no faithful heap home; a goopg-private mechanism is authorized). The
  relation-size fallback (6.1, M0125-0003/-0005) is thereby re-scoped from primary line to
  **S-cold safety net** — inert on warm clusters by its `RowCount > 0` early-return.
- ~~**Q47's downstream windowed self-join defect, Q49's one-row gap, Q51**~~ — **MOVED INTO
  SCOPE 2026-07-29** as M0125-0013 / -0014 / -0015. See "Why Q8 / Q47 / Q49 / Q51 moved from
  'ledger row' to 'task'" above. Their ledger rows stay as the evidence record; the tasks
  are now the schedule. **Q8** was adopted in the same pass as M0125-0012 — it had never
  appeared in this list, and had no task either.
- **§13.1 phase 0.2's unfinished panic → `XX000` half.** Still out of scope, still a ledger
  row from M0124-0003, and now the **only** member of that group. `internal/server/` holds
  exactly one `recover()` (`server.go:780`), which logs `backend goroutine panic` and closes
  the socket instead of emitting `ErrorResponse` + `ReadyForQuery`. It is deliberately not
  adopted here: it is a server-lifecycle change with no TPC-DS query of its own, and the
  bounds check that landed in `9740fce9` removed its forcing function. Reopen criterion —
  the next panic that escapes to `serveConn` (Q39's `exactIntVariance` was the last one,
  and it is fixed).

## PostgreSQL References

- `postgres/src/backend/access/table/tableam.c` — `table_block_relation_estimate_size`
- `postgres/src/backend/access/heap/heapam_handler.c` — `heapam_estimate_rel_size`
- `postgres/src/backend/optimizer/util/plancat.c` — `estimate_rel_size` dispatch
- `postgres/src/backend/optimizer/plan/initsplan.c` — `distribute_restrictinfo_to_rels`
- `postgres/src/backend/optimizer/plan/createplan.c` — `order_qual_clauses`
- `postgres/src/backend/utils/adt/numeric.c` — `sqrt_var` (checksum float normalisation)
