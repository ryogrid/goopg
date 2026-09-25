# M0142-0004b — recon: why a 3-way CTE `UNION ALL` lands at `est=3` against actuals up to 1557x higher

Status: accepted (recon closed 2026-09-15, no code change; fix filed as M0142-0011)

## Task

`.ralph/fix_plan.md`'s M0142-0004b line, filed by M0142-0004: Q33/Q56/Q60 each
`UNION ALL` three CTE branches (`ss`/`cs`/`ws`, each a filtered 4-way join
grouped by `i_manufact_id`) and the resulting Append/SetOp estimates exactly
`3` against actuals 405/1452/1557 (135x/484x/519x). The task's own resume
point: instrument `EstimateRows` on the standalone `ss`/`cs`/`ws` subplan
(before the union wraps it) and find which node in that subtree collapses to
1 first — do not guess the mechanism from the outside.

## Method — instrument before theorising

Started `bench/tpcds/runtime_goopg/data-sf025` via the standard lifecycle
(`bench/tpcds/server.sh start sf025`, port 65437 — the git-tracked SF0.25
cluster this milestone's other recons use), ran Q33's `ss` CTE body standalone
(own SQL, `select i_manufact_id, sum(ss_ext_sales_price) … group by
i_manufact_id`, the same 4-way join + `IN` subquery Q33 wraps in a CTE).
`EXPLAIN` on the standalone body reproduces the witness exactly:

```
HashAggregate                                    rows=1
  -> Hash Semi Join (i.i_manufact_id = i_manufact_id)  rows=1
        -> Gather                                       rows=99
              -> Nested Loop                             rows=32
                    -> Nested Loop                        rows=32
                          -> Parallel Hash Join            rows=91
                                store_sales x date_dim
                          -> Index Scan customer_address_pkey
                    -> Index Scan item_pkey
        -> Seq Scan on item_1 (i_category = 'Books')     rows=1733
```

The full `Q33` `EXPLAIN` confirms the same shape recurs identically for all
three branches (`cs`/`ws` each print their own `HashAggregate … rows=1` above
a `Hash Semi Join … rows=1`), and the outer `Limit rows=3` is exactly
`1+1+1` — the filed `est=3` symptom is the union's own arithmetic
(`estimateSetOp`) correctly summing three already-collapsed branch estimates,
confirming M0142-0004's own note that the defect is upstream of the set-op
function, in the branch estimate itself.

Added a temporary `GOOPG_EA0004B_TRACE=1`-gated `fmt.Fprintf(os.Stderr, …)`
in `estimateJoin`'s SEMI/ANTI branch and in `semiPairMatchFraction`
(`internal/optimizer/cardinality.go`), rebuilt via `bench/tpcds/server.sh`
(which rebuilds `cmd/goopg` from source on every start), and re-ran the
standalone `ss` body. Reverted after the recon — **this task lands no
production diff** (`git status`/`git diff` on `cardinality.go` empty after
revert).

## Finding: the SEMI join's own match-fraction math is correct; its OUTER input is already wrong before the semi-join runs

The trace shows `matchFrac=0.9979`, `resid=1` — i.e. `semiJoinMatchFraction`
(M0142-0006's own code) computes an almost-pass-through fraction, exactly as
expected given `nd1=nd2=994` (`i_manufact_id`'s ndistinct is close to fully
shared between the outer's `item` and the Books-filtered `item_1`). The SEMI
join is not the defect. But its outer input `l = EstimateRows(j.Left)`
resolves to **1**, not the ~32-99 the same subtree's `Nested Loop`/`Gather`
nodes show in the very same `EXPLAIN` output as their own printed `rows=`.

Extending the trace to walk `j.Left`'s structure confirms it is exactly the
4-relation join chain `Gather{NestedLoop{NestedLoop{ParallelHashJoin{store_
sales,date_dim}, IndexScan(customer_address)}, IndexScan(item)}}` — i.e. the
SAME subtree EXPLAIN renders with `rows=32`/`91`/`99` at each level. Two
different numbers for the same subtree, from two different call sites, is
exactly the `pattern_sibling_paths_must_agree` shape.

## Root cause, isolated to two candidate mechanisms (NOT disambiguated — that is the resume point)

**Mechanism A — `estimateJoin`'s measured-selectivity branch is gated on
physical join algorithm, not on whether an equi-key is resolvable.**
`internal/optimizer/cardinality.go`'s `estimateJoin` (~line 792) reads:

```go
if j.Algo == JoinAlgoHash || j.Algo == JoinAlgoMerge {
    // … pairNDistinct / MCV / superkey measured-selectivity path …
}
```

Every generic `*Join` node in the traced subtree is `Algo == JoinAlgoNested
Loop` (the plain nested-loop `*Join`, not the specialised
`*NestedLoopIndexJoin`, whose `Right` is a bound `*IndexScan`). Because the
gate excludes `JoinAlgoNestedLoop`, these nodes fall straight to the
"unmeasurable" fallback at ~line 811: `est := scaleByFloat(l*r,
defaultEqSelectivity)` (`0.005`), capped at `max(l, r)`. For the inner
`item_sk`-bound Nested Loop here, `l≈91, r=1` (the `IndexScan` returns ~1 row
per probe — correct for an equality-bound probe), so
`91*1*0.005 = 0.455` rounds/clamps down to **1** — even though the join is a
proven-key equality lookup that should pass through close to `l`. PG's own
`calc_joinrel_size_estimate`/`eqjoinsel` are algorithm-agnostic: row-count
sizing happens once on the joinrel, independent of which Path (NestLoop /
Hash / Merge) is cheapest (`postgres/src/backend/optimizer/path/costsize.c`).
Gating the measured branch on `Algo` has no PG analogue and looks like the
direct defect — but see Mechanism B below for why fixing it here specifically
is not yet safe to land blind.

**Mechanism B — `EstimateRows(*Join)` always recomputes from scratch via
`estimateJoin`, never consulting the node's own already-costed `PlanCost.
PlanRows`.** `internal/optimizer/plancost.go`'s `legacyDisplayCostOf` already
established the correct pattern elsewhere (`distinctpaths.go`,
`groupingpaths.go`, `partialaggpaths.go`, `partialsortpaths.go`,
`windowsetoppaths.go`): check `PlanCostInfo()`/`CostSet` first, and only
derive a fresh estimate when nothing is cached. `internal/executor/
operators_explain.go:3024-3028` is the renderer that actually prints a node's
`rows=` field, and it reads `PlanRows` from that same cached `PlanCost`
carrier — **not** `EstimateRows(n)`. That is why the inner `Nested Loop`
nodes print `rows=32` (their own correctly-costed `PlanCost.PlanRows`, set
once during DP-search costing) while `estimateJoin`'s SEMI/ANTI branch
computes a DIFFERENT, wrong `l=1` by calling raw `EstimateRows(j.Left)`,
which walks the subtree bottom-up via the switch in `EstimateRows` (case
`*Join: return estimateJoin(x)`) and hits Mechanism A's crude fallback fresh,
discarding the accurate cached value sitting right there in the same node.

**These are not necessarily the same fix.** Mechanism A's fix (broaden the
`Algo` gate) changes the SELECTIVITY FORMULA itself — it would also change
`EstimateRows`' output for every generic-Algo-NestedLoop `*Join` called during
DP search, before any `PlanCost` exists to consult, which is exactly the kind
of "narrowing/costing-order" territory the banner's `M0141-S2a`/`M0139-0007`
item was just about (does the search see a stale or an updated number when
this join is FIRST priced, not just at EXPLAIN-render time afterward).
Mechanism B's fix (prefer cached `PlanRows` when `CostSet`) is narrower — it
only changes `EstimateRows`' answer for ALREADY-costed subtrees (post-search,
at EXPLAIN-time and at any lookahead call like `semiJoinMatchFraction`'s that
happens to run after the child was priced) — but is a bigger *architectural*
change to `EstimateRows`' contract (every one of its ~15 call sites would
start reading a mutable, timing-dependent cache instead of a pure function of
node structure), which needs its own audit for correctness before landing.

Neither hypothesis is disambiguated by this task — doing so (and then
choosing, scoping, and floor-measuring the fix) is exactly a full slice on
its own, filed below as **M0142-0011**.

## Floor measurements (mandatory for M0137–M0143 recon tasks)

No production code changed — the `GOOPG_EA0004B_TRACE` instrumentation was
added and fully reverted in the same loop (`git status`/`git diff` on
`cardinality.go` empty; `go build ./internal/optimizer/...` clean post-revert).
The plan-parity/ea-ratchet floors are therefore unchanged from M0142-0010's
pin: TPC-H `match=8/22`, TPC-DS `match=2/99`, `make ea-ratchet` 112-entry
baseline (this task neither fixes nor regresses any finding in it — Q33/Q56/
Q60's CTE-branch findings remain open, now with two candidate mechanisms
instead of an unknown one). Throwaway server (`bench/tpcds/server.sh stop
sf025`) stopped before commit; the git-tracked SF0.25 cluster data directory
itself is untouched (read-only `EXPLAIN`s only, no DDL/DML against it).

## Follow-up

Filed as **M0142-0011** (fix_plan.md, under this same M0142 milestone):
disambiguate Mechanism A vs B (instrument which one actually fires for the
witness, and whether A is even reachable independent of B — B's absence could
be masking whether A alone would suffice), choose the narrower correct fix,
and run the full floor-measurement suite (TPC-H plan-parity, TPC-DS
plan-parity, `make ea-ratchet`, SF0.25 regression sweep) before landing,
matching the treatment M0142-0006/M0142-0009 got for their own `cardinality.go`
changes.
