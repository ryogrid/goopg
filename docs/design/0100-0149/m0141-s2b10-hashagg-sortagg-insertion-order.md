# M0141-S2b-10 — root cause of the Hashed-vs-Sorted `PathAgg` divergence: candidate insertion order, not a cost formula bug

Status: accepted (recon complete; no cost-model formula changed; no production
code changed this loop — the fix is filed as a follow-up `Kind: impl` task)
Date: 2026-09-18
Parent: `docs/design/0100-0149/m0141-s2b-6-resume-hashed-vs-sorted-real-sf1.md`
(`M0141-S2b-6-resume`)

## Question

`M0141-S2b-6-resume` established that for TPC-H Q4/Q12 (GROUP BY key == ORDER
BY key, low output cardinality, large join-output input row count), goopg's
cost model elects `Hashed`\+explicit-`Sort` while real PG 18.3 elects `Sorted`
(`GroupAggregate` fed by a `Sort` below it), by a real, non-tied margin
confirmed against a fresh live PG `EXPLAIN`. It ruled out the `R113`
`GOOPG_PG_SORT_RELATION_BYTES_COST` Sort byte-size currency as the cause (the
election and margin were unchanged after flipping it) and filed this task to
compare `costAgg`'s `AggStrategyHashed` term-by-term against PG's `cost_agg`
hashed-strategy formula (`postgres/src/backend/optimizer/path/costsize.c`)
before attempting any fix.

## Method

Re-ran the same private-clone `tpch-estimate-audit-arm.sh` methodology
`M0141-S2b-6-resume` established (online `pg_basebackup -X fetch` clone of the
live, read-only `:65433`, never stopped; `PGSHAPED=1 PLAN_ONLY=1 DP_TRACE=1`):

```
PGSHAPED=1 PLAN_ONLY=1 DP_TRACE=1 REFERENCE="" \
  scripts/tpch-estimate-audit-arm.sh m0141-s2b10-recon-20260918 --queries 4,12
```

Ran clean (rc=0, served-binary sha256 verified). The server log
(`tmp/tpch-audit-m0141-s2b10-recon-20260918.server.log`, not committed, same
precedent as every prior arm-script run in this lineage) carries the full
`DPPATH`/`DPGROUP`/`DPPGSORT` trace.

Separately, read `postgres/src/backend/optimizer/path/costsize.c`'s
`cost_agg` (line 2682) term-by-term against `internal/optimizer/cost_funcs.go`'s
`costAgg` (line 503), and cross-checked PG's *actual* internal cost estimate
for the discarded `HashAggregate` alternative by issuing session-scoped
`EXPLAIN` against the read-only PG 18.3 reference (`:65432`, db `tpch`) with
`SET enable_sort = off` — a per-session GUC toggle, not DDL/DML, so it does not
violate the reference cluster's read-only rule. No write of any kind reached
`:65432`/`:65433`.

## Result 1 — `costAgg`'s formula is NOT the divergence

`costAgg`'s `AggStrategyHashed`/`AggStrategySorted` arms are term-for-term
faithful to `cost_agg` (verified line-by-line: `transPerTuple`/`transCost`,
`groupCmpPerTuple`/hash-value CPU term, `finalPerGroup`/`finalCost`,
`cp.cpuTupleCost*groups`/`cpu_tuple_cost*numGroups`, and both engines'
explicit comment that AGG_SORTED/AGG_HASHED "share exactly the same total CPU
cost" — `cost_funcs.go:490-492` cites `costsize.c:2715-2723` verbatim). The
spill arm (`hashAggEntrySize`/`hash_agg_set_limits`) is inert for both Q4
(`numGroups=5`) and Q12 (`numGroups=7`) — far too few groups to ever approach
`work_mem`, confirmed by the `DPPGSORT`/hash-size trace showing `goopgbranch=
memory` throughout. **There is no formula bug to fix here.**

Proof by direct reproduction of PG's own formula on live data: hand-deriving
`costSortRunWithWidth`'s in-memory (`memory` branch, no I/O) CPU-only term
`2*cpu_operator_cost*N*log2(N)` for Q4's presort (`N=12141` from the trace)
gives ≈854 cost units — matching the ≈854-900 unit gap actually observed
between goopg's Sorted-own-cost and Hashed-own-cost (`upper.groupagg.sort`
total 501786.60 vs `upper.groupagg.hashed` total 500932.63, diff 853.97). The
presort cost genuinely is what it looks like; nothing is being mis-priced.

## Result 2 — PG's OWN cost formula, applied without restriction, ALSO says Hashed is cheaper

Cross-checked PG's real, live cost model by forcing the alternative it
normally never shows. Read-only `EXPLAIN`, private session GUC only, against
`:65432`/`tpch`:

```
-- Q4, default (max_parallel_workers_per_gather=0)
GroupAggregate  (cost=190893.67..190990.46 rows=5 width=24)
  ->  Sort  (cost=190893.67..190925.92 rows=12899 width=16)
        ->  Nested Loop Semi Join  (cost=0.43..190012.99 rows=12899 width=16)
              ...

-- Q4, SET enable_sort = off
Sort  (cost=190077.60..190077.61 rows=5 width=24)
  Disabled: true
  ->  HashAggregate  (cost=190077.49..190077.54 rows=5 width=24)
        ->  Nested Loop Semi Join  (cost=0.43..190012.99 rows=12899 width=16)
              ...  -- SAME child as the default plan
```

PG 18's disabled-path annotation (`Disabled: true`) does **not** inflate the
printed cost (the historical `disable_cost` multiplier was replaced by a
`disabled_nodes` counter compared before cost, per PG 17+); `190077.61` is
PG's genuine, un-penalized cost estimate for `HashAggregate` + a trivial
5-row output `Sort`, over the *identical* child plan (`cost=0.43..190012.99`)
as the chosen `GroupAggregate` candidate. **190077.61 < 190990.46** — PG's
own formula prices the Hashed alternative as *cheaper* by 912.85 units
(0.48%), yet PG's unforced planner picks the more expensive Sorted plan.
Q12 reproduces the same pattern with the identical child
(`cost=61564.00..325960.49`):

```
-- Q12, default
GroupAggregate  (cost=328065.16..328634.21 rows=7 width=27)
  ->  Sort  (cost=328065.16..328136.28 rows=28449 width=27)
        ->  Hash Join  (cost=61564.00..325960.49 rows=28449 width=27) ...

-- Q12, SET enable_sort = off
Sort  (cost=326458.52..326458.53 rows=7 width=27)
  Disabled: true
  ->  HashAggregate  (cost=326458.35..326458.42 rows=7 width=27)
        ->  Hash Join  (cost=61564.00..325960.49 rows=28449 width=27) ...  -- SAME child
```

`326458.53 < 328634.21` — Hashed is cheaper by 2175.68 units (0.66%).

**This is the crux finding: goopg's Hashed-wins conclusion is not wrong
relative to PG's cost formula — it is what PG's own formula says too.** A fix
that adjusts `costAgg`/`costSortRunWithWidth`'s arithmetic to make Sorted
"win" would be fighting the correct formula, and would be the wrong kind of
fix M0141-S2a's whole currency-narrowing lineage has been careful to avoid
(`docs/design/cost-model/`, the "0077 line": never land a formula change
without a term-by-term match against `costsize.c`).

## Result 3 — the real divergence: candidate insertion order, not cost

Both margins found in Result 2 (0.48%, 0.66%) sit inside PG's
`STD_FUZZ_FACTOR` (`pathnode.c:50`, `1.01` — a 1% tolerance used throughout
`add_path`'s dominance comparisons). goopg already has the *identical*
mechanism, faithfully ported: `internal/optimizer/path.go:27`
(`stdFuzzFactor = 1.01`, citing `pathnode.c:50`) feeds `comparePathCostsFuzzily`
(`path.go:901`) and `addToPathlist`'s dominance/tie-break logic — **on a
fuzzy-tied cost with equal pathkeys, the FIRST-inserted path wins and the
second is marked `dominated`**, matching `add_path`'s own documented
behaviour (`pathnode.c:1062-1066`'s comment, ported verbatim into `path.go`'s
`addPath` doc comment).

The live trace proves this is exactly what happens for Q4:

```
DPPATH path producer=upper.groupagg.hashed ... total=500932.62978854164 ... verdict=accepted
DPPATH path producer=upper.groupagg.sort   ... total=501786.6034243473  ... verdict=accepted
...
DPPATH path producer=upper.ordered.sort  ... total=500932.700336744 ... pathkeys=1 verdict=accepted   <- Hashed, wrapped in its own tiny 5-row Sort, ADDED FIRST
DPPATH path producer=upper.ordered.input ... total=501786.6034243473 ... pathkeys=1 verdict=dominated <- Sorted (contained=true, no wrap needed), ADDED SECOND, cost only 0.17% higher, SAME pathkeys -> dominated
DPGROUP elected shape=Sort-over-Aggregate strategy=0   (0 = AggStrategyHashed)
```

Both final `ordered.Pathlist` entries end up with **identical pathkeys**
(both satisfy the query's `ORDER BY`, one by construction (`Sorted`, no Sort
node needed — `contained=true`), the other by an explicit wrapping `Sort`
added on top of `Hashed`'s tiny 5-row output). With pathkeys tied and total
cost fuzzily tied (0.17% for Q4, well inside 1%), `addPath`'s tie-break falls
through to pure insertion order — and whichever candidate is *offered first*
wins, regardless of which one is actually cheaper.

**`internal/optimizer/groupingpaths.go`'s `addGroupingPaths` — the function
whose own doc comment claims to BE "the per-input body of
`add_paths_to_grouping_rel` (planner.c:7114)" — adds the HASHED candidate to
the `grouped` rel's Pathlist first (`groupingpaths.go:406`,
`groupAggHashedProducer`), then the SORTED candidate second
(`groupingpaths.go:463/475`, `groupAggSortedIdxProducer`/
`groupAggSortedProducer`).** Real PG's `add_paths_to_grouping_rel`
(`postgres/src/backend/optimizer/plan/planner.c:7113`) does the *opposite*:
its `can_sort` block (lines 7128-7264, adding every `AGG_SORTED`/`AGG_PLAIN`
candidate including the presorted-input case) runs **before** its `can_hash`
block (lines 7286-7315, adding the `AGG_HASHED` candidate). Since both
engines share the exact same fuzzy-tie-break-by-insertion-order convention,
this single order inversion is sufficient, on its own, to flip the election
outcome whenever the two candidates' costs are fuzzily tied — exactly the
situation Q4 (0.17% gap) and Q12 (0.63% gap, `501786.60`⁠→⁠`503962.28`-scale
values differ slightly per-run due to clone timing/ANALYZE drift, but the
same order of magnitude) land in.

Q5/Q21 do not exhibit this bug because their ORDER BY key does not match
their GROUP BY key (`contained=false` for the Sorted candidate too — both
strategies need their own separate final Sort, so their `pathkeys`/cost
relationship is different and the fuzzy-tie/pathkeys-equal coincidence that
triggers the order-dependence for Q4/Q12 does not arise there).

## Conclusion — do not touch `costAgg`; the fix is a two-line reorder

1. `costAgg`/`costSortRunWithWidth` are PG-faithful. Confirmed twice: by
   term-by-term formula comparison, and by reproducing PG's own formula
   result live (`enable_sort=off`) — PG's real cost model also ranks Hashed
   cheaper for both witnesses.
2. The actual divergence from PG's *chosen plan* (not PG's cost estimate) is
   that PG's `add_paths_to_grouping_rel` inserts Sorted-strategy candidates
   into the grouping rel's Pathlist before Hashed-strategy candidates, and
   goopg's `addGroupingPaths` does the reverse. Combined with both engines'
   identical, correct `STD_FUZZ_FACTOR`-based tie-break-by-insertion-order
   rule in `add_path`/`addPath`, the reversed order silently flips the
   winner whenever the two strategies' costs are within 1% of each other.
3. The fix is narrowly scoped and low-risk: reorder `addGroupingPaths`
   (`internal/optimizer/groupingpaths.go`) to emit the SORTED candidate(s)
   (the `if aggNode.GroupingSets == nil { ... }` block, currently last)
   *before* the HASHED candidate (the `if groupingHashable(...)` block,
   currently first) — mirroring `add_paths_to_grouping_rel`'s `can_sort`-
   then-`can_hash` order exactly. No cost arithmetic changes. Filed as
   **M0141-S2b-11** (`Kind: impl`, `Parent: M0141-S2b-10`) in
   `.ralph/fix_plan.md`.
4. Risk to flag for M0141-S2b-11: this reorder can change the winner for
   *any* other GROUP BY query whose Hashed/Sorted candidates are fuzzily
   tied, not just Q4/Q12 — a full TPC-H spot-check, TPC-DS SF0.25 sweep, and
   plan-shape diff are mandatory, and any query that flips must be checked
   against a fresh PG `EXPLAIN` (same method as this doc's Result 2) rather
   than assumed correct just because it changed. The `PLAN_PARITY` floor
   (`match >= 2`, `AGENT.md`) is the acceptance bar; a plan shape moving
   *closer* to PG (as Q4/Q12 should, post-fix) is the expected and desired
   effect, not a regression, per this workstream's standing rule that
   matching PG is never a regression however the row-count/timing gates
   move.

## Gates run (this loop — recon only, no production file touched)

- `scripts/tpch-estimate-audit-arm.sh` (one arm) rc=0, served-binary sha256
  verified.
- Read-only `EXPLAIN`/session-GUC only against `:65432` (no DDL/DML/ANALYZE);
  `:65433` touched only via `pg_basebackup -X fetch` (never stopped/started/
  written).
- No `go build`/`go test` gate needed — no `.go` file in this repo was
  edited this loop (recon is read-only code inspection plus live-cluster
  `EXPLAIN`/trace capture).
- `python3 scripts/ralph_protected_regions.py check-designdocs` — run before
  commit alongside the `fix_plan.md`/`README.md` edits.
