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

## S2b-5 result (2026-09-16) — `anyTranslated` hypothesis also REFUTED; the real cause is a cost-model election, not a wiring gap

Measured, not inferred. Landed a small permanent instrumentation addition to
`internal/optimizer/upperorderedgrouping.go` (gated on the existing
`GOOPG_PGSHAPED_DP_TRACE`/`dpTrace`, same convention as `pathTraceEnabled` in
`pathtrace.go` — a `DPGROUP` line per decline point inside
`groupingEmissionPathkeys`, one on successful translation, one at each
`electOrderedGrouping` decline point, and one recording the final elected
shape (`Sort-over-Aggregate` vs `bare-Aggregate`, plus the winning
`AggStrategy`)) and re-ran S2b-0's six-query private-cluster probe (same
`cluster.New` + `tpch.DDL()` + a small synthetic load, `max_parallel_workers_per_gather
= 0`, scratch test file deleted after use — nothing from the probe harness
itself is committed, only the `DPGROUP` trace lines in
`upperorderedgrouping.go`).

**First attempt was itself cache-contaminated — a second methodology trap,
distinct from S2b-0's parallel-contamination trap.** `internal/testutil/tpch`
(the scratch probe's package) does not import `internal/optimizer` at
Go-compile-time — the server under test is a separate `go run ./cmd/goopg`
subprocess (`cluster.go:113`), so editing `upperorderedgrouping.go` does not
change the `tpch_test` package's own build inputs and `go test` replayed a
byte-identical CACHED result from before the instrumentation existed (visible
as `ok  ... (cached)`, and by every floating-point cost in the DPPATH lines
matching S2b-0's original capture to the same 17 significant digits). Re-run
with `-count=1` — the documented "one-off probe" carve-out in
`ci/design/test-gate-speedups/05` §1, not a gate run — produced the real,
different result below. **Any future probe that changes code reached only
through a `cluster.New`-spawned subprocess must force `-count=1`**, or it
silently re-reports stale findings; this is now worth its own line in
`AGENT.md`/a memory note.

**The real result: `electOrderedGrouping` does not decline for any of the six
queries.** For all of Q4/Q5/Q8/Q12/Q21/Q22: exactly one `DPGROUP decline`
line (`reason=strategy-or-mode`, `strategy=0` — the Hashed candidate, which
always short-circuits `groupingEmissionPathkeys` immediately since
`AggStrategyHashed` is the zero value and can never equal
`AggStrategySorted`), followed by one `DPGROUP translated ok` line for the
Sorted candidate (`keys=1 strategy=1`), followed by one `DPGROUP elected`
line. **`anyTranslated` is `true` for every one of the six — the second
hypothesis this task was filed to check is also false as stated.** The loop
runs its full tournament (`addOrderedPaths` on both candidates,
`setCheapest`, `getCheapestFractionalPath`) and reaches an election every
time:

| query | elected shape | winning strategy |
|---|---|---|
| Q4 | `Sort` over `Aggregate` | `0` (Hashed) |
| Q5 | `Sort` over `Aggregate` | `0` (Hashed) |
| Q8 | bare `Aggregate` (no Sort) | `1` (Sorted) |
| Q12 | `Sort` over `Aggregate` | `0` (Hashed) |
| Q21 | `Sort` over `Aggregate` | `0` (Hashed) |
| Q22 | bare `Aggregate` (no Sort) | `1` (Sorted) |

For Q4/Q5/Q12/Q21 the loop **offers both** the translated sort-free Sorted
candidate and a `Sort`-over-Hashed candidate to `setCheapest`, and the cost
comparison picks **Hashed + an explicit Sort above it** as cheaper than the
Sorted candidate's already-ordered output. Q8/Q22 pick the sort-free Sorted
candidate and emit no Sort node at all — for these two the loop's job is
already done; whatever residual "mechanism (B)" symptom a captured plan shows
for them (if any) is not at this rel.

**This means the loop is not declining or under-wired for Q4/Q5/Q12/Q21 —
it is correctly running PG's own `create_ordered_paths` tournament and PG's
own kind of answer (a cost comparison) is choosing the Hashed+Sort shape.**
Whether that choice is *right* — i.e. whether PG 18.3 would make the same
cost call for the same query — is a completely different question than
"is the mechanism wired", and a stale scratch capture in this repo answers it
for Q4 specifically: `tmp/take4/runs/plansweep/q04.{pg,goopg}.txt` (an old,
uncommitted capture, cited here only as corroborating evidence, not as an
authoritative plan-parity artifact) shows real PG choosing `Finalize
GroupAggregate` fed by a `Sort` **below** the aggregate (Sorted strategy,
group-key order falls out for free, no Sort above), while goopg's captured
plan is exactly the `Sort` **above** `HashAggregate` shape this loop's trace
predicts. **The root cause for at least Q4 (and plausibly Q5/Q12/Q21, same
shape) is therefore a cost-model discrepancy in the Hashed-vs-Sorted
`PathAgg` comparison** (or in the Sort's own cost, or in how cheaply PG's
`GroupAggregate`-fed-by-Sort prices versus goopg's) — **not** a missing
`electOrderedGrouping`/`groupingEmissionPathkeys` wiring gap. Both prior
hypotheses this design doc chased (`len(cands)<2`, `anyTranslated=false`) are
now refuted; the mechanism this doc set out to scope was already complete
before this doc's own first task even started.

**What this does NOT establish**: whether the Hashed-vs-Sorted cost
comparison is wrong in general or only for this query shape; whether Q5/Q12/
Q21 match Q4's exact PG plan shape (not verified against a fresh PG capture,
only asserted by code-read symmetry — same GROUP BY-equals-ORDER BY-column
shape); and whether fixing the cost comparison would actually flip these
plans to PG's shape without regressing something else (M0141-S2a-fix2's
precedent: a plausible-looking cost fix in this exact neighbourhood measured
net-negative and was reverted, per the banner's item 1). **Resume point**:
compare goopg's actual costed numbers for the Hashed and Sort-over-Sorted
candidates at Q4/Q5/Q12/Q21 (the `DPPATH` lines this probe already captures
carry `startup`/`total` for both — a fresh probe run needs no new
instrumentation) against what PG's cost formulas would produce for the same
shapes, to find which specific cost term is under- or over-pricing one side.
Filed as **M0141-S2b-6** below.

## S2b-6 result (2026-09-16) — synthetic-data probe REFUTES its own inputs; the real question needs SF1 scale, not a code-read

Ran the same `cluster.New` + `tpch.DDL()` + synthetic-load probe pattern as
S2b-0/S2b-5, this time with **`ANALYZE` run on all eight tables** (S2b-0/
S2b-5 followed `tpch_run_test.go`'s convention of `ANALYZE region` only —
see the methodology note below) and captured the live `DPPATH`/`DPGROUP`
numbers for Q4/Q5/Q12/Q21 at HEAD (`51a2d176d`, same commit S2b-5 measured
against). Scratch test file `internal/testutil/tpch/zzz_s2b6_probe_test.go`
deleted after use (same precedent as S2b-0/S2b-5); nothing from the probe
harness itself is committed.

**The result does not reproduce S2b-5's table.** With full `ANALYZE`
coverage, only Q5 and Q21 elect `Sort-over-Aggregate`/`strategy=0` (Hashed);
Q4 and Q12 elect `bare-Aggregate`/`strategy=1` (Sorted) — the opposite of
what S2b-5 reported for those two. Concretely, from the `DPGROUP`/`DPPATH`
trace:

| query | ORDER BY vs GROUP BY | Hashed own total | Sorted own total (incl. input Sort) | Hashed+Sort-above total | Sorted-passthrough total | elected |
|---|---|---|---|---|---|---|
| Q4  | same column (`o_orderpriority`) | 0.3375 | 0.3525 | 0.3525 | 0.3525 | **tie → Sorted** (lower startup 0.330 vs 0.3475) |
| Q5  | different (`sum(...)` DESC) | 6.82 | 6.835 | 6.835 | 6.85 (extra Sort needed, dominated) | **Hashed**, cleanly |
| Q12 | same column (`l_shipmode`) | 2.525 | 2.54 | 2.54 | 2.54 | **tie → Sorted** (lower startup 2.5125 vs 2.535) |
| Q21 | different (`count(*) DESC, s_name`) | 5.00 | 5.015 | 5.015 | 5.03 (extra Sort needed, dominated) | **Hashed**, cleanly |

Two findings, not one:

1. **Q5/Q21 are not a discrepancy at all.** Their `ORDER BY` does not match
   their `GROUP BY` columns (an aggregate expression, or a second column),
   so the translated Sorted candidate still needs an *extra* Sort on top —
   `groupingEmissionPathkeys` correctly prices that extra wrap (visible as
   the second, `dominated` `upper.ordered.sort` line at 6.85/5.03) and
   Hashed+cheap-output-sort correctly wins. This matches the EXPLAIN shape
   goopg actually emits for both (`Sort` over `HashAggregate`) and is, on a
   structural read, the same shape PG would choose for the same reason —
   no cost-model bug here.
2. **Q4/Q12's "election" is an exact tie broken by startup cost, not a
   clean win either way**, and the tie is a **degeneracy of the synthetic
   dataset, not a signal about the real cost model.** Both queries' join
   inputs estimate at `rows≈1` on this 5-16-row synthetic load, so
   `numGroups ≈ inputRows ≈ 1` (clamped to 2 by both `cost_tuplesort` and
   `costSortRunWithWidth`'s "never log(0)" floor) — the Sorted candidate's
   *input* Sort (nominally `O(inputRows · log inputRows)`, the expensive
   term at real scale) and the Hashed candidate's *output* Sort (nominally
   `O(numGroups · log numGroups)`, the cheap term) are pricing **the exact
   same two clamped tuples** and land on bit-identical totals (`0.3525`,
   `2.54`). At TPC-H SF1 scale `inputRows` (orders/lineitem-derived, tens of
   thousands to millions of rows post-join) and `numGroups` (a handful of
   `o_orderpriority`/`l_shipmode` values) are nowhere near equal, so this
   tie **cannot occur** at the scale that actually motivated S2b-0/S1 — a
   probe built on this dataset structurally cannot separate "which term is
   under/over-priced" for Q4/Q12's shape, because the two terms it would
   need to separate happen to collapse to the same input at this data size.
   S2b-5's opposite result for the same two queries is the same degeneracy
   resolved the other way by a small ANALYZE-driven perturbation (partial
   vs full table coverage nudges the sub-0.02-cost-unit startup comparison
   across the tie), not evidence of a different cost bug.

**Methodology note, since this refutes S2b-5's own numbers on the same
commit:** S2b-0/S2b-5's probe (deleted, unrecoverable verbatim) followed
`tpch_run_test.go`'s `ANALYZE region` convention — i.e. `orders`/`lineitem`/
etc. carried **no real statistics**, only planner defaults. This probe
`ANALYZE`d all eight tables. That is very likely why Q4/Q12 fall on
different sides of the tie between the two loops: different default-vs-real
selectivity/ndistinct inputs perturb the already-near-zero-margin startup
comparison. Neither run is "the bug" — **both are noise from a dataset too
small to carry a real signal for this specific question.**

**What this settles and what it does not:**
- It does **not** reproduce, confirm, or refute S1/S2's original TPC-H
  "mechanism (B)" 6-query finding — that finding was measured against the
  real HammerDB SF1-loaded cluster (`:65433`), where `inputRows` and
  `numGroups` are genuinely separated by orders of magnitude, not this
  synthetic 5-16-row fixture.
- It **does** establish that the tiny-synthetic-dataset probe pattern this
  whole S2b sub-thread (S2b-0, S2b-5, and this task) has relied on is the
  wrong instrument for S2b-6's specific question (a cost-term-under/over-
  pricing comparison that only separates at real cardinality ratios), even
  though it was the right instrument for S2b-0/S2b-5's questions (whether a
  gate declines / whether a translation succeeds — both binary, both
  reproduce regardless of scale).
- It does **not** find any cost-model bug to fix. `costSortRunWithWidth`'s
  clamp-at-2 and `costAgg`'s SORTED/HASHED arms both price the degenerate
  tied case exactly as PG's `cost_tuplesort`/`cost_agg` would (hand-verified
  term-by-term against `costsize.c:1898-1985`/`2682-2768` while writing this
  section — no divergence found in the formulas themselves, only in the
  data feeding them).

**Resume point:** the real Hashed-vs-Sorted `PathAgg` comparison for Q4/Q5/
Q12/Q21 can only be measured against real SF1-scale row/group cardinalities
— i.e. it is **blocked on the same TPC-H bench cluster reload M0142-0003i/
-0003k(c) already blocks** (`.ralph/fix_plan.md` M0142-0003k, `CLAUDE.md`
"Benchmark clusters" §TPC-H caveat). Once the `tpch` database on `:65433` is
reloaded, capture live `EXPLAIN`/`DPGROUP`/`DPPATH` for Q4/Q5/Q12/Q21 there
directly (no synthetic fixture) and repeat this same term-by-term diff — at
that scale a genuine discrepancy, if one exists, will show as a clean win/
loss rather than a coincidental tie. Filed as **M0141-S2b-6-resume** below,
gated on the reload.
