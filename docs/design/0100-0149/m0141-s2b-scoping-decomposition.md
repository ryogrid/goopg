# M0141-S2b — scoping decomposition: which upper rel needs the Pathlist surgery, and in what order

**Status:** accepted
**Milestone:** M0141 (AggSplit / plan-parity aggregation & sort strategy), plan-parity group
**Harness:** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Task:** `.ralph/fix_plan.md` M0141-S2b
**Outcome:** recon/decomposition only, no production change. S2b's own fix_plan
text calls for exactly this: "real multi-call-site upper-planner surgery, size
it as its own scoping task before attempting it (K24 already calls this the
workstream's largest single item; do not attempt in one sitting)."

## Correction to the record first

**`electOrderedGrouping` (`internal/optimizer/upperorderedgrouping.go`) already
implements the mechanism S2b's title describes — for GROUP_AGG only.** It
landed 2026-09-11 (`e8a1215fd`, "R47 slice 2 — ordered-level loop adjudicates
grouping survivors (K101) — 16 flips toward PG, ZERO EXTRA"), four days
*before* M0141-S2's 2026-09-15 dated finding and five days before S7's
2026-09-16 recon. Both of those tasks read `createOrderedPaths`/
`addOrderedPaths` (`upperordered.go:64-128`) directly and concluded "only one
synthetic candidate ever reaches the ORDER BY contest" — true of that pair of
functions in isolation, but planner.go:1960 calls `electOrderedGrouping`
*before* falling through to `createOrderedPaths`, and that function already
loops the GROUP_AGG rel's real `Pathlist`. Neither S2 nor S7 cites it. This is
not a wrong scope call by either task — S1's fresh 2026-09-15 capture (used by
S2) was taken *after* `electOrderedGrouping` was live and *still* finds
mechanism (B) alive in 6 TPC-H queries — so the gap is real, just misdescribed
as "the loop has never been built" when the accurate statement is "the loop
exists and still doesn't fire for these queries." Get this right before
generalizing further, or the wrong bottleneck gets patched.

## Per-call-site / per-upstream-rel census

`createOrderedPaths` has four callers (`planner.go:799,1965,2023,11022`). What
matters is not the caller but the *rel that fed it*, since that is where the
loop-vs-collapse mechanism lives.

| upstream rel | own Pathlist plurality? | loop-fix wired to ORDER BY? | code |
|---|---|---|---|
| **GROUP_AGG** (agg present, `planner.go:1965`) | **yes** — Hashed vs Sorted `PathAgg`, real cost contest (`groupingpaths.go`) | **yes** — `electOrderedGrouping`, landed 2026-09-11 | `upperorderedgrouping.go` |
| **DISTINCT** (`createDistinctPaths`, reached via `wrapMinMaxOrderByDistinct` line 11022 and the plain-DISTINCT wrapper) | **yes** — hashed vs unique-over-sorted, C-16a/b (`distinctpaths.go:71` `addDistinctPaths`) | **no** | `distinctpaths.go` |
| **WINDOW** (`createWindowPaths`, feeds line 1965/2023) | **no** — `seedPathForNode` always wraps exactly one input Node; the function's own comment: "goopg's input is a single finished Node, so that loop has one iteration and the rel gets one candidate" | n/a — nothing to loop yet | `windowsetoppaths.go:65-122` |
| **SETOP** (`createSetOpPaths`, feeds line 799) | **yes** — Append vs Hashed (`addSetOpPaths`) | **no**, and **not re-fetchable later**: allocated via `newUpperRelForNode` (a *fresh* rel every call, relids always 0), and the function's own comment says sharing one relids-0 rel across a `UNION`/`INTERSECT`/`EXCEPT` chain "returns the wrong subtree" — so even wiring a loop here needs a rel-identity fix first, a different problem from the other three | `windowsetoppaths.go:249-294` |
| **base join/scan** (no aggregation/window/distinct; the plain `SELECT ... FROM t1 JOIN t2 ... ORDER BY` case) | **presumed yes internally** (the DP join search's own dominance tournament calls `addPath` repeatedly per relids-set — see `[[planner_verify_both_candidates_generated]]`), but **discarded at the seam**: `upperorderedinput.go`'s C-07 half recovers only the search's *single cost-cheapest winner's* own pathkeys (`searchedTreePathkeys`, validated once against the published schema — never a second candidate) before collapsing to a `Node` | **no**, and this is the actual K24/F15/K12(B) item both S2 and S7 cite | `upperorderedinput.go` (file header states the coordinate-boundary risk explicitly: "bitten twice... the boundary map is a TOTALITY invariant that panics on any hole") |

Two structural notes that apply regardless of which rel is fixed:

1. **A fully generalized loop still only has two arms.** `addOrderedPaths`
   (`upperordered.go:122-128`) offers a candidate as-is when
   `pathkeysContainedIn` (full containment) or wraps it in a full
   `sortPathForBounded` otherwise — there is no third "prefix match →
   Incremental Sort" arm. Every slice below is therefore *necessary but not
   sufficient* for M0141-S7; S7's own third-arm work is still required on top
   regardless of which upstream rel gets fixed here.
2. **WINDOW cannot be fixed locally.** Its own rel never has more than one
   candidate; any value from generalizing it is entirely downstream of the
   base-join/scan fix (row 5), because that is what would let a *different*
   Node reach `createWindowPaths` as `input` in the first place.

## Working hypothesis for GROUP_AGG's residual gap (not traced this loop)

The likely reason `electOrderedGrouping` still declines for TPC-H Q4/Q5/Q8/
Q12/Q21/Q22 (S2's mechanism-B list, captured 2026-09-15 with the loop already
live) is its own gate at `upperorderedgrouping.go:166`:
`if len(cands) < 2 { return decline() }`. A genuine second (Sorted) `PathAgg`
candidate can only exist if the join/scan tree beneath grouping handed it a
sort-delivering input — and per the base-join row above, the join/scan search
forwards only its own single cost-cheapest winner as a `Node`, the same
one-candidate seam repeated one level down. If that winner isn't sorted-shaped,
grouping's own Hashed/Sorted contest never gets a Sorted candidate to compare,
`len(cands) < 2` fires, and the loop declines regardless of how correct its own
translation logic is. **This is a hypothesis, not a measurement** — confirming
it needs a `GOOPG_PGSHAPED_DP_TRACE=1` capture of the six queries checking
whether a Sorted `PathAgg` candidate is ever added to the GROUP_AGG rel at all.
Filed as S2b-0 below because it decides whether generalizing DISTINCT (S2b-1)
in isolation can move anything, or whether every remaining gap funnels through
the base-join fix (S2b-2) regardless of which upper rel sits above it.

## Recommended decomposition (replaces the single S2b task)

Ordered cheapest/lowest-risk first, **not** strictly by TPC-DS-witness count —
S2b-0 buys the cheap information that decides whether S2b-1 is worth doing
before S2b-2 lands.

1. **S2b-0 — trace-only recon.** `GOOPG_PGSHAPED_DP_TRACE=1` capture of TPC-H
   Q4/Q5/Q8/Q12/Q21/Q22, grep for `producer=` lines feeding the GROUP_AGG rel,
   confirm/refute whether a Sorted `PathAgg` candidate is ever offered. No
   production change. Settles the hypothesis above before further slices are
   attempted blind.
2. **S2b-1 — DISTINCT loop-fix (`electOrderedDistinct` /
   `distinctEmissionPathkeys`).** Mirrors `electOrderedGrouping`/
   `groupingEmissionPathkeys` almost exactly: DISTINCT already has the same
   internal two-candidate shape (hashed vs. unique-over-sorted) GROUP_AGG has.
   Cheapest net-new slice, no new plumbing, self-contained in
   `distinctpaths.go` + a new sibling file. Directly motivated by S7's TPC-DS
   "Unique ×1" witness, though S2b-0's answer may mean it *also* needs S2b-2
   before it can flip anything (same starvation risk as GROUP_AGG) — attempt
   and measure regardless; a null result here is itself informative and cheap
   to obtain.
3. **S2b-2 — base join/scan Pathlist across the search coordinate boundary.**
   The actual K24/F15/K12(B) item: publish the join/scan search's *surviving*
   candidates (not just the single winner `upperorderedinput.go` already
   recovers) past the collapse-to-`Node` seam, respecting the "VALIDATE, NEVER
   TRANSLATE" rule that file's header already established for the one-winner
   case. This is the large, genuinely risky slice — per K24's own warning, **do
   not attempt in one sitting**; its own first sub-task should be a further
   scoping pass (how the DP search's per-relids candidate set is currently
   discarded, and what a validated multi-candidate boundary crossing looks
   like) before any code changes. Unlocks: TPC-DS's Merge Join ×2 + Nested Loop
   ×4 (+ likely Subquery Scan ×1) S7 witnesses; is the prerequisite for S2b-3;
   and is the most likely fix for the six TPC-H mechanism-B queries if S2b-0
   confirms the starvation hypothesis.
4. **S2b-3 — WINDOW loop-fix.** Gated on S2b-2: `createWindowPaths` has
   nothing of its own to loop over until the join/scan seam beneath it can
   deliver more than one candidate `input` Node. Do not schedule before S2b-2
   lands.
5. **S2b-4 — SETOP rel-identity fix.** Independent problem (rel is
   allocated fresh per call, not keyed for later re-fetch), no TPC-H/TPC-DS
   witness currently motivates it (no `Incremental Sort` witness sits over a
   set operation in the 14-query census, and S2's six TPC-H queries are all
   plain SELECTs). Keep filed; do not schedule ahead of 1-3.

## S2b-0 result (2026-09-16) — the `len(cands) < 2` hypothesis is REFUTED

Measured, not inferred. A scratch `GOOPG_PGSHAPED_DP_TRACE=1` capture (throwaway
`cluster.New` server, TPC-H DDL + a small synthetic load — see "Why not the
shared `:65433` cluster" below) over Q4/Q5/Q8/Q12/Q21/Q22 with
`max_parallel_workers_per_gather = 0` forced (matching `estimate-audit
-plan-only`'s `-serial=true` default, `m0137-0003-baseline-capture-procedure.md`
§2) shows, for **every one of the six queries**, exactly two `DPPATH`
`producer=upper.groupagg.*` lines on the GROUP_AGG rel — one
`upper.groupagg.hashed`, one `upper.groupagg.sort` — **both `verdict=accepted`**.
`electOrderedGrouping`'s `cands` slice is therefore `len==2` for all six, every
time. The hypothesis this loop was filed to check ("the join/scan tree beneath
grouping never hands it a sort-shaped input, so `len(cands) < 2` fires") is
**false as stated** — a Sorted `PathAgg` candidate is offered and survives
`addPath` dominance for every one of the six witnesses.

**First attempt was contaminated and corrected in-loop.** An initial run
against `internal/testutil/tpch`'s existing `TestTPCHScaleLoadAndQueryRun`
(no `-serial` pin — that flag is `estimate-audit`-only, not a GUC this test
sets) showed the grouped rel's Pathlist ALSO growing
`upper.groupagg.{gathered,gathermerge,finalize,split}` entries — i.e. real
parallel Partial/Finalize-Agg candidates, because `max_parallel_workers_per_gather`
defaulted on. `electOrderedGrouping`'s own first loop
(`upperorderedgrouping.go:159-165`) declines **unconditionally** the instant it
sees a `PathFinalizeAgg` in the Pathlist, before ever counting `cands` — so
that first run could not have distinguished the `len(cands)<2` hypothesis from
simple parallel contamination, and mirrors
`[[goopg_tpch_bench_plans_are_all_parallel]]`'s standing warning almost
exactly. Re-run with `max_parallel_workers_per_gather = 0` (a scratch test
file's `AppendPostgresqlConf`, not a committed change — see below) removed
every parallel producer from the trace and gave the clean 2-candidate result
above.

**Why not the shared `:65433` TPC-H bench cluster.** Per `CLAUDE.md`'s
benchmark-clusters section and M0142-0003k, that cluster's `tpch` database is
currently emptied of its dataset (12 unrelated scratch tables in its place)
and its reload is a blocked, human-gated action
(M0142-0003i/-0003k(c)) — reconfirmed live this loop
(`\dt` on `:65433` still shows only `agg_data`/`lrs_*`/`mj_*`/`zz_*`, no
`lineitem`). This recon used a private throwaway server instead (same
precedent `m0137-0003-baseline-capture-procedure.md`'s own verification section
set): `internal/testutil/tpch`'s `cluster.New` + `DDL()` +
`tpch_scale_run_test.go`'s own `scaleLoader` at `GOOPG_TPCH_ORDERS=15000`
(SF≈0.01). This answers the *structural* question (is a candidate ever
offered at all) validly at any scale — `addGroupingPaths`'s HASHED/SORTED gate
logic (`groupingpaths.go:379-482`) is a function of the aggregate's own shape
(GROUP BY columns, special aggregates, GUCs), not of row-count statistics — but
a *cost-comparison* question (which candidate a Sort would pick as cheapest)
would need the real SF=1 load; none of this loop's findings below depend on
relative costs at scale, only on candidate presence/absence and translation
gates, both scale-independent.

**Second-level narrowing — the real decline point is one step further in,
UNCONFIRMED this loop.** With `cands` always `len==2`, `electOrderedGrouping`'s
next gate is `anyTranslated` (`upperorderedgrouping.go:169-175`):
`groupingEmissionPathkeys` is called on both candidates, and only the Sorted
one can ever return non-nil (`cand.AggStrategy != AggStrategySorted` short-circuits
the Hashed one to `nil` immediately, `upperorderedgrouping.go:65`). Reading
`groupingEmissionPathkeys` (`upperorderedgrouping.go:56-117`) against each
query's actual GROUP BY / ORDER BY clause (`internal/testutil/tpch/tpch.go`):

| query | GROUP BY | ORDER BY | plausible gate |
|---|---|---|---|
| Q4 | `o_orderpriority` (bare column) | same column, ASC | none obvious — should translate; **unexplained, see below** |
| Q5 | `n_name` (bare column) | `revenue desc` (an aggregate VALUE, not a group key) | **not a bug candidate**: no group-key-order Sort can ever satisfy an ORDER BY on an aggregate result — PG needs a Sort here too. Not yet cross-checked against the PG reference plan to confirm PG's shape matches goopg's for this query. |
| Q8 | `o_year` — inside the query text this is `extract(year from o_orderdate)`, but Q8's GROUP BY sits in the OUTER query over the `all_nations` derived table, so by the time `addGroupingPaths` sees `spec.GroupExprs` it may already be a plain `*ColumnRef` into the subquery's output, not the raw `extract(...)` — **not verified this loop; do not assume the bare-`*ColumnRef` gate at line 88 is the cause without reading the actual `GroupExprs` AST goopg builds for this query** | `o_year` (same alias) | unconfirmed — could be the `*ColumnRef`-only gate (line 88), could translate fine |
| Q12 | `l_shipmode` (bare column) | same column, ASC | none obvious — should translate; **unexplained, see below** |
| Q21 | `s_name` (bare column) | `numwait desc, s_name` — leading sort key is an aggregate VALUE (`count(*)`), `s_name` only secondary | same non-bug shape as Q5 — leading ORDER BY key isn't the group key, so no group-key Sort can front the requested order; not cross-checked against PG |
| Q22 | `cntrycode` — same derived-table shape as Q8 (`substr(c_phone,1,2)` aliased inside a FROM-subquery) | same alias | unconfirmed, same caveat as Q8 |

**What this loop does NOT claim**: it does NOT claim the `*ColumnRef`-only gate
in `groupingEmissionPathkeys` is a confirmed bug, and it does NOT claim Q4/Q12
are explained — both translate structurally per a *read* of the code but were
not traced live (the DPPATH channel does not print `anyTranslated`'s outcome
or the final `getCheapestFractionalPath` winner's shape; that needs either a
new trace line or a debugger/print-statement probe, out of scope for a
recon-only loop). **Resume point for whichever loop picks this up next**: add
a temporary trace line (or reuse `DPTRACE`) inside `electOrderedGrouping`
printing `anyTranslated` and, on `!anyTranslated`, which candidate(s) failed
which specific check inside `groupingEmissionPathkeys` — then re-run this same
six-query probe. That single instrumented run should fully resolve the table
above (both the Q4/Q12 mystery and the Q8/Q22 derived-table question) in one
pass, without needing to reload the shared cluster.

## Disposition

`.ralph/fix_plan.md` M0141-S2b is closed on this basis, same precedent as
M0140-0006: the task asked either to land the surgery or to record why it
can't be landed in one loop with concrete, loop-sized resume points. This doc
does the latter, and additionally corrects a mis-scoped premise both M0141-S2
and M0141-S7 carried forward (electOrderedGrouping already existing). No
production code changed. M0141-S7's fix_plan entry is updated in the same
commit to stop claiming uniformly "all 14 witnesses need M0141-S2b" — see the
per-rel table above for which witnesses map to which sub-task. Ledger row
appended (task-id `m0141-s2b`).
