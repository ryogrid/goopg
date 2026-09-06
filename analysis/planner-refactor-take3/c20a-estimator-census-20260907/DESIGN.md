# C-20a (P6-01) — one cardinality estimator: census, verdict, and the gate

**Verdict: the three deletions C-20a names are NOT available, and none of them
is blocked on remaining call sites.** Each is blocked on a different structural
fact, stated in §2. The item's own stated prerequisite — "EXPLAIN `rows=` from
the path (P0-02 remainder)" — is not merely incomplete: the carrier field
exists and is populated on every search-produced node, and EXPLAIN never reads
it (§3). That is the one piece of work that makes C-20a executable, and it is
small.

**Delivered instead of a deletion:** the gate the item cites and that has never
run (§5), wired to `make ea-ratchet`; a census with a falsifiable record of the
two estimators' agreement (§4, `internal/optimizer/cardinality_two_estimators_test.go`);
and a re-scoped C-20a row in TODO_ALL.

Item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md` C-20a.
Design cited: take3 `08-target-design.md` §9.1. Gate cited: take3
`09-verification-and-acceptance.md` §5 P6 — "PP + EA ratchet".

---

## 1. The consumer census

`git grep` over tracked `.go`, excluding tests, comment lines, and
`EstimateRows`' own 27 recursive arms inside `cardinality.go`:

| package | file | live call sites |
|---|---|---:|
| executor | `operators_explain.go` | 4 |
| executor | `operators_join_agg.go` | 1 |
| executor | `subq_cache.go` | 1 |
| optimizer | `planner.go` | 3 |
| optimizer | `joinkeyproof.go` | 3 |
| optimizer | `pushdown.go` | 2 |
| optimizer | `plancost.go` | 2 |
| optimizer | `nl_index_join.go` | 2 |
| optimizer | `joinsearch.go` | 2 |
| optimizer | `groupingpaths.go` | 2 |
| optimizer | `cte_stats_synthesis.go` | 2 |
| optimizer | `partialaggpaths.go` | 1 |
| optimizer | `nl_index_join_selectivity.go` | 1 |
| optimizer | `memoize.go` | 1 |
| optimizer | `distinctpaths.go` | 1 |
| **total** | **15 files** | **28** |

`estimateJoin` has exactly one caller: the `*Join` arm of `EstimateRows`.
`calcJoinrelSize` has exactly one production caller: `joinRelBuilder.sizeJoinRel`
(`joinrelsize.go:97`), inside the search.

The C-11…C-18 work did what the prerequisite says it did. `groupingpaths.go`,
`distinctpaths.go`, `partialaggpaths.go`, `windowsetoppaths.go` and
`upperrel.go` all now read row counts through `legacyDisplayCostOf(child).PlanRows`,
which prefers the **path's** number and falls back to `EstimateRows` only when
the node carries no `PlanCost`. Those five files are the C-11…C-18 consumers,
and they are converted. What the item did not anticipate is that they were
never the majority: three of the remaining call sites are in the **executor**,
where there is no `RelOptInfo` to consult and never will be.

## 2. Why each of the three deletions is unavailable

### 2.1 `EstimateRows` — a coordinate-space problem, not a call-site problem

`calcJoinrelSize` is a method on `searchCtx` over two `*RelOptInfo`. It is
reachable only from inside the join search, it answers about a *relation set*,
and it needs the joinrel's full restriction list. `EstimateRows` is a walker
over the plan `Node` tree; it answers about a *node*, and it is called with a
`Node` and nothing else.

The consumers outside the search have a Node and no RelOptInfo, and cannot
acquire one:

- `internal/executor/operators_explain.go` renders a plan the planner has
  already returned;
- `internal/executor/operators_join_agg.go:794` sizes the hash table when the
  plan came through the legacy rewriter and `plan.InnerRows`/`OuterRows` is
  zero — that IS the fallback arm;
- `internal/executor/subq_cache.go:106` budgets a correlated-subquery hash map
  at execution time.

"Everything reads `calcJoinrelSize`" is therefore not a migration that can be
performed. What can be performed is the substitution the item's prerequisite
names: get the path's number ONTO the node, and let every Node-level consumer
read that. §3 shows the carrier is already there.

### 2.2 `joinkeyproof.go` is not a mirror

The file's own header calls itself the surviving half of a pair whose graph-space
sibling was deleted at M0127-P6.3, which is presumably where "mirror" comes
from. But of its twelve functions, only `superkeyJoinEstimate` is
`estimateJoin`'s. The rest are load-bearing elsewhere:

| function | consumers outside `estimateJoin` |
|---|---|
| `resolveBaseColumn` | `cardinality.go` ×3 (incl. `estimateNumGroups`), `selectivity.go:762`, `extstats.go:456` |
| `groupUniqueNDistinct` | `cardinality.go` ×2 |
| `uniqueKeyColumnSets` | `planner.go` ×8 — every scan-construction site |
| `columnsSubset` | **`joinrelsize.go:601,613`** — C-05's own landed code |

Deleting the file would take out C-05's dependency and the base-column resolver
the whole selectivity substrate runs on. `resolveBaseColumn` is additionally the
subject of `TestResolverFamilyArmListsAgree`, which parses two type switches and
pins their arm lists against each other; that test exists because the missing
`*NestedLoopIndexJoin` arm (`9b43c67f3`) priced every column above an NLI at
`DEFAULT_NUM_DISTINCT` and produced TPC-DS aggregate estimates up to 8007×
wrong. Removing the subject without replacing the guarantee re-opens that class.

### 2.3 `estimateJoin`

One caller, so it looks like the cheap one. It is not separable: it is the
`*Join` arm of `EstimateRows`, and it goes when `EstimateRows` goes. Deleting
it alone means the `*Join` arm returns 0, which propagates as "no estimate" to
every node above it — the M0125-0038 collapse that made all 18 plans in the
M0125-0026 capture render `rows=1` on every non-leaf node.

## 3. The actual prerequisite, and how close it is

`PlanCost` (`plancost.go:28`) carries `PlanRows`, and `stampPlanCost` sets it
from `p.Rows` on **every** node the search produces — one funnel, so no path
kind can forget. The number is there.

`explainCostFields` (`internal/executor/operators_explain.go:2086`) reads
`StartupCost`, `TotalCost` and `PlanWidth` off that carrier — **and not
`PlanRows`.** All four EXPLAIN row sites (`:496`, `:1600`, `:1980`, `:1988`)
compute `rows=` as `optimizer.EstimateRows(rowSrc)` unconditionally, including
on nodes that carry a real path row count.

The consequence is worth stating plainly, because it silently invalidates the
provenance of every estimate artefact in the tree:

> On a search-produced plan, the planner **chooses** with `calcJoinrelSize`
> and EXPLAIN **reports** `estimateJoin`. `make plan-gate MODE=semantic-cost`,
> `cmd/estimate-audit`, the est-vs-actual tables in the c13a census
> (8007× on Q99, 245,587× on Q78) and the new EA ratchet all read the
> reported number. None of them has ever measured the estimator that picked
> the plan.

That single-line-per-site change is the "P0-02 remainder". It moves no plan —
it is a display path — but it moves every `rows=` in every capture, so it
requires a `plan_snapshots` re-pin and an `estimateaudit` fixture re-capture in
the same commit (take2 TODO P0-02's own hazard note, and 09 §7.1's
re-baselining rule). It is **not** in this item's file ownership
(`internal/executor/`), which is why it is filed as the successor rather than
done here.

## 4. What is landed instead: the agreement is now observed

`internal/optimizer/cardinality_two_estimators_test.go` builds one two-table
join in both coordinate spaces over the same `catalog.Table` values and the
same statistics, and asserts:

1. **control** — plain single non-key equality: both reduce to
   `outer × inner × 1/max(nd_l, nd_r)` and agree;
2. **superkey** — both columns of a composite unique key equated:
   `calcJoinrelSize` gives the outer's 6,000,000 (C-05's no-fan-out rule) and
   `estimateJoin` gives the same via `superkeyJoinEstimate`, through entirely
   separate code.

They agree today. Nothing in the tree established that before this test, and
the agreement is a coincidence of two independent implementations rather than a
guarantee. The test is written to fail loudly if either moves.

A fixture note worth keeping: the first run of test 2 reported a 2500×
divergence that was **not real**. `estimateJoin` has no catalog handle, so
`resolveBaseColumn` reads a relation's uniqueness evidence off the *node*
(`SeqScan.UniqueKeys`, populated at every scan-construction site in
`planner.go`). A fixture that omits it does not fail — it falls back silently to
the marginal product. That is the same "an unwinnable path is an untested path"
shape the repo has been bitten by before, in fixture form.

## 5. The gate: `make ea-ratchet`

Ledger `take3-ea-ratchet-never-ran` established that the "EA ratchet" cited by
C-05, C-10a, C-20a and C-21 has never executed. Re-verified here:
`scripts/tpch-estimate-audit-arm.sh` is named by no Makefile target, no hook, no
precommit script and no `ci/batch` stage, and its default pinned PG baseline
`analysis/leftdeep-joins/2026-08-05-p56giii-parity.pg.plans.txt` is absent, so a
default-flag run exits before measuring. (`estimate-audit -plan-only`, the
plan-capture step, does run and is fine. It is the est-vs-actual **parity
ratchet** that has not.)

Three further gaps made it unusable for the defects it is cited against even if
it had been wired: TPC-H corpus only (`cmd/estimate-audit/main.go:317-322`);
joinrel granularity, so a base relation estimating `rows=1` is not a candidate
even in principle (`audit.go:96-98`); and TPC-H has no `LIMIT`, so the
NLI+Memoize shape never arises.

### 5.1 What was built

| file | role |
|---|---|
| `scripts/estimate-parity/parity.py` | parser + scorer + ratchet |
| `scripts/estimate-parity/mkexplain.py` | query file → `EXPLAIN (ANALYZE, TIMING OFF)` script |
| `scripts/estimate-parity-gate.sh` | clone, capped server, ANALYZE, capture, score |
| `Makefile` | `ea-ratchet`, `ea-ratchet-repin`, and a `make help` line |

Design decisions, each against a specific failure the predecessor had:

- **TPC-DS SF0.5, `EXPLAIN ANALYZE`.** The truth is measured, not estimated.
  The c13a census established that an estimate-based reading of this corpus
  produces a different top-ten, a different ranking and a top entry 29× smaller
  than the measured one — the estimates are not a usable proxy for themselves.
- **Relation-set keying.** A node's key is the set of base relations in its
  subtree. It is the one coordinate two different planners agree on: it
  survives a different join order, a different join algorithm, an extra
  `Gather`, an extra materialisation. A singleton key is a base relation and a
  larger key is a joinrel, so **one** keying gives both granularities — the
  gap the joinrel-only predecessor could not close. CTE bodies and sub-plans
  are scoped, so a CTE reference does not unify with the query that fills it.
- **PG-relative bar.** A node is a finding when
  `qerr > max(floor, PG_qerr × tol)` — default `floor=10`, `tol=2`. This is
  the only bar that behaves correctly at both ends of this corpus:
  Q47/Q57/Q81/Q89 emit `rows=1` where PG 18.3 emits `rows=1` on the identical
  node and must PASS; Q99's 8007× over-estimate is not shared by PG and must
  FAIL. An absolute bar gets one of the two wrong whichever value it takes,
  and a gate that fails on correct behaviour gets switched off.
- **Symmetric q-error**, `max(est,actual)/min(est,actual)`, both floored at 1
  (PG's `clamp_row_est`, which goopg mirrors). A 1000× under- and a 1000×
  over-estimate are equally bad: one picks a nested loop over a hash join and
  the other the reverse.
- **Per-loop on both sides.** Multiplying the actual by `loops` and comparing
  against a per-loop estimate manufactures an error equal to the loop count on
  every parameterised path.
- **Ratchet, not a threshold.** The pinned baseline is the set of finding
  identities (`Q<n>:<relset>`). NEW findings fail; disappeared findings are
  reported as FIXED and prompt a re-pin. A count-only ratchet would let one
  fix pay for one regression.

### 5.2 Running it

```bash
make ea-ratchet          # full capture (~2 h) + ratchet against the pinned baseline
make ea-ratchet-repin    # full capture + rewrite the baseline
EA_CAPTURE=<file> make ea-ratchet    # re-score an existing capture, no server at all
```

Isolation is built in, not by convention: the script clones the SF0.5 data
directory to `tmp/c20a/data-sf05` and serves it on **5534** under
`GOOPG_CG_UNIT=goopg-ea-ratchet`. It never opens 65437 or 65433. A concurrent
writer on a shared bench data directory has damaged that cluster's WAL before.

Two hazards it handles because both were hit while building it:

- **one session for the whole corpus is wrong.** The first draft ANALYZEd and
  EXPLAINed in a single psql session (to defend against per-connection
  statistics). A server death on Q2 then cost every remaining query, and the
  scorer reported a clean gate over a population of two. It now runs one psql
  per query, checks liveness between queries, and restarts the server if a
  heavy query takes it out.
- **`GOGC=off` is the wrong regime here.** `bench/tpcds/env_tpcds.sh` exports
  `GOGC=off` because the SF0.5 sweep is a *timing* measurement. This gate
  measures row counts, for which `GOGC=off` is pure risk — one heavy query
  parks the heap at `GOMEMLIMIT` and every later query runs against a
  thrashing or SIGKILLed server. The script overrides to `GOGC=100`,
  `GOMEMLIMIT=12GiB`.

Every capture writes a `.header` sidecar carrying the engine binary's sha256
prefix, the commit, and the data directory — because an arm that silently ran
against a peer's binary has happened in this tree.

## 6. Disposition

C-20a stays open, re-scoped. Its successor and only true prerequisite is the
P0-02 remainder in §3: make EXPLAIN's `rows=` read `PlanCost.PlanRows`, with
the `plan_snapshots` re-pin and `estimateaudit` fixture re-capture in the same
commit. Once every Node-level consumer can read the path's number off the node,
`EstimateRows` becomes the legacy-rewriter fallback only, and the deletion is a
mechanical consequence rather than a re-implementation.

`joinkeyproof.go` should be struck from the item entirely: it is not a mirror,
and only `superkeyJoinEstimate` retires with `estimateJoin`.
