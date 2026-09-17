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

## S2b-1 result (2026-09-16) — landed; a real bug found and fixed along the way; the corpus has no witness that flips

Implemented, not recon: `internal/optimizer/upperordereddistinct.go`
(`distinctEmissionPathkeys`, `electOrderedDistinct`), mirroring
`groupingEmissionPathkeys`/`electOrderedGrouping` for the DISTINCT upper rel.
Wired into `planner.go`'s `s.Distinct` arm, replacing the legacy
`distinctOutputSatisfiesOrder` static check as the FIRST thing tried (the
legacy check + unconditional-Sort fallback still runs on decline — same
"never regress, only widen" contract grouping's own wiring uses).

**Simpler than grouping in one respect**: DISTINCT never renames or reorders
columns (`p.Distinct.schema` is always the pre-distinct child's own schema),
so there is no input-to-output coordinate boundary to translate across —
`distinctEmissionPathkeys` only has to answer "does this candidate's
executor contract guarantee a fixed order", not "where did this expression
move to". Both shapes `addDistinctPaths` builds are unconditionally,
statically ordered: `distinctOp` (hashed) hash-dedups then always re-sorts
ascending/nulls-last over every output column (`operators_distinct.go`), and
`distinctOnOp` (unique) streams its producer Sort's order verbatim — no cost
dependency, no data dependency, unlike grouping's `AggStrategySorted`-only
gate.

**A real bug found and fixed before landing, not by code review but by live
verification.** The first version reused the caller's shared `upperRels`
registry for `electOrderedDistinct`'s internal `UpperOrdered` rel fetch,
exactly like `electOrderedGrouping` does. That is safe for grouping because
`electOrderedGrouping`'s call site is the FIRST thing to touch the
statement's `(UpperOrdered, 0)` entry. It is NOT safe for DISTINCT: per the
per-call-site census above, planner.go's `s.Distinct` wrapper runs AFTER the
*generic* ORDER BY block already called `createOrderedPaths` once for the
SAME `s.OrderBy` keys, over the PRE-distinct child (the file's own comment:
"Applied after sorting so ORDER BY is respected" — a structural inversion
from PG's real DISTINCT-then-ORDER-BY sequence, not something this task
changed). Sharing the registry meant `setCheapest` could — and, live, did —
pick that stale, semantically unrelated Sort candidate (built over a Node
that is not even `distinctNode`) right back out from under the election.
Caught by running the FIXED build against the git-tracked TPC-DS SF0.25
cluster (`bench/tpcds/server.sh start sf025`, a private `GOOPG_BIN` per
`[[goopg_bench_bin_shared_with_nightly_lane]]`) on Q41 (`select distinct
i_product_name from item ... order by i_product_name limit 100` — the one
TPC-DS query with both a real `SELECT DISTINCT` and a matching ORDER BY; see
below for why `bench/tpcds/plans-pg/Q49.txt`'s `Unique` is NOT this
mechanism's witness) with `GOOPG_PGSHAPED_DP_TRACE=1`: the trace showed a
successful 2-candidate translation followed by `loop-decline
reason=sort-child-not-distinct` — the tournament's own winner was a `*Sort`
wrapping something other than `*Distinct`/`*DistinctOn`, tripping the
shape-gate defensively (a wrong answer was never emitted — the decline
correctly fell back to the legacy path — but the mechanism was not doing its
intended job). Fix: `electOrderedDistinct` now fetches its `UpperOrdered` rel
from a **dedicated, throwaway `newUpperRels()`**, never the caller's `u` —
isolating the election from anything an earlier stage of the SAME statement
already put there. This also let the decline path drop the
snapshot/restore-on-decline machinery grouping's version needs (nothing
shared to restore). Regression-tested directly:
`TestElectOrderedDistinctIgnoresStalePreDistinctOrderedEntry` pre-populates
`(UpperOrdered, 0)` on the shared registry with an artificially cheap stale
Sort before calling `electOrderedDistinct`, and was verified to FAIL against
the pre-fix code (confirmed by temporarily reverting the one-line fix and
re-running) and PASS against the fix.

**Verified live, not just unit-tested.** Rebuilt goopg with the fix, started
the SF0.25 cluster, ran `EXPLAIN` on Q41 with `GOOPG_PGSHAPED_DP_TRACE=1`:
trace now ends `DPDISTINCT elected shape=bare-*optimizer.Distinct` (the
mechanism actively elects, not merely declines-to-a-lucky-legacy-answer),
plan is `Limit -> Unique -> Sort -> Seq Scan` — byte-shape-identical to
`bench/tpcds/plans-pg/Q41.txt`'s PG reference. Diffed against the pre-S2b-1
build (`git stash` of just the `planner.go` hunk, same binary rebuild
procedure) to separate "already correct" from "fixed by this task": **Q41
was already correct before S2b-1** (its ORDER-BY-blind cost tournament picks
Hashed, whose node type `*Distinct` already passed the legacy
`distinctOutputSatisfiesOrder` check) — S2b-1 does not regress it, but does
not flip it either. Forcing `set enable_hashagg=off` to make Unique
(`*DistinctOn`, the exact case the legacy check could never recognize) the
*only* survivor demonstrates the mechanism declines cleanly
(`cands<2(1)` — `addPath`'s own dominance tournament prunes the disabled
Hashed candidate before `electOrderedDistinct` ever sees it, so there is
nothing to compare, by the same design grouping's own `cands<2` gate uses)
rather than misfiring — a known, intentionally-scoped-out edge (a genuine
single-survivor "just check its own order" case would be a real widening,
not implemented here, no witness currently motivates it).

**Why the corpus shows no flip, and why `Unique x1` was the wrong witness
name.** `.ralph/fix_plan.md`'s own mapping ("`Unique`x1 -> **M0141-S2b-1**")
pointed at Q49 (`docs/design/0100-0149/m0141-s7-readjudicate-and-scope-incremental-sort.md`'s
witness table). **That mapping is wrong**: reading `bench/tpcds/runtime_goopg/tpcds-data/queries/query49.sql`
shows its `Unique` comes from a plain `UNION` (three-way `SELECT ... UNION
SELECT ... UNION SELECT ...`, PG's own dedup strategy for non-ALL set
operations), which goopg plans through the entirely separate SETOP upper rel
(`addSetOpPaths`, `windowsetoppaths.go`) — **not** `createDistinctPaths`.
Q49's witness belongs to **S2b-4** (SETOP rel-identity), not S2b-1. Grepping
the actual TPC-DS query corpus for a real `SELECT DISTINCT` clause finds six
hits (Q6, Q38, Q41, Q54, Q87, plus a scratch `query_0.sql`); of those, only
Q41 pairs a `DISTINCT` clause with an outer `ORDER BY` on the same column —
every other hit's `DISTINCT` sits inside a scalar subquery or a set-operation
leg with no ORDER BY of its own, so `electOrderedDistinct` (which only ever
fires when `len(keys) > 0`) cannot apply to them regardless of correctness.
TPC-H has zero `SELECT DISTINCT` queries in its canonical 22 at all (it uses
`GROUP BY` throughout). **Net: S2b-1 is a genuine, tested, live-verified
generalization of the ORDER BY / DISTINCT interaction with a real bug caught
and fixed in the process, but the current TPC-H/TPC-DS corpus has exactly
one candidate query (Q41) and it already matched before this task — a null
result on the goal metric, honestly reported, same precedent as S2b-0/S2b-5/
S2b-6.** `.ralph/fix_plan.md`'s S7 witness table should be corrected to drop
the `Unique`x1 -> S2b-1 mapping and fold Q49 into S2b-4's scope instead;
noted here so a future loop does not re-expect S2b-1 to move Q49.

## S2b-2 result (2026-09-17) — the base-rel Pathlist surgery is REAL and reachable, but PROVABLY INERT until Incremental Sort exists

**Scope-only, no production diff.** S2b-2 as filed reads "base join/scan
Pathlist-across-the-search-boundary surgery... the real K24 item," with its
own warning to size it before writing code. This is that sizing pass.

**The seam, read first.** `createOrderedPaths` (`upperordered.go:64`) takes
exactly one `input Node` — the search's own already-materialized winner —
wraps it in a single `seed *Path` via `newPrebuiltPath`, and hands that one
seed to `addOrderedPaths` (`upperordered.go:122`), which offers it to the
`ORDERED` rel as-is if `seed.Pathkeys` already satisfies the ORDER BY, else
stacks a `*Sort` on it. Exactly one candidate ever reaches this tournament.
Contrast PG's oracle, `create_ordered_paths` (`planner.c:5308`): it loops
`input_rel->pathlist` — every surviving candidate from the DP search, not
just the cheapest — offering each (as-is or Sorted) to `add_path` on the
`ORDERED` rel, so a candidate that is not cheapest-total pre-Sort can still
win post-Sort (PG's own reason to keep more than one candidate alive at all).
R21 slice 2a already built the accessor this surgery would need:
`searchedRelOf(node) *RelOptInfo` (`searchedtree.go:169`) walks the boundary
wrapper chain and returns the search's own `RelOptInfo`, `Pathlist` included
— so **the missing plumbing is not "no way to reach the candidates," it is
"nothing downstream ever asks for more than the one seed already carries."**

**Live-traced against the full TPC-DS SF0.25 corpus** (temporary
`GOOPG_S2B2DEBUG=1`-gated `fmt.Fprintf` calls in `createOrderedPaths`,
printing `searchedRelOf(input)`'s `Rows`/`Pathlist` — length, cost, rows,
pathkeys count of every candidate — right where `seed` is built; reverted
before commit via `git checkout --`, confirmed empty `git diff --stat`).
`scripts/tpcds-sf025-regression.sh sweep` then `plans`, private
`GOOPG_BIN=tmp/goopg-s2b2-bin` (shared `tmp/goopg-bench-bin` is in active use
by the nightly lane). Both runs: `PASS=96 MISMATCH=0`, `PLAN-SHAPE:
changed=0` — the instrumentation is read-only and moved nothing, as
expected. **Finding 1: the seam is real and reached constantly** — 38
distinct `createOrderedPaths` calls across the corpus reach a non-nil
`searchedRelOf(input)`, with `Pathlist` sizes from 3 to 16 (median ~7) — i.e.
`addOrderedPaths` is discarding a median of ~6 real alternative candidates
per call, corpus-wide, exactly as the task's framing predicted. **Finding 2,
decisive:** parsed all 38 blocks programmatically (script run inline, not
committed) and checked two invariants PG's cost model does NOT share but
goopg's currently does: (a) **every candidate in a given call's `Pathlist`
carries the SAME `Rows`** (36/38 blocks exactly; the remaining 2 differ by
1 row, traced to a `kind=11` — LIMIT-bearing — candidate's own rounding, not
a genuine cardinality split) — candidates of one rel are alternate physical
strategies for the SAME logical relation, so `costSortRun`'s only inputs
(`rows`, `width`, both rel-level) are identical across every candidate; and
(b) **the candidate `sr.CheapestTotal` already points at is, in all 38
blocks with no exception, the exact minimum-cost entry in `Pathlist`** (the
search's own `setCheapest` already found it), and that candidate's cost
tracks the seed's `legacyDisplayCostOf` value used today (small numeric
drift expected — two different cost derivations of the same materialized
Node, not two different candidates).

**Why (a)+(b) together mean the surgery cannot move a plan today.** Since
every candidate of a rel prices its Sort from the identical `(rows, width)`
pair, and goopg's sort cost function (`costSortRun`) has no term that varies
by a candidate's OWN `Pathkeys` — no incremental-sort prefix credit exists
yet (M0141-S7 is unimplemented, confirmed `[ ]` and gated on S1-S6) — the
Sort cost added on top is a CONSTANT across every candidate in the list.
Adding an identical constant to every candidate's cost cannot change their
relative order: whichever candidate was cheapest-total BEFORE the Sort
(`sr.CheapestTotal`, confirmed (b) above) remains cheapest-total AFTER it.
That candidate is exactly the one `createOrderedPaths` already uses as its
single seed today. **Opening the full `Pathlist` to the ORDERED tournament,
on its own, is therefore provably a zero-plan-movement change on this
corpus** — the same "moves nothing" verdict R21 Slice 1 predicted and
measured for its own plumbing-only cut
(`docs/design/not_ralph/plan_parity_fix_take2/r21-upper-planner-seam/DESIGN.md`
§5), not a defect in this recon's method.

**What would actually make the surgery pay off: per-candidate Pathkeys
credit in the sort cost, i.e. Incremental Sort (M0141-S7).** PG's
`cost_incremental_sort` (`costsize.c`) prices a sort whose input already
satisfies a PREFIX of the required key list cheaper than a full
`cost_sort` — a genuinely per-candidate term, since different candidates in
the same `Pathlist` carry different `Pathkeys` prefixes (observed directly
in the trace: within one 16-candidate block, `pathkeys` values of 0 and 1
coexist at wildly different base costs). Only once that asymmetry exists
does "which candidate is cheapest AFTER an appropriately-priced Sort" become
a real question distinct from "which candidate is cheapest before any Sort"
— which is the whole reason PG's `create_ordered_paths` bothers to loop the
full pathlist instead of picking `cheapest_total` and sorting it. **This
re-orders M0141's own remaining-slices sequencing**: S2b-2's Pathlist-surgery
plumbing and S7's Incremental Sort are not independent, sequential slices —
S2b-2 is dead weight without S7, symmetrically to how S7's own prefix-match
arm (already noted in S2b's census above) needs a real multi-candidate
Pathlist to have anything to choose among. Building either alone, in
isolation, reproduces the "byte-identical plans" non-result this recon just
demonstrated for S2b-2.

**Decomposition for whichever loop picks this back up** (mirrors R21's own
slice discipline):

1. **S2b-2a (plumbing, no behavior change)** — thread `searchedRelOf(input)`
   into `createOrderedPaths`/`addOrderedPaths` so the full `Pathlist` is
   *visible* at the call site (today it is reachable via the accessor but
   nothing in this file calls it). Gate: byte-identical plans on both
   corpora, matching R21 Slice 1's own gate — per Finding 2 above, this is
   now a *predicted*, not merely hoped-for, null result.
2. **S2b-2b (materialize-on-demand, no behavior change)** — for each
   candidate beyond the current seed, defer `createPlanNode` until
   `setCheapest` has already chosen a winner (mirrors how the existing code
   defers materialization to the very end today), so a corpus-wide surgery
   does not pay a `createPlanNode` cost for N-1 discarded candidates per
   query. Needs the same `validatedSearchPathkeys`
   (`upperorderedinput.go`) coordinate-boundary check generalized to run
   per-candidate rather than once for the single seed — every candidate's
   `Pathkeys` are in the SEARCH's inner coordinate space and must be
   validated against the schema the boundary publishes before being trusted,
   the same rule the file's header already states for the one seed it
   handles today.
3. **S2b-2c (the actual payoff, blocked on M0141-S7)** — once
   `cost_incremental_sort`'s per-candidate prefix credit exists, 2a+2b's
   plumbing is what lets `addOrderedPaths` run a REAL tournament across
   `Pathlist` instead of the single always-cheapest-pre-Sort seed. Do not
   attempt before S7 lands; this recon's Finding 2 is the proof that doing
   so earlier cannot move a plan.

No ledger row needed for new unimplemented scope: the gap (Incremental Sort
as S2b-2's real prerequisite) is already M0141-S7, filed and unchecked; this
recon corrects the SEQUENCING between two already-filed items, it does not
discover a new one.

## S2b-2a landed (2026-09-17)

Implemented decomposition item 1 above: `createOrderedPaths`
(`internal/optimizer/upperordered.go`) now calls `searchedRelOf(input)` right
where `seed` is built and, when it resolves, stores the search rel's own
`Pathlist` onto a new field, `RelOptInfo.SearchCandidates`
(`internal/optimizer/path.go`) — following the same "travels as DATA on the
rel" precedent `NeededCols`/`OutputCols` already use for a value one future
consumer reads and nothing reads yet. `addOrderedPaths` needed no signature
change: it already receives `ordered *RelOptInfo` as its first parameter, so
the field alone makes the list reachable at its call site without a new
parameter threaded through 3 production call sites (`upperordered.go`,
`upperordereddistinct.go`, `upperorderedgrouping.go`) and 6 test call sites —
narrower than the literal "thread into `addOrderedPaths`" reading, and lower
risk, since `addOrderedPaths`'s body is intentionally untouched this slice.

Two new tests in `upperordered_test.go` pin the gate: a searched-root input
(a new `searchedPricedNode` test fixture — `pricedNode` plus the
`searchedTree` tag) gets its search rel's 2-entry `Pathlist` copied onto
`ordered.SearchCandidates` by identity, while `ordered.Pathlist` itself stays
at exactly 1 entry (the seed) — the *Sort* still comes back, matching a
non-searched input. A non-searched input leaves the field nil. `go test
./internal/optimizer/...` full package PASS. TPC-DS SF0.25 sweep (private
bin, nightly batch was live): `PASS=96 MISMATCH=0`, `PLAN-SHAPE: changed=0` —
byte-identical, confirming Finding 2's prediction. **S2b-2a is DONE.**

**Next in the decomposition**: S2b-2b (materialize-on-demand — defer
`createPlanNode` on any non-seed candidate until `setCheapest` picks a
winner, and generalize `validatedSearchPathkeys` to run per-candidate) can
now read `ordered.SearchCandidates` directly instead of re-deriving
`searchedRelOf`. S2b-2c (the actual tournament) stays blocked on M0141-S7's
`addOrderedPaths` third arm.

## S2b-2b landed (2026-09-17) — pathkeys half only; the materialize-on-demand half has nothing to defer yet

Implemented the buildable half of decomposition item 2: generalized
`validatedSearchPathkeys` (upperorderedinput.go, rule 1 — re-earn a path's
ordering claim against the schema the boundary actually publishes, since it
was resolved in the search's own inner coordinate space) from running once
for the single WINNING path (`stampSearchPathkeys`'s own call, unchanged) to
running once per entry of `RelOptInfo.SearchCandidates` — the new function
`validatedSearchCandidateKeys` (upperorderedinput.go) maps it over the
candidate list and returns a parallel-indexed `[][]PathKey`, stored on a new
field `RelOptInfo.SearchCandidateKeys` (path.go), populated by
`createOrderedPaths` (upperordered.go) in the same `sr != nil` branch S2b-2a
added. Same "travels as DATA, nothing reads it yet" posture as
`SearchCandidates` itself — no new call site downstream.

**Why the materialize-on-demand half is deferred, not skipped.** Re-reading
S2b-2b's own filed text: "defer `createPlanNode` on any non-seed candidate
until `setCheapest` has chosen a winner." That describes a *cost* to avoid —
paying `createPlanNode` for the N-1 candidates a real tournament would build
and then discard. But nothing in production builds a `Node` for any
`SearchCandidates` entry today (S2b-2c, which is what would ever call
`createPlanNode` on one of them, is still blocked on M0141-S7's
`addOrderedPaths` third arm) — there is no eager path to defer yet. Sizing
this half now would mean writing dead code against an interface S2b-2c
hasn't defined (what a materialized incremental-sort candidate even looks
like — `PathIncrementalSort`, per S7's own implementation-order list, does
not exist). The design constraint itself is recorded here instead, as the
contract S2b-2c must follow when it starts building real candidates from
`SearchCandidates`/`SearchCandidateKeys`: materialize lazily, after
`setCheapest`, never eagerly per candidate.

**Gate**: same as S2b-2a — a new test,
`TestCreateOrderedPathsValidatesSearchCandidatePathkeys`
(upperordered_test.go), pins three cases in one search rel's Pathlist: a
candidate whose one key fully validates, a candidate whose first key
validates and second key (bad column index) truncates the claim rather than
invalidating it entirely (file header rule 1's own contract), and a
candidate with no Pathkeys at all (nil in, nil out). `ordered.Pathlist`
stays at exactly 1 (the seed) in the same test — still plumbing only. `go
test ./internal/optimizer/...` full package PASS. TPC-DS SF0.25 sweep
(private bin `tmp/goopg-s2b2b-bin`, nightly batch was live and holds
`tmp/goopg-bench-bin`; binary deleted after the run): `PASS=96 MISMATCH=0`,
`PLAN-SHAPE: changed=0` — byte-identical, as predicted. **S2b-2b's pathkeys
half is DONE; its materialize-on-demand half folds into S2b-2c** (there is
no standalone artifact to land for it before S2b-2c exists to consume it).

**Next in the decomposition**: S2b-2c (the actual tournament — build a
`PathIncrementalSort` candidate over each `SearchCandidates` entry using its
`SearchCandidateKeys` prefix and M0141-S7's `costIncrementalSort`, offer it
to `addOrderedPaths`'s new third arm, materializing lazily per this update's
contract) is still **blocked on M0141-S7**: `costIncrementalSort` and
`pathkeysCountContainedIn` are landed groundwork with zero production
callers, but the executor has no Incremental Sort operator, so a candidate
that actually won the ORDERED tournament today would reach `createPlanNode`
with no node kind to emit. S7's own implementation-order list (fix_plan.md)
sequences the executor operator AFTER the `addOrderedPaths` third arm — that
ordering needs re-checking before S2b-2c starts: offering an
executor-unbacked path kind to a real tournament, where it could actually
win, is a materially different risk than the groundwork-only steps landed
so far.

## S2b-2c landed (2026-09-17c) — the third arm, gated off by default

Resolved the risk the prior update flagged (offering an executor-unbacked
path kind to a real tournament) by following the SAME off-by-default env-var
convention every other experimental path family in `internal/optimizer`
already uses (`GOOPG_PARTIAL_SORT_PATHS`, `GOOPG_PARTIAL_AGG_PATHS`, …)
rather than reordering S7's own stated implementation sequence
(`addOrderedPaths` third arm before the executor operator). New file
`internal/optimizer/incrementalsortpaths.go`:

- **`PathIncrementalSort`** (`path.go`), a new `PathKind`. It has
  deliberately no `createPlanNode` arm — `createplan.go`'s own header states
  the philosophy this follows: "the real path-kind arms... panic loudly
  rather than silently mis-build, because a constructed-but-unhandled kind
  would be a bug in the phase that adds it." Reaching `createPlanNode` with
  this kind therefore panics via the existing `default` arm — by design, not
  as an oversight — and `GOOPG_INCREMENTAL_SORT`'s off default keeps that
  panic unreachable in production until the executor operator lands.
- **`addIncrementalSortPaths`**, called from `addOrderedPaths`'s tail
  (`upperordered.go`) right after arm 2's full Sort: for each
  `ordered.SearchCandidates[i]` whose `ordered.SearchCandidateKeys[i]`
  shares a genuine PARTIAL prefix with `sortPathkeys`
  (`pathkeysCountContainedIn`, 0 < nCommon < len(required) — a full match is
  arm 1's territory for the seed only, a zero-length match is arm 2's), it
  builds a `PathIncrementalSort` over that candidate priced by
  `costIncrementalSort(cp, candidate.Cost, candidate.Rows, groups, ncols,
  avgVarBytes, limitTuples, width)` and offers it to `addPath`.
- **`groups` (the prefix's `estimate_num_groups`)** is computed via
  `estimateNumGroups(sortPathkeys[:nCommon]'s Exprs, input.node,
  int64(candidate.Rows))` — reusing the SEED's own materialized Node as the
  `child` statistics source for a DIFFERENT candidate's prefix. This is sound
  specifically because of S2b-2's own Finding 2 (§"S2b-2 result" above):
  every entry in one call's `Pathlist` is an alternate PHYSICAL strategy for
  the SAME logical relids, sharing the same base tables/columns/statistics —
  the seed's Node is a faithful stats source for a sibling's prefix, not an
  approximation. (`estimateNumGroups`'s `child`-typed-switch column resolver
  is also nil-safe for a Node it does not recognise, degrading to
  `defaultNumDistinct` rather than panicking — verified by reading
  `resolveBaseColumn`/`groupUniqueNDistinct`, joinkeyproof.go/cardinality.go
  — so an opaque test fixture's `.node` is safe to pass too.)
- **Materialization stays lazy**, satisfying S2b-2b's own contract: building
  a `*Path` here allocates no executor Node (`Children: []*Path{candidate}`
  only), so offering N-1 losing candidates costs nothing beyond the struct
  itself, and `createPlanNode` still runs exactly once, on whichever path
  `setCheapest` picks.
- **GUC note**: PG prices this under its own `enable_incremental_sort` GUC
  (declared in `catalog.go`/`defaults.go`, default on, but not read by
  `costParams` — the familiar "declared but unconsumed" shape). This arm
  reuses `cp.enableSort` instead of wiring a second still-unconsumed flag;
  deferred to the ledger (`m0141-s7-incremental-sort-guc`) since it is
  orthogonal to whether the arm exists and the mode gate already keeps
  production inert.

**Verified, not merely argued, that the fail-loud panic is real**: an
initial test that ran the new arm through `createOrderedPaths` end-to-end
with the flag on hit `panic("createPlan: path kind 20 not yet translatable
(P5.5)")` exactly where the design predicted, because the synthetic
candidate genuinely won the tournament. The test suite was restructured to
call `addOrderedPaths` directly (mirroring the existing
`TestAddOrderedPathsOffersExactlyOneProducerPerInput`) so the ADDED arm is
exercised without materializing a winner the executor cannot yet emit — the
same boundary S2b-2b already drew around "never call `createPlanNode` on a
non-seed candidate before it wins."

**A genuine dominance result, not just a plumbing check**: with a realistic
seed cost stamped (`legacyDisplayCostOf`, matching what `createOrderedPaths`
does in production before calling `addOrderedPaths` — the first version of
the positive test skipped this and got a false negative, since
`newPrebuiltPath`'s zero-cost seed made the untouched full Sort look
artificially free), the Incremental Sort candidate's prefix credit correctly
PRUNES the dominated full-Sort candidate via `addPath`'s ordinary fuzzy-cost
comparison — both share the identical `Pathkeys` (the full requirement; an
Incremental Sort still delivers everything, only its cost differs), so a
strictly cheaper one legitimately evicts the other. That is the entire
reason this arm exists.

Four new tests (`incrementalsortpaths_test.go`): default-off inertness
(end-to-end through `createOrderedPaths`, safe because the flag being off
means the new kind can never be constructed), the positive dominance case
above, a fully-contained-candidate skip, and a zero-shared-prefix skip.
`GOOPG_INCREMENTAL_SORT` registered in `flaglabels.go`
(`flagResolvedState`/`flagProvenanceOrder`) and `scripts/planner-flags.env`
regenerated (`go run ./cmd/gen-planner-flag-labels`). `go build ./...`,
`go vet ./internal/optimizer/...`, and `go test ./internal/optimizer/...`
(full package, including `TestFlagProvenanceEnvIsGenerated`) all clean.
TPC-DS SF0.25 sweep at the default (flag off, private bin
`tmp/goopg-s2b2c-bin`, nightly batch was live and holds
`tmp/goopg-bench-bin`; binary deleted after the run): `PASS=96 MISMATCH=0`,
`PLAN-SHAPE: changed=0` — byte-identical, confirming production is
untouched. **S2b-2c is DONE for its filed scope** (the third arm exists,
gated). The still-open resume points — flip `GOOPG_INCREMENTAL_SORT` on for
real measurement, build the executor operator, `createplansimple.go`
wiring, `EXPLAIN` rendering, and re-run S7's 14-query TPC-DS census — belong
to M0141-S7's own remaining implementation-order steps, not to S2b-2c.

## S2b-3 recon (2026-09-17j) — unblocked by S2b-2c/M0141-S7, but needs its OWN decomposition

**Why this loop ran a recon instead of code**: S2b-3 ("WINDOW loop-fix") was
filed gated on "S2b-2's actual payoff", later narrowed to "gated on M0141-S2b-2c
specifically (blocked on M0141-S7)". Both S2b-2c and M0141-S7's executor
operator (`M0141-S7-exec-a/b/c`) are now landed, so the stated gate is clear —
but reading `windowsetoppaths.go`'s own header before touching it surfaced
that its "NEITHER HALF HAS A CHOICE TO OFFER... C-14 Incremental Sort is
BLOCKED with no executor counterpart" claim is now STALE (superseded by a
LATER change, R6/plan-parity-fix-take2, which the header was never updated to
reflect) and that the real remaining gap is two-piece, matching K24/S2b-2's
own "do not attempt in one sitting" precedent. Recorded here rather than
starting an under-scoped edit.

**What already exists (R6, `createplansimple.go:294` `createWindowPlan`,
`operators_window.go` `windowOp.Open`)**: a real `*WindowAgg.Presorted` field
and a real executor fast-path (`if !o.plan.Presorted && (...) { sort
internally }`) already let a `*WindowAgg` skip its own internal sort when the
plan says the input already arrives in `PARTITION BY ++ ORDER BY` order. The
one caller that sets it today, `createWindowPlan`, only recognizes two child
shapes via `childDeliversSortKeys` (`createplansimple.go:357`): a `*Sort` node
it just stacked itself, or a lower `*WindowAgg` chained on the identical keys
(the "two window specs share one ordering, sort once" case). **A raw
join/scan `Node` — even one genuinely ordered by an index or a merge join —
is never recognized**, because `createWindowPaths`
(`windowsetoppaths.go:91`) never sees more than the single collapsed `input`
Node; there is no join/scan Pathlist visible at this call site at all. This
is the literal TPC-DS Q67 gap (`WindowAgg` partitioned on `dw1.i_category`;
PG's real plan feeds it a `Presorted Key: i_category`-tagged Incremental Sort
right below the WindowAgg — see `m0141-s7-readjudicate-and-scope-incremental-sort.md`
line 67).

**Two independent pieces, confirmed by reading (not inferred), same
"prerequisite is inert alone" shape S2b-2/M0141-S7 already showed once**:

1. **`costWindow` (`windowsetoppaths.go:193`) has NO presorted-aware branch.**
   It unconditionally adds `costSortRunWithWidth(...)`'s full cost to every
   window chain regardless of whether the input already satisfies
   `windowSortKeys` in full or in part. So even after piece 2 below makes a
   second, already-ordered candidate *visible* to `addWindowPaths`, that
   candidate would still be costed as if it needed a full fresh sort — it
   could only win by having a cheaper *input* cost, never by having its sort
   elided, which is the actual PG behavior Q67 needs. This is the same
   "seam reached but inert" finding S2b-2's recon made for the base
   join/scan boundary, transplanted to WINDOW: **piece 2 without piece 1
   changes nothing measurable.**
2. **`createWindowPaths`/`addWindowPaths` see exactly one input `Node`, never
   a Pathlist.** Wiring in `searchedRelOf(input)` and populating
   `winRel.SearchCandidates`/`SearchCandidateKeys` is the same, already-proven
   mechanism S2b-2a/2b built (`RelOptInfo.SearchCandidates`,
   `validatedSearchCandidateKeys`, both reusable verbatim — `SortKey`→`PathKey`
   conversion is also solved already, `pathkeysForSortKeys`,
   `pathkeys.go:103`). What is NOT reusable verbatim is
   `addIncrementalSortPaths` itself (`incrementalsortpaths.go:147`): it adds a
   bare `PathIncrementalSort` directly onto the target rel's own Pathlist,
   which is correct for `upper.ordered`/`electOrderedGrouping` (there, the
   sort's result outranks the aggregate — sort-above-agg is the rel's own
   final output) but WRONG for WINDOW (the sort must nest BELOW the
   `*WindowAgg`, which must remain the rel's outer/final node no matter which
   input candidate wins). `addWindowPaths`'s per-candidate loop needs its
   own prefix-matching arm — same `pathkeysCountContainedIn` test
   `addIncrementalSortPaths` already uses, but building `PathWindow{Children:
   [PathIncrementalSort{Children: [candidate]}]}` (or `Presorted`-style
   full-match with no sort node at all) instead of a bare
   `PathIncrementalSort`.

**Decomposition** (mirrors S2b-2a/2b/2c's own split; filed as fix_plan
sub-tasks, none selected yet):

- **S2b-3a** — teach `costWindow` a presorted/partial-prefix credit, reusing
  `costIncrementalSort`'s formula (`incrementalsortpaths.go`,
  `costsize.c:1898-1985`-derived per M0141-S2b-6's own hand-verification) for
  the shared-prefix case, and skip `sortRun` entirely when the full key list
  is already covered. Gate: same "inert alone" byte-identical-plan predicted
  null result S2b-2a/M0139-0007a's own precedent set, since nothing calls it
  with a presorted input yet.
- **S2b-3b** — wire `searchedRelOf`/`SearchCandidates`/`SearchCandidateKeys`
  into `createWindowPaths`, and extend `addWindowPaths`'s per-candidate loop
  to build a `PathWindow` over `PathIncrementalSort`-over-`candidate` (partial
  prefix) or a `Presorted=true` `PathWindow` directly over `candidate` (full
  match) for every search candidate whose pathkeys share a nonzero prefix
  with `windowSortKeys(top)`, alongside the existing always-full-Sort
  candidate. Depends on S2b-3a landing first (same ordering S2b-2c depended
  on M0141-S7's cost function). TPC-DS Q67 is the sole corpus witness and the
  gate.

No production code changed this loop (recon only, same precedent as
S2b-0/S2b-2/S2b-6). Ledger row appended (task-id `m0141-s2b-3`).
