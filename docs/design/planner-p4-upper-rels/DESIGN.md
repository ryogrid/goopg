# C-11 / C-12 / C-13 (P4-02 / P4-03 / P4-04) — upper `RelOptInfo`s, a real upper-rel `PathSort`, and the bounded sort

Status: **design only**. Nothing here is implemented; no file under
`internal/` was modified while it was written (three implementation agents own
that tree concurrently). Every goopg claim below was read out of the tree at
`262d6484e`; every PostgreSQL claim is cited to `./postgres` (PG 18.3,
read-only oracle).

Items covered, one cut each:

| cut | item | one line |
|---|---|---|
| **Cut 1** | **C-11 / P4-02** | the upper-`RelOptInfo` registry and its five kinds, landed INERT — no plan may move |
| **Cut 2** | **C-12 / P4-03** | the `ORDERED` upper rel gets a real `PathSort` through `addPath`, so the top-level ORDER BY `Sort` is priced for the first time |
| **Cut 3** | **C-13 / P4-04** | the bounded / top-N sort, split into an executor half (**C-13a**) and `cost_tuplesort`'s `limit_tuples` arm (**C-13b**) |

Upstream design: take3 `08-target-design.md` §7; gate protocol take3
`09-verification-and-acceptance.md` §5 row **P4** plus §3 (correctness floor),
§4.3 (hygiene) and §6 (measurement methodology). Prerequisite scoping already
landed: C-10a (`docs/design/planner-c10a-grouping-sets-scope/DESIGN.md`),
C-10c (`docs/design/planner-c10c-upper-qual-placement/DESIGN.md`), C-10d
(measurement, `analysis/planner-refactor-take3/c10d-boundary-census-20260905/README.md`).
Adjacent and cross-referenced rather than duplicated:
`docs/design/planner-spill-cost-calibration/DESIGN.md` §6.1b, which recorded
from the other side that goopg's biggest sort is unpriced and referred the
question here.

---

## 0. The three findings that shape everything below

Stated first because each one contradicts a plausible reading of the item
titles.

**F1. `costSortRun` has exactly one production caller.** `grep -rn costSortRun
internal/ --include=*.go` returns one non-test call: `sortPathFor`
(`internal/optimizer/joinpathsmerge.go:441`). goopg therefore prices
**merge-join input sorts and nothing else**. Every top-level ORDER BY `Sort`,
every `Sort` an aggregate rule wraps, every DISTINCT sort is priced by
`DeriveLegacyDisplayCost` (`internal/optimizer/plancost.go:116`), which is
explicitly *not a cost model* — its own header says "this is NOT a cost model
and nothing may plan against it" and names Phase 4 as its deleter. Consequence:
**C-12 is not a cost-neutral re-plumbing.** The sorts it newly prices cost
*zero* today, so landing it moves costs strictly upward on exactly the shapes
that carry a big sort. See §5.4.

**F2. goopg's TPC-H corpus has no `LIMIT` at all.** The suite is HammerDB's
`pgolap.tcl` templates (`internal/testutil/tpch/tpch.go:110`), not the TPC-H
spec text: the spec's `LIMIT 100` / `LIMIT 20` / `LIMIT 10` on Q2/Q3/Q10/Q18/Q21
are **absent** from all 22 strings, and `grep -l Limit bench/tpch/plans-pg/*.txt`
matches **zero** files. So C-13 has **no TPC-H witness whatsoever** — not a weak
one, none. Its entire measurable surface is TPC-DS, where 81 of 99 committed PG
plans are `Limit`-rooted. Any ranking argument that reaches for "Q18 is
`ORDER BY … LIMIT 100` over 1.5M rows" is reading the spec, not the corpus.
See §6.5, which also says why C-13 is nevertheless still the best bet.

**F3. There is no upper-rel scaffolding of any kind.** `grep -rn
'upperRel\|UpperRel\|UPPERREL\|fetchUpperRel' internal/ --include=*.go` returns
nothing. `RelOptInfo` (`internal/optimizer/path.go:244`) is keyed by `Relids`
alone, registered in `searchCtx.relMap map[RelSet]*RelOptInfo`
(`joinsearch.go:113`) and in the per-level `joinrels` lists; the search
publishes a **`Node`**, not a rel and not a path
(`planJoinlistSearch`, `relfromjoinlist.go:184`, returns `(Node, error)`).
C-11 is therefore genuinely new structure, not a rename.

---

## 1. What an upper `RelOptInfo` is

### 1.1 PostgreSQL

`UpperRelationKind` (`postgres/src/include/nodes/pathnodes.h:69-81`) is an
eight-value enum:

```c
typedef enum UpperRelationKind
{
	UPPERREL_SETOP,				/* result of UNION/INTERSECT/EXCEPT, if any */
	UPPERREL_PARTIAL_GROUP_AGG, /* result of partial grouping/aggregation, if any */
	UPPERREL_GROUP_AGG,			/* result of grouping/aggregation, if any */
	UPPERREL_WINDOW,			/* result of window functions, if any */
	UPPERREL_PARTIAL_DISTINCT,	/* result of partial "SELECT DISTINCT", if any */
	UPPERREL_DISTINCT,			/* result of "SELECT DISTINCT", if any */
	UPPERREL_ORDERED,			/* result of ORDER BY, if any */
	UPPERREL_FINAL,				/* result of any remaining top-level actions */
	/* NB: UPPERREL_FINAL must be last enum entry; it's used to size arrays */
} UpperRelationKind;
```

`fetch_upper_rel` (`postgres/src/backend/optimizer/util/relnode.c:1458-1497`)
is a find-or-create keyed on `(kind, relids)` over `root->upper_rels[kind]`,
which is a plain `List` per kind — the function's own comment says "No code
outside this function should assume anything about how to find a particular
upperrel." What it initialises is small and is exactly the set `add_path` and
`set_cheapest` read (relnode.c:1480-1494):

```c
	upperrel->reloptkind = RELOPT_UPPER_REL;
	upperrel->relids = bms_copy(relids);

	/* cheap startup cost is interesting iff not all tuples to be retrieved */
	upperrel->consider_startup = (root->tuple_fraction > 0);
	upperrel->consider_param_startup = false;
	upperrel->consider_parallel = false;	/* might get changed later */
	upperrel->reltarget = create_empty_pathtarget();
	upperrel->pathlist = NIL;
	upperrel->cheapest_startup_path = NULL;
	upperrel->cheapest_total_path = NULL;
	upperrel->cheapest_unique_path = NULL;
	upperrel->cheapest_parameterized_paths = NIL;
```

Two properties are load-bearing for goopg:

- **The relids of an upper rel carry no fixed meaning.** relnode.c:1449-1451:
  "The meaning of the Relids set is not specified here, and very likely will
  vary for different relation kinds." Every in-tree `create_*_paths` caller
  passes `NULL` (e.g. `create_ordered_paths` at
  `postgres/src/backend/optimizer/plan/planner.c:5319`: "For now, do all work
  in the (ORDERED, NULL) upperrel"). An upper rel is therefore **not** a
  relset-keyed object and must never share the search's relset index.
- **`consider_startup` is the only `tuple_fraction` fact an upper rel carries**,
  and goopg already has the field: `RelOptInfo.ConsiderStartup`
  (`internal/optimizer/path.go:380`), documented against the same
  relnode.c:211/707 lines.

The chain in `grouping_planner` is a pipeline of upper rels, each consuming the
previous rel's `pathlist`: set-op → grouping → window → distinct →
`create_ordered_paths` (planner.c:5308) → `UPPERREL_FINAL`
(planner.c:1868 `fetch_upper_rel(root, UPPERREL_FINAL, NULL)`).

### 1.2 goopg today

`RelOptInfo` (`internal/optimizer/path.go:244-421`) already carries every field
`fetch_upper_rel` initialises, under goopg names: `Relids`, `Rows`, `Width`,
`Pathlist`, `PartialPathlist`, `CheapestTotal`, `CheapestStartup`,
`CheapestParameterized`, `ConsiderStartup`, `ConsiderParamStartup`,
`ConsiderParallel`. It also carries five goopg-specific fields an upper rel
must be given a policy for, because their zero values are *wrong* rather than
merely absent — see §4.3: `NCols`, `AvgVarBytes`, `ColVarBytes`, `baseLeaf`,
`baseOffset`.

What does not exist:

| PG | goopg | note |
|---|---|---|
| `UpperRelationKind` | — | C-11 introduces it |
| `root->upper_rels[kind]` | — | C-11 introduces it |
| `fetch_upper_rel` | — | C-11 introduces it |
| upper rel ≠ search rel | `relMap`/`joinrels` are the only registries | C-11 must keep upper rels OUT of both |
| `grouping_planner` pipeline | a linear sequence of Node rewrites, §2 | C-11..C-18 retire them one at a time |

### 1.3 The three boundaries an upper rel must cross, and which are permanent

1. **The search-root boundary.** `createPlanAtSearchRoot(p *Path, bindingWidth
   int) Node` (`internal/optimizer/createplanroot.go:80`) turns the chosen path
   into a Node and emits a reordering `Project` republishing the search's
   output in pre-search *binding* coordinates (elided when the layout already
   is binding order). Above this line goopg is a Node tree in binding /
   output-schema coordinates; below it, a Path tree in relset coordinates.
   **This boundary is not permanent** — P6-02 replaces `baseLeaf`/`baseOffset`
   with a real range table — but it is permanent for C-11..C-13. §4.2 says how
   the upper rels sit on top of it without moving it.

2. **The FROM-subquery / CTE / view boundary.** C-10d measured it: a derived
   table is planned as an opaque prebuilt leaf (`planSubqueryRangeVar`,
   `planner.go:4295`), a full `pull_up_subqueries` port would remove only
   18 of 46 ABOVE-BLOCKING boundaries (39%), and TPC-H Q9 — P4-01's own witness
   — puts its whole 6-way join tree one scope down. **Treat as permanent.**
   C-11's upper rels must be per-planning-scope by construction, and
   `relfromjoinlist.go:26-46` already ledgers exactly what that costs C-12
   (must always stack a full Sort, since the sub-problem committed to one path)
   and C-13 (an outer LIMIT cannot push a bound into an inner scope).

3. **The `MaybeAddGather` boundary.** The Gather decision is not in the planner
   at all: `optimizer.MaybeAddGather` is called from
   `internal/postmaster/dispatch.go:1525` and
   `dispatch_extended.go:160`, **after** the plan-cache lookup, on the finished
   tree. `parallel.go:9-11` states the reason ("goopg's planner has no path
   abstraction to extend"). **Permanent for C-11..C-13**; C-19h retires it.
   §7 is entirely about surviving it.

---

## 2. What does this job today — the retirement inventory

This is the list C-11 through C-18 eventually delete. It is given in
**execution order** inside `planSelectWithSettings`
(`internal/optimizer/planner.go:847`), because the order is the part a
path-based replacement has to reproduce.

| # | site | what it is | PG counterpart | retired by |
|---|---|---|---|---|
| 1 | `planner.go:670` `wrapSetOpSortLimit` (called :1097, :1114) | wrap-on-top: attaches trailing ORDER BY/LIMIT to a finished set-op tree | tail of `grouping_planner` for set-ops | C-18 |
| 2 | `planner.go:9991` `rewriteMinMaxAggregates` + `:10370` `wrapMinMaxOrderByDistinct` | tree rewrite then re-wrap | `preprocess_minmax_aggregates` (planagg.c) | none of C-11..C-13 |
| 3 | `planner.go:7359` `buildAggregateStage` | constructs the single `*Aggregate`; `Strategy` is fixed at construction, never compared | `create_grouping_paths` | C-15 |
| 4 | `groupagg_indexorder.go:63` `applyIndexOrderedGroupingRule` | **rule** + rewrite: group keys matching a btree prefix ⇒ swap the child for a full-range index scan and force `AggStrategySorted` | `get_useful_group_keys_orderings` | C-15 |
| 5 | `group_input_target.go:268` `stampAggregateInputTarget` | compute-only stamp, no mutation (B-01c) | `make_group_input_target` | — (already the P4-01 shape) |
| 6 | `groupagg_presorted.go:45` `applyPresortedAggregateRule` | **rule** + wrap: an aggregate with internal ORDER BY/DISTINCT gets a `Sort` under it and `AggStrategySorted` | `adjust_group_pathkeys_for_groupagg` | C-15 |
| 7 | `groupagg_hashagg.go:60` `applyEnableHashAggRule` | **rule** + wrap: `enable_hashagg=off` ⇒ `Sort` + `AggStrategySorted` | `cost_agg`'s hashed-arm suppression | C-15 |
| 8 | `planner.go:6650` `buildWindowStage` | constructs the `*WindowAgg` chain; placement fixed by construction | `create_window_paths` | C-18 |
| 9 | **`planner.go:1720`** and the SRF twin **`:1766`** — `orderSort = &Sort{pos: s.Pos(), Child: node, Keys: keys}` | **wrap-on-top: the top-level ORDER BY Sort** | `create_ordered_paths` (planner.c:5308) | **C-12** |
| 10 | **`planner.go:1806`** — `node = &Limit{…}` | wrap-on-top: LIMIT/OFFSET/WITH TIES | `create_limit_path` (pathnode.c:4118) | **C-13** (reassigned from C-16 by C-10c) |
| 11 | `planner.go:15234/15505` `tryPromoteIndexOnlyScan` / `tryPromoteOrderedIndexOnlyScan` | scan-level rewrite; the ordered variant can **retroactively delete** the Sort from step 9 | index-only path generation + pathkey satisfaction | C-12 must coexist — §5.5 |
| 12 | `planner.go:1969` `liftLimitAboveLockRows` | tree rewrite: `Project(Limit(x))` ⇒ `Limit(LockRows(Project(x)))` for SKIP LOCKED | LIMIT/rowmark interaction | C-13a must coexist — §6.3 |
| 13 | `planner.go:2074` `&DistinctOn{…}` (+ an implicit extra `Sort`) | wrap-on-top | `create_distinct_paths` DISTINCT ON arm | C-16 |
| 14 | `planner.go:2080` `&Distinct{…}` | wrap-on-top | `create_distinct_paths` | C-16 |
| 15 | `planner.go:~2124` a **second** `&Sort{…}` above the `Distinct` | wrap-on-top, stacked: `Distinct`'s internal sort is ascending-only so ORDER BY is re-applied | `create_distinct_paths` × `create_ordered_paths` | C-16 + C-12 |
| 16 | `parallel.go:100` `MaybeAddGather` | post-pass over the finished tree; splits `*Aggregate` into partial+finalize, or leaves a `*Sort` for merge-on-gather | `generate_gather_paths` + partial/finalize agg paths | C-19h |
| 17 | `parallel_agg.go:113` `aggregateSplitIsSafe`, `:282` `splitAggregateIsProfitable` | name whitelist + a groups/rows heuristic — not a cost comparison | parallel-safety + `add_path` | C-19g |
| 18 | `plancost.go:116` `DeriveLegacyDisplayCost` | fabricates EXPLAIN costs for every node above the seam | none — it exists only because the seam does | C-15/C-16/C-18 finish it; **C-12 removes its `*Sort` arm's monopoly** |

### 2.1 What C-11..C-13 must COEXIST with (the field is not clean)

Explicitly, so no cut is written assuming a later cut has already landed:

- **`MaybeAddGather` is still the only parallelism decision** until C-19h, and
  C-19d (priced `PathGather`) is landing concurrently. §7.
- **The legacy estimators are still live** until C-20a: `EstimateRows(n)`
  (`plancost.go`) is the only row count available for a Node above the seam,
  so C-12's upper rel must take `Rows` from it. That is a temporary read, and
  it is the correct temporary read — see §4.3.
- **All three aggregate rules (rows 4/6/7) still run**, so the `*Sort` nodes
  they wrap are still built by the rules, not by paths. C-12 prices only the
  top-level ORDER BY sort, not those. Stated because "C-12 prices sorts" is
  otherwise ambiguous.
- **`DeriveLegacyDisplayCost` stays**, minus the top-level `Sort`. Its `*Sort`
  arm (`plancost.go:143`) and `sortComparisonDisplayCost` (`:184`, whose own
  comment says "The whole-sort variants (external merge, bounded top-N) are
  Phase 4's business") remain reachable for the rule-built sorts.
- **C-10c's contract holds**: every outer-join link a Phase-4 item can see has
  already survived `reduceOuterJoins` demotion, so the only remaining
  obligation is *placement*, and `pushSingleSideQualsIntoInnerJoinInputs`
  still descends the `*Sort` and `*Limit` arms C-12/C-13 would otherwise
  delete. **Neither C-12 nor C-13 deletes those arms** (both keep emitting
  `*Sort` / `*Limit` nodes at the same tree positions), so C-10c's per-item
  re-assert for C-12/C-13 is satisfied by a fixture asserting the arms are
  still reached, not by moving the delay test.
- **`GOOPG_PGSHAPED_COLLAPSE` / the C-04 single-problem work** is in flight
  below the seam. C-11..C-13 touch nothing below it.

---

## 3. Cut 1 — C-11: the upper-rel registry, landed inert

**Goal: a `RelOptInfo` that is reachable by kind, is not a search rel, and
changes no plan.** The model is C-08, which landed a whole PG derivation
"provably inert until C-04".

### 3.1 What lands

New file `internal/optimizer/upperrel.go`:

```go
type UpperRelKind int

const (
    UpperSetOp UpperRelKind = iota
    UpperPartialGroupAgg
    UpperGroupAgg
    UpperWindow
    UpperPartialDistinct
    UpperDistinct
    UpperOrdered
    UpperFinal   // must stay last: it sizes the array (pathnodes.h:80)
)

type upperRels [UpperFinal + 1][]*RelOptInfo

func fetchUpperRel(u *upperRels, kind UpperRelKind, relids RelSet, tupleFraction float64) *RelOptInfo
```

Mirroring relnode.c:1458-1497 exactly: find-or-create on `(kind, relids)`;
`ConsiderStartup = tupleFraction > 0`; `ConsiderParamStartup = false`;
`ConsiderParallel = false`; empty pathlist and nil cheapest slots.

### 3.2 Three decisions C-11 must make, with recommendations

**Decision 1 — where the registry lives.** PG hangs `upper_rels[]` off
`PlannerInfo`. goopg's nearest equivalent is `searchCtx`, but that is the wrong
home: a `searchCtx` exists **per search problem** (`searchOneProblem`,
`relfromjoinlist.go`), and a statement with a pinned spine or a sub-joinlist
has several, while the upper rels are per **statement scope**. Recommend: the
registry is a value on the statement-level planning context that
`planSelectWithSettings` already threads (the same place `ctx.tupleFraction`
and `ctx.queryPathkeys` are set, `planner.go:1317`/`:1385`), constructed once
per `planSelectWithSettings` invocation — so a subquery, a CTE and a view body
each get their own, which is exactly the C-10d permanent boundary expressed in
data.

**Decision 2 — relids.** Recommend **`Relids = 0`** for every upper rel, which
is `fetch_upper_rel(root, KIND, NULL)`, what every in-tree PG caller passes.
Two goopg-specific payoffs: it is impossible to confuse an upper rel with a
search rel in any relset-keyed structure, and `tracePath`
(`pathtrace.go:68`) already renders `RelSet(0)` as `relids=-`
(`relSetBits`, `:79`), so DPPATH lines for upper-rel paths are unambiguous
**with no format change** and no `enumtrace.go` parser change (that parser
anchors on `DPTRACE`, not `DPPATH` — `pathtrace.go:20-30`).

**Decision 3 — upper rels are NOT in `relMap` or `joinrels`.** `makeJoinRel`
(`joinsearchlevel.go:523`) is a find-or-create over `relMap`, and
`finalRel` (`joinsearch.go:291`) asserts exactly one rel at the top level. An
upper rel inserted into either would corrupt both. This is a one-line
invariant with a test, and it is the single most likely way to break the
search from above.

### 3.3 Why it is inert

C-11 adds a type, a constructor and a registry with **no producer and no
consumer**. `planSelectWithSettings` constructs the registry and nothing reads
it. This is checkable rather than argued: no existing call site changes, so no
Node is built differently.

### 3.4 Scope estimate

**~120–200 LOC production + ~150 test LOC. Estimated by structural analogy**,
not by grep: the file is `fetch_upper_rel` (40 lines in C, ~50 in Go), the enum
(8 constants), and the registry accessor, plus the invariant tests. Marked as
an estimate. Note the standing caution that grep oversizes Go changes here
(52 sites by grep vs 4 by the compiler on a past type change) — but this cut
adds rather than changes, so grep is not the risk.

### 3.5 Gate

P4 row of take3 09 §5 applies but is nearly vacuous for an inert cut. The
enforceable claims are:

- units suite clean; pgbench smoke via hook (never `--no-verify`);
- `make plan-gate` **byte-identical**, both `-digest` and `-diff` on TPC-H
  `VERDICT: PASS`, TPC-DS SF0.5 `PASS=95 MISMATCH=0 CKMISMATCH=0`;
- PP on both suites: `changed=0`. An inert cut that moves one plan is a failed
  cut, and this is the whole test.

**Negative result:** any PP delta, any plan-gate delta. That means the registry
acquired a reader that was not intended — most likely through `relMap`
(Decision 3). Fix or revert; do not "explain" it.

---

## 4. The structural question C-12 turns on: how does a Path get above the seam?

C-12 needs an upper rel whose pathlist contains a `PathSort` over *something*.
The something is a finished Node (the aggregate / window / join tree), because
the seam publishes a Node (F3). Three options; the recommendation is (b).

**(a) Move the seam.** Make `createPlanAtSearchRoot`'s reordering `Project` a
path kind and let the upper chain be pure Paths down to the leaves. Correct,
and it is where P6-02 ends up. Rejected for C-12: it changes what every
`createPlanNode` arm's layout contract means, at the same time as three other
agents are editing those arms, and it makes C-12 a two-variable commit, which
09 §5's sequencing rule forbids.

**(b) `PathPrebuilt` over the finished Node — recommended.** `PathPrebuilt`
(`path.go:51`) exists precisely for this: "the C0 bridge: while path generation
does not yet exist, the join subtree … is wrapped in a `PathPrebuilt` so
`createPlan` has something to translate". `newPrebuiltPath(rel, n)`
(`path.go:492`) is the constructor. C-12 wraps the finished child Node in a
`PathPrebuilt` over the `ORDERED` upper rel, stacks a `PathSort` on it via
`addPath`, runs `setCheapest`, and calls `createPlanNode` on the winner.
`createPlan.go:64` already has the `PathPrebuilt` arm and
`createplansimple.go:148` `createSortPlan` already has the `PathSort` arm.

**(c) Keep the Node rewrite and only compute a cost.** Rejected outright: it is
a second cost model beside `addPath`, which design ch. 04 §1 forbids, and it
buys nothing C-13b could build on.

### 4.1 Why option (b)'s coordinate story is already correct — this is the part that looks like a trap and is not

`createSortPlan` (`createplansimple.go:148-173`) translates each pathkey
expression through `translateToLayout` **only when the child layout is
non-nil**:

```go
	var index map[int]int
	if childLayout != nil {
		index = childLayout.bindingIndex()
	}
```

and `createPlanNode`'s `PathPrebuilt` arm returns `baseRelLayout(p.Rel, p.node)`
(`createplan.go:65`), which returns **nil** when `rel.baseLeaf == nil`
(`createplanjoin.go:124-129`). An upper rel has no `baseLeaf` by construction.
So an upper-rel `PathSort` emits its keys **as written** — which is exactly
right, because the ORDER BY keys built at `planner.go:1716` are already
resolved against the child's output schema (post-aggregate via
`resolveExprAfterAggregate`, post-window via `resolveExprAfterWindow`), not in
binding coordinates. The existing arm is reusable **verbatim**.

This was verified by reading, and it is worth stating loudly because the
obvious hypothesis — "the arm exists but its key translation is written for
merge children, so it will mis-translate an upper-rel sort" — is *false*, and
chasing it would be the Q8 failure mode again (five hypotheses about the wrong
thing).

### 4.2 So what actually blocks C-12 today

Four things, all read out of the tree:

1. **No producer.** `sortPathFor` (`joinpathsmerge.go:430`) is the only
   `PathSort` producer, and its own doc says it is **deliberately not offered
   to `addPath`**: "it belongs to this merge candidate, not to the input
   relation's pathlist, and adding it there would let a sort generated for one
   pair change another pair's `CheapestTotal`." So `PathSort` has never once
   been through the dominance tournament.
2. **No rel to add it to.** F3 — C-11 is the prerequisite, and it is the
   *whole* prerequisite.
3. **No cost.** F1 — `costSortRun`'s only caller is `sortPathFor`, so the
   top-level sort is priced by `DeriveLegacyDisplayCost` and contributes
   nothing to any comparison.
4. **The upper rel's `NCols` would be 0**, which silently suppresses the
   external-merge arm. See §4.3 — this is the real trap.

### 4.3 The real trap: a fresh upper rel prices a 1.5M-row spilling sort as an in-memory quicksort

`costSortRun` (`cost_funcs.go:267`) gates its disk arm on `ncols > 0`:

```go
	if ncols > 0 && cp.workMem > 0 {
		inputBytes := tuples * hashsize.EntryBytes(ncols, 0)
		...
	}
```

with the documented reading "Zero means 'column count unknown' and suppresses
the disk arm … an unknown width must not invent an I/O charge." `sortPathFor`
supplies it as `relNCols(sub.Rel)` (`joinpathsmerge.go:441`), and `relNCols`
(`path.go:475`) falls back to `rel.baseLeaf.Output()` — **both of which are
zero/nil on a fresh upper rel**.

So a C-12 that calls `fetchUpperRel` and then `sortPathFor`-shaped costing
would price goopg's largest sorts with the in-memory branch only. Concretely:
`bench/tpch/plans-pg/Q18.txt` line 2 is
`Sort (cost=486946.95..488130.80 rows=473537 width=79)` in PG; the
spill-calibration probe measured goopg's own Q18 top sort at
`rows=1565307 width=204` (recorded in
`docs/design/planner-spill-cost-calibration/DESIGN.md` §6.1b). At 48 bytes per
`Datum` plus payload that is far past `sortChunkBytes = 256 MiB`
(`internal/executor/operators.go:898`), so the node **spills at run time** while
being priced as if it did not. That is precisely the asymmetry
`costSortRun`'s header was written to kill on the merge side ("two
independently calibrated models competing inside one `addPath` comparison").

**Requirement, therefore: C-11's `fetchUpperRel` must populate `Rows`, `Width`,
`NCols` and `AvgVarBytes` from the input**, not leave them zero. For C-12 the
input is a Node, so:

- `Rows` ← `EstimateRows(child)` (the legacy estimator — a temporary read,
  retired by C-20a, and named as such in the code comment);
- `Width` ← `TupleWidth(child.Output())` (`plancost.go` already does this);
- `NCols` ← `len(child.Output())`;
- `AvgVarBytes` ← the one honestly-unavailable number above the seam. Two
  candidate answers and **this is the one thing in the cut that cannot be
  settled from the code alone**: (i) 0, which under-states the entry and
  therefore under-charges the spill in the same direction the bug above does;
  (ii) propagate the search-root rel's `AvgVarBytes` through the boundary,
  which over-states after an aggregate collapses rows to a few numeric columns.
  **Probe P1 (§9) settles it by measurement.** Do not pick one by argument.

---

## 5. Cut 2 — C-12: the `ORDERED` upper rel with a real `PathSort`

### 5.1 Shape

At `planner.go:1720` (and the SRF twin at `:1766`), replace the unconditional
`orderSort = &Sort{…}` with:

```
ordered := fetchUpperRel(reg, UpperOrdered, 0, tupleFraction)   // C-11
seed    := newPrebuiltPath(ordered, node)                        // §4 option (b)
addPath(ordered, seed-with-input-cost, "upper.ordered.input")    // only if it already delivers the keys — today: never (§5.5)
addPath(ordered, sortPathOver(seed, keys, cp), "upper.ordered.sort")
setCheapest(ordered)
best := getCheapestFractionalPath(ordered, tupleFraction)
node, _ = createPlanNode(best)
```

with `sortPathOver` the upper-rel sibling of `sortPathFor` — same `costSortRun`
call, same `disabledNodesFor(!cp.enableSort, …)` treatment, `ParallelSafe`
per `create_sort_path`'s rule (`pathnode.c:3236`: `rel->consider_parallel &&
subpath->parallel_safe`).

`createPlanNode` then produces `&Sort{pos, Child, Keys}` through the existing
arm — **the same node, at the same position, with the same keys** as today.

### 5.2 One non-obvious obligation: keep the B-01c stamp

Today `planner.go:1721` calls `stampSortInputTarget(orderSort, nil)`
immediately after construction, and `sort_input_target.go`'s
`assertSortInputTargetCoversKeys` is its consumer. `createSortPlan` does not
stamp. C-12 must stamp the node `createPlanNode` returns, or B-01c's assertion
loses its subject silently (`InputTargetKnown == false` reads as "unknown",
which is the *safe* direction and therefore will not fail a test — which is
why it has to be said here).

### 5.3 What is provably unchanged

- **Node tree shape**: identical. Same `*Sort`, same child, same keys.
- **Parallelism**: `MaybeAddGather`'s two decision functions read structure and
  sizes, never `PlanCost` — `sortPartialRootPays` (`parallel.go:354`)
  switches on `drivingScan(srt.Child)`'s Go type; `terminatesPartial`
  (`:363`) switches on node type; `splitAggregateIsProfitable`
  (`parallel_agg.go:282`) takes rows/groups/workers. So C-12 cannot move a
  Gather. **This is the claim that makes C-12 safe under the "cost model has no
  parallel dimension" hazard, and it must be re-verified by reading those three
  functions at implementation time, not taken from this document.**
- **Values**: nothing about tuple production changes.

### 5.4 What does change, and it is not nothing

The `*Sort`'s EXPLAIN cost columns. Today they come from
`DeriveLegacyDisplayCost` (`plancost.go:143`: `childTotal +
sortComparisonDisplayCost`, in-memory shape only); after C-12 they come from
`costSortRun`, which adds the external-merge term. On Q18-class nodes that is a
large upward move. Consequences to plan for:

- `make plan-gate` (`cmd/plan-snapshot`) diffs goopg-vs-goopg **including
  costs** → **must be re-pinned in the same commit**, with the diff inspected
  line by line and every changed cost attributable to a `Sort`.
- PP excludes costs from the normalised tree ("costs/rows/widths/times excluded
  to a separate column", 09 §2.2), so **PP should read `changed=0` on tree
  shape** while the cost column moves. If PP reports a shape change, C-12 did
  something it was not supposed to.
- The EXPLAIN cost column moves toward PG, which is the point; record the
  before/after for Q18/Q10/Q16 as evidence rather than as a target.

### 5.5 Two paths, or one? — and the C-07 hand-off

Today the `ORDERED` rel receives **exactly one** path (the sort), because
nothing above the seam carries `Pathkeys`: `planJoinlistSearch` returns a Node
and drops them, which `querypathkeys.go:24-40` documents at length. So C-12 as
specified has **no path competition** and cannot pick wrong. That is a feature
for a first cut and a limitation to state.

The second path appears when C-07's unlanded half lands — widening
`addOrderedIndexPaths`' useful-column set to the query-pathkey columns, which
`querypathkeys.go:44-49` says is "a map union at one line" in
`pathindexordered.go`'s `colExprs`. `TestAddOrderedIndexPathsGateIsCompleteButGenerationIsNot`
exists specifically to be flipped red-to-green by C-11/C-12. **C-12 should not
flip it**: doing so is a second variable in the same commit. File it as C-12a.

When C-12a does land, the rule "verify BOTH candidates were generated before
comparing costs" becomes live, and it is already satisfied by construction:
`addPath` emits a `DPPATH path producer=… verdict=accepted|dominated` line per
candidate (`pathtrace.go:54-73`) under `GOOPG_PGSHAPED_DP_TRACE=1`. Use
producer strings `upper.ordered.input` and `upper.ordered.sort`, and the
adjudication is a grep, not an investigation.

Note the interaction with row 11 of §2: `tryPromoteOrderedIndexOnlyScan`
(`planner.go:15505`) can *retroactively delete* the ORDER BY Sort when a
covering index already delivers the order. It runs at `planner.go:1899`, i.e.
**after** the Sort is built, and C-12 does not change that. It stays a
post-pass; C-12a is what would let the same outcome be reached by cost. Both
being live simultaneously is fine (the post-pass only ever removes a Sort the
path model would then also have declined) but it is a shape C-12a's gate must
include.

### 5.6 Scope estimate

**~150–250 LOC production + ~250 test LOC.** Estimated as: one new producer
function (~60 lines with the doc comment this repo writes), the call-site
replacement at two sites in `planner.go`, the `fetchUpperRel` field population
of §4.3, plus the plan-gate re-pin. **Estimate, not a measurement** — the
honest way to sharpen it is to make the change and run `go build`, per the
grep-oversizing lesson.

### 5.7 Gate (take3 09 §5 P4) and what a negative result looks like

| step (09 §2.3) | instrument | pass condition |
|---|---|---|
| 1 | PP both suites | **tree shape `changed=0`**; cost column moves on Sort-bearing plans only |
| 2 | attribution | every timing move maps to a query whose Sort cost moved |
| 3 | TPC-DS sweep TOTAL arm | within ±17% |
| 4 | `tpch-runner -digest` + `-diff` | `VERDICT: PASS` |
| 5 | **T on every query whose plan or cost moved** | no query > 1.2× its pre-cut time (bar B2) |
| 6 | EA ratchet | slack/floor never loosen |
| — | `make plan-gate` | re-pinned in-commit, diff fully attributed |
| — | TPC-DS SF0.5 | `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` |

Step 5 is not optional and not satisfied by step 4: **a row-count gate cannot
catch a plan-shape regression** — all 21 TPC-H result sets stayed byte-identical
while Q2 went 43× slower. Since C-12 is *predicted* not to move any plan, the
practical form is: if PP says nothing moved, time the ten longest queries on
both suites anyway as an A/A check, holding server age constant (a server that
just ran a timeout query sits at GOMEMLIMIT with GOGC=off and mimics a
regression — 09 §6).

**Negative results, stated in advance:**

- *PP reports a shape change.* C-12 moved a plan it had no mechanism to move.
  Almost certainly `MaybeAddGather` — go straight to §5.3's three functions and
  re-read them; if one of them has acquired a `PlanCost` reader since this
  document was written, C-12 is a parallel-affecting change and must be
  re-designed with a serial control arm.
- *Timing regresses with no shape change.* Not C-12 (it changes no executed
  work). Re-run the baseline before believing it — TPC-H drift past ~1.7%
  late-session is documented (baseline 213.84 s re-ran 221.01 s on identical
  code).
- *A `Sort`'s new cost is LOWER than its legacy display cost.* Then §4.3's
  `NCols` population did not happen and the disk arm is suppressed. This is the
  specific failure to look for first, because it is silent.

---

## 6. Cut 3 — C-13: bounded / top-N sort

### 6.1 The formula, transcribed

`cost_tuplesort` (`postgres/src/backend/optimizer/path/costsize.c:1898-1982`).
The `limit_tuples` arm is two pieces. First the output-size selection
(costsize.c:1918-1929):

```c
	/* Do we have a useful LIMIT? */
	if (limit_tuples > 0 && limit_tuples < tuples)
	{
		output_tuples = limit_tuples;
		output_bytes = relation_byte_size(output_tuples, width);
	}
	else
	{
		output_tuples = tuples;
		output_bytes = input_bytes;
	}
```

then the middle branch of the three-way arm (costsize.c:1960-1969):

```c
	else if (tuples > 2 * output_tuples || input_bytes > sort_mem_bytes)
	{
		/*
		 * We'll use a bounded heap-sort keeping just K tuples in memory, for
		 * a total number of tuple comparisons of N log2 K; but the constant
		 * factor is a bit higher than for quicksort.  Tweak it so that the
		 * cost curve is continuous at the crossover point.
		 */
		*startup_cost = comparison_cost * tuples * LOG2(2.0 * output_tuples);
	}
```

and the run cost is deliberately **not** pro-rated (costsize.c:1976-1982):
"Note it's correct to use `tuples` not `output_tuples` here — the upper LIMIT
will pro-rate the run cost so we'd be double counting the LIMIT otherwise."

Note the branch order: the **disk** branch is tested first on
`output_bytes > sort_mem_bytes`, so a useful LIMIT that shrinks `output_bytes`
below `work_mem` **removes the spill charge entirely** and lands in the bounded
branch. That is where the money is, and goopg's `costSortRun` currently cannot
express it because it tests `inputBytes > sortMemBytes` with no notion of
output bytes (`cost_funcs.go:279-282`).

goopg's own header already names the gap
(`internal/optimizer/cost_funcs.go:254-259`):

> Only two of upstream's three branches can be reached from here. PG's middle
> branch is the bounded heap-sort for a useful LIMIT (`limit_tuples`); goopg
> has no LIMIT-aware sort path, so `output_tuples == tuples` and
> `output_bytes == input_bytes` identically … Ledgered with the LIMIT
> push-down.

### 6.2 Where `limit_tuples` comes from, and where C-13's plumbing stops

PG derives it in `grouping_planner` (`planner.c:1451-1462`):

```c
	if (parse->limitCount || parse->limitOffset)
	{
		tuple_fraction = preprocess_limit(root, tuple_fraction,
										  &offset_est, &count_est);
		if (count_est > 0 && offset_est >= 0)
			limit_tuples = (double) count_est + (double) offset_est;
	}
```

and hands it to `create_ordered_paths` (planner.c:1852-1857) as
`have_postponed_srfs ? -1.0 : limit_tuples`.

**The distinction that matters:** `root->limit_tuples` is separately forced to
`-1.0` whenever the query has grouping / grouping sets / DISTINCT / aggregates /
window functions / target SRFs / HAVING (planner.c:1625-1635) — but that is the
value handed *down to the scan/join level*. `create_ordered_paths` receives the
**local** `limit_tuples`, unconditionally. So an aggregate query's top-level
ORDER BY sort **does** get the bound. Nearly every TPC-DS query is exactly that
shape, so getting this wrong would zero out the item.

**goopg already has the derivation and throws half of it away.**
`preprocessLimit` (`internal/optimizer/tuplefraction.go:97`) is
`preprocess_limit` including the `limitEstimates{count, offset}` pair
(`:75`), with upstream's three-state encoding preserved (0 absent, positive
estimate, −1 present-but-unestimatable). Its only production caller is
`searchTupleFraction` (`joinsearchseam.go:685`), which discards it:

```go
	tf, _ := preprocessLimit(&Limit{Limit: l, Offset: o}, 0)
	return tf
```

So **C-13's planner-side plumbing is: stop dropping the second return value,
and carry `count+offset` to the `ORDERED` upper rel.** That is the whole of it.

**Where it stops, precisely.** C-17 (`tuple_fraction` end-to-end) is the item
that threads the *fraction* through every upper rel. C-13 needs only the
*absolute bound* at one rel. The two are different numbers with different
lifetimes (`tuplefraction.go:32-40` explains the overloading), and C-13 must
not touch `searchCtx.tupleFraction`, `getCheapestFractionalPath`, or
`ConsiderStartup`. Concretely, C-13 adds one field to the statement-level
planning context beside `ctx.tupleFraction`, and reads it at exactly one place.
Anything more is C-17.

Two guards to copy, both upstream:

- **`have_postponed_srfs ⇒ −1`** (planner.c:1856). goopg's equivalent is the
  SRF post-sort arm at `planner.go:1766`: that Sort sits above the ProjectSet,
  so its input row count is post-expansion and the pre-expansion LIMIT count is
  not a bound on it. Pass no bound on that arm.
- **`limitParseConst` already declines non-constants** (`joinsearchseam.go:696`,
  rendering an unresolved clause as a `*ParamRef` so `constInt` refuses). Keep
  that: a `LIMIT $1` gets no bound. Known divergence from PG, already ledgered
  (2026-08-05): PG runs `estimate_expression_value` first, so `LIMIT 5+5` and a
  bound `LIMIT $1` are constants to PG and not to goopg. C-13 inherits the
  ledger row; it does not widen it.

### 6.3 C-13 splits in two, and the halves are independent

This is the design's main re-scoping recommendation.

**C-13a — the executor bound.** goopg's `sortOp`
(`internal/executor/operators.go:790-870`) has **no bound of any kind**: it
buffers everything, spilling at `sortChunkBytes = 256 MiB` (`:898`), then
sorts. `internal/optimizer/parallel.go:294` says so in as many words: "goopg's
Sort carries no top-N limit, so there is no per-worker truncation to reason
about." The runtime win the item's title claims ("the largest recorded
`ORDER BY … LIMIT` win", from
`docs/design/not_ralph/parallel-sort/DESIGN.md` §8/§9.3) is **entirely this
half**, and it needs neither C-11 nor C-12: it is a `Bound` field on
`optimizer.Sort` plus a k-heap in `sortOp`, and it changes no plan and no cost.

**C-13b — the cost arm.** `costSortRun` grows the `limit_tuples` parameter and
the middle branch. This one **does** require C-12, because until an upper-rel
`PathSort` exists there is nothing to price.

Ordering consequence: **C-13a can land before C-11.** It is behaviour-preserving
(same rows, same order — a bounded top-k of the same comparator is the same
first k rows), independently measurable, and does not touch the planner. C-13b
lands after C-12 and is a cost-only change whose only observable effect, until
C-12a gives the `ORDERED` rel a second path, is the EXPLAIN cost column.

**C-13a's design, minimally.** PG's mechanism is dynamic:
`ExecSetTupleBound` (`postgres/src/backend/executor/execProcnode.c:848`) is
called from `ExecReScan`-time in `nodeLimit.c:423` and descends through
`SortState`, `IncrementalSortState`, `AppendState` (:914),
`MergeAppendState` (:927), a projecting `ResultState` with no qual (:941),
`SubqueryScanState` only when `qual == NULL` (:952), `GatherState` (:969) and
`GatherMergeState` (:978) — and *stops* at anything that can discard or combine
rows. The bound itself is `compute_tuples_needed` (`nodeLimit.c:431-436`):

```c
	if ((node->noCount) || (node->limitOption == LIMIT_OPTION_WITH_TIES))
		return -1;
	/* Note: if this overflows, we'll return a negative value, which is OK */
	return node->count + node->offset;
```

goopg has no `ExecSetTupleBound` and no rescan-with-changed-params machinery for
this. Recommended goopg form: a **static, planner-stamped bound**, set where the
`*Limit` is built (`planner.go:1806`) onto the `*Sort` **only when the Limit's
direct child is that Sort**, only when both count and offset are constants
(reusing `limitParseConst`/`limitClauseEstimate`, so the constant test lives in
one place), and **never for `WITH TIES`** (mirroring `compute_tuples_needed`).
Conservative on purpose: the direct-child restriction sidesteps having to
re-derive PG's descent whitelist, and it covers the TPC-DS corpus, whose LIMITs
are literals directly above the ORDER BY.

Three coexistence obligations for C-13a:

- `liftLimitAboveLockRows` (`planner.go:1969`) restructures
  `Project(Limit(x))` into `Limit(LockRows(Project(x)))` for SKIP LOCKED. It
  runs *after* the stamp. Assert the stamp is dropped or re-derived on that
  path; a `LockRows` between `Limit` and `Sort` discards rows and invalidates
  the bound.
- `MaybeAddGather` may turn the stamped `*Sort` into a per-worker sort under a
  `GatherMerge` (`parallel.go:295-299`). This is **correct** — PG does the same
  (`ExecSetTupleBound` recurses through `GatherMergeState`, execProcnode.c:978,
  "no one worker could possibly need to return more tuples than the Gather
  itself needs to") — but `parallel.go:294`'s comment becomes false and must be
  updated in the same commit.
- EXPLAIN: PG reports `Sort Method: top-N heapsort`. goopg's `SortStat`
  (`operators.go:885`) supports `quicksort` / `external merge`. Adding the third
  is PG-parity work; it is visible to `pg-oracle-diff` and to regress but not to
  PP (which compares node types, not Sort Method lines).

### 6.4 Scope estimate

C-13a: **~60–120 LOC planner + ~150–250 LOC executor + ~250 test LOC.** The
executor number is the uncertain one — a k-heap over `rows`/`keyvals`/`ctids`
kept in lockstep (`operators.go:806-830` documents that lockstep as a live
hazard: "a `keyvals` that outlived a flush would be offset by every spilled row
and silently compare the wrong keys"). C-13b: **~40–80 LOC.** **Estimates.**

### 6.5 Which queries can move — and the corpus problem

Because of **F2**, TPC-H contributes nothing: 0 of 22 queries carry a LIMIT and
0 of 22 committed PG plans contain a `Limit` node. C-13's corpus is TPC-DS.

Census of the committed PG plans (`bench/tpcds/plans-pg/*.txt`, 99 files):

- **81** plans are `Limit`-rooted;
- of those, **39** have a `Sort` (or `Incremental Sort`) as the Limit's
  immediate child;
- of those 39, only **2** sort ≥ 10 000 rows: Q22 (`rows=71857`) and Q67
  (`Incremental Sort`, `rows=34106`). The remainder are tens to hundreds of rows,
  where a bound saves nothing measurable.

**That census is PG's, and it understates goopg's opportunity — probably by a
lot.** PG's 42 non-Sort children (`GroupAggregate`, `WindowAgg`, `Append`,
`Hash Join`, `Finalize GroupAggregate`, …) are plans where PG *removed* the sort
— via an index-ordered path, an incremental sort, or a GroupAggregate that was
already producing the order. goopg has none of those mechanisms above the seam:
`planner.go:1720` stacks an explicit full `Sort` **unconditionally**. So goopg's
`Limit → Sort(N)` count is higher than 39 and its N values are larger, and the
deciding number is not in this repository yet.

**This is the one number that decides whether C-13 is worth landing, and it is
unmeasured. Probe P2 (§9) takes it.** An honest reading of the PG census alone
would rank C-13 *last*; the reason it is still ranked first (§10) is that the
goopg census is the relevant one and every structural reason points to it being
much larger.

### 6.6 Gate and negative results

C-13a is gated like a pure executor change plus a timing claim:

- values: `tpch-runner -digest`/`-diff` PASS (unaffected corpus, run as a
  control), TPC-DS SF0.5 `PASS=95 MISMATCH=0 CKMISMATCH=0` — this is the real
  correctness gate, since a broken bound returns the *wrong k rows*, which the
  SF0.5 gate's checksum arm catches;
- shape: PP `changed=0` on both suites — C-13a moves no plan by construction;
- **timing: the TPC-DS sweep TOTAL arm plus per-query times for every query in
  Probe P2's list.** This is the claim. Fresh server per arm, distinct
  `GOOPG_CG_UNIT`, `GOGC=off` per `bench/tpcds/env_tpcds.sh`, server age held
  constant, ±17% band;
- regress: `WITH TIES`, `LIMIT ALL`, `OFFSET` without `LIMIT`, `LIMIT 0`,
  `ORDER BY … FOR UPDATE` (the ctid side-channel path), and a rescan
  (`Sort` under an NLI) each get a red-then-green fixture.

**Negative results:**

- *Probe P2 finds no goopg `Limit → Sort(large N)` beyond PG's two.* Then C-13's
  measurable win is Q22 and Q67 only, and the item should be **re-scoped to
  C-13b alone** (a correctness-of-the-cost-model item, landed with C-12, no
  timing claim) or deferred behind C-14 (Incremental Sort), which is what PG
  actually uses on Q67. This is a real possible outcome and the design says so
  in advance.
- *The TOTAL arm moves less than ±17%.* Report the per-query figures for the
  large sorts; if those individually beat the band the item is still a win
  reported honestly as a per-query one, per 09 §6 ("suite claims on totals,
  per-query only above band or on repeats").
- *A small-N query regresses.* Heap maintenance is `N log k`; with `k` close to
  `N` that loses to quicksort. PG's own guard is the branch condition
  `tuples > 2 * output_tuples || input_bytes > sort_mem_bytes`
  (costsize.c:1960) — copy it as the *executor's* gate too, not only the cost
  model's.
- *Checksum mismatch on the SF0.5 gate.* The bound is wrong (most likely
  `count` without `offset`, or a `WITH TIES` leak). Revert; this is a
  wrong-answer class, not a tuning class.

---

## 7. The parallel hazard, addressed per cut

The standing rule: goopg's cost model has no parallel dimension,
`MaybeAddGather` is a post-planning **size** rule, and `drivingScan`
(`parallel.go:474`) admits a narrow set of shapes — so a change that moves work
off a shape the post-pass can drive silently loses a 5-worker Gather. Three
individually-correct hash-join cost fixes each regressed TPC-H by 10–22%
this way (Q5 +444%). C-19d is landing the priced `PathGather` concurrently.

| cut | can it change which join is chosen? | argument |
|---|---|---|
| C-11 | **No** | inert; no producer, no consumer (§3.3) |
| C-12 | **No** | it prices a node that sits *above* the search root; no path below the seam changes cost, and `finalPath`/`getCheapestFractionalPath` are untouched. The Gather post-pass reads structure and sizes, never `PlanCost` (§5.3) |
| C-13a | **No** | executor-only; the plan is byte-identical |
| C-13b | **Not until C-12a** | it changes only the `ORDERED` rel's single path's cost; with one path in the list there is nothing to lose to |

**The one that must be re-verified rather than believed** is C-12's: it rests on
`sortPartialRootPays`, `terminatesPartial` and `splitAggregateIsProfitable`
containing no cost reader. That was true at `262d6484e`; C-19d is editing
`parallel.go`'s neighbourhood right now. Re-read all three before landing C-12,
and if any has gained a `PlanCost` reader, add a serial control arm
(`estimate-audit --serial`) to C-12's gate.

Conversely, **C-19d changes nothing for these three cuts** as long as
`PathGather` stays below the search root. If C-19d's `PathGather` ends up
competing at the *final* rel, then C-12's `ORDERED` rel sits above it and the
`PathPrebuilt` seed of §5.1 must be built from `finalPath()`'s winner, which is
already what `createPlanAtSearchRoot` consumes. No conflict either way; noted so
the two designs are known to compose.

---

## 8. Interaction with the other landed P4-00 scoping

- **C-10a**: the flat `Aggregate.GroupingSets [][]int` stays; `GROUP_AGG`
  carries no rollup list. C-11's enum includes `UpperGroupAgg` but C-11 lands it
  *empty*; C-15 fills it. Nothing in C-11..C-13 reads grouping sets.
- **C-10c**: the per-item re-assert table names `*Sort` for C-12 and `*Limit`
  for C-13 as the `pushSingleSideQualsIntoInnerJoinInputs` arms at risk. Since
  neither cut deletes its arm (§2.1), the obligation is discharged by a fixture
  asserting the arms are still reached with the upper-rel path in place — cheap,
  and it is the only guard, because C-10c's load-bearing finding is that goopg
  has no PlaceHolderVar and row counts never move when the guard breaks.
- **C-10d**: the boundary is treated as permanent (§1.3). The recorded costs to
  C-12 ("must always stack a full Sort") and C-13 ("an outer LIMIT cannot push a
  bound in") are accepted as-is by this design and are not worked around.

---

## 9. Probes required before implementation

Both are cheap and both settle something this document deliberately refuses to
guess.

**Probe P1 — what `AvgVarBytes` should be for an upper rel (§4.3).** Run TPC-H
Q18 and TPC-DS Q22 with the spill instrumentation the calibration design's §6.2
already asks for, and compare `hashsize.EntryBytes(ncols, avgVarBytes)` for the
three candidate `avgVarBytes` values (0, the search-root rel's, the aggregate
output's measured per-row bytes) against the **actual on-disk size of the sort's
spill files divided by its row count**. The measured ratio is the principled
answer; do not fit one to a suite time. Shares an arm with
`docs/design/planner-spill-cost-calibration/DESIGN.md` §6.2 — run them together.

**Probe P2 — the goopg `Limit → Sort(N)` census (§6.5).** Capture `EXPLAIN` for
all 99 TPC-DS SF0.5 queries against goopg (`scripts/tpcds-sf05-regression.sh
plans` already does the capture) and count, per query, `Limit` nodes whose
descent reaches a `Sort` with its estimated input rows. Compare against the PG
census in §6.5 (81 Limit-rooted / 39 Limit→Sort / 2 with N ≥ 10 000). Deliver:
the list of queries with N ≥ 10 000, and their current runtimes. **This list is
C-13a's timing gate and its go/no-go.**

Optional but recommended before C-12: **Probe P3 —** run one query with
`GOOPG_PGSHAPED_DP_TRACE=1` after wiring the `ORDERED` rel, and confirm two
`DPPATH path producer=upper.ordered.*` lines appear with `relids=-`. That is the
cheap standing answer to "verify both candidates were generated"; it costs one
run and it is what a past investigation into Q8 lacked.

---

## 10. Landing order and the ranking

**Recommended order: C-13a → C-11 → C-12 → C-13b**, with C-12a (C-07's map
union) filed separately after C-12.

C-13a is placed first because it has **no dependency on the other two**, carries
the whole runtime claim, and is the only one of the four that a user would
notice. If Probe P2 comes back thin, the whole item is re-scoped before any
planner structure is disturbed — which is the cheapest possible place to learn
that.

**Most likely to produce a measurable win: C-13a.** The reasoning, with its
weakness stated:

- It changes *executed work*, not prices. C-11 and C-12 change no executed work
  at all, so neither can produce a timing win by construction — C-12's value is
  that it makes a whole class of node visible to the cost model for the first
  time (F1), which is a correctness-of-the-model win that pays off in later
  items.
- The shape is common in the corpus that has it: 81 of 99 TPC-DS plans are
  `Limit`-rooted, and nearly every one ends `order by … limit 100`.
- goopg's sort is unusually expensive to run in full: interpreted per-key
  evaluation (mitigated but not removed by the precomputed-keyvals work,
  parallel-sort §9.1), 48-byte `Datum` rows, and a 256 MiB spill threshold. A
  bound of 100 does not merely change `N log N` to `N log k` — it can remove the
  spill entirely, which is the disk-branch-to-bounded-branch transition
  costsize.c:1930/1960 encodes.
- **The weakness:** F2 removes TPC-H from the picture entirely, and PG's own
  TPC-DS plans put a large sort under a Limit in only 2 of 99 queries. The
  argument depends on goopg's census being materially worse than PG's, which is
  structurally very likely (goopg always stacks a full Sort) but is
  **unmeasured**. Probe P2 is not optional.

**Candidate for re-scoping: C-13, into C-13a and C-13b.** They have different
dependencies (none vs C-12), different risk classes (wrong-answer vs
cost-column), different gates (timing vs plan-parity) and different sizes. 09
§5's sequencing rule — "one variable per commit; two-input items split before
start" — applies to this item literally.

**Candidate for demotion, not dropping: C-12.** It cannot produce a timing win
and, until C-12a lands, its `ORDERED` rel holds exactly one path, so it is
"structure plus a cost column". It should still land, and land *before* C-13b,
because it is the thing that makes goopg's sort cost model describe the sorts
goopg actually runs (F1) — but nobody should expect a number from it, and its
gate should be written to prove *absence* of movement rather than presence of a
win.

**Nothing here should be dropped.** C-11 is the enabling structure for
C-15/C-16/C-17/C-18 as well, and its cost is small and its risk near zero.

---

## 11. Out of scope, stated so it is a choice

- **C-14 Incremental Sort.** No executor counterpart exists; PG uses it on
  Q67 and Q3 and several others in the census above. Excluded from A3 closure
  until the executor lands, per take3 08 §7. If Probe P2 finds that goopg's
  large-sort-under-Limit queries are the same ones PG solves with an incremental
  sort, C-14 becomes the better item and C-13a becomes its prerequisite rather
  than its rival — say so in the P2 report.
- **C-12a** (widening `addOrderedIndexPaths`' useful-column set so ORDER BY
  motivates index paths). One line in `pathindexordered.go`'s `colExprs`; a
  separate commit because it is the first cut that can move a plan.
- **`create_ordered_paths`' partial-path arm** (planner.c:5399ff, sorting the
  cheapest partial path under a Gather Merge). Belongs with C-19e, which
  re-decides `Gather Merge → Sort → Parallel scan` by cost; and the committed
  measurement that leader-side sorting currently wins (q16 0.9 s vs 1.6 s, q10
  3.0 vs 3.4, q13 4.8 vs 5.1) is a permitted divergence under 09 §4.4 case 1.
- **`create_limit_path` as a real path** (pathnode.c:4118 +
  `adjust_limit_rows_costs`). C-13 stamps a bound onto an existing `*Limit`
  node; making `Limit` a priced path is C-13's successor and is only worth doing
  once `UPPERREL_FINAL` has more than one path in it.
- **Const-folding the LIMIT clause** so `LIMIT 5+5` and a bound `LIMIT $1` get a
  bound. Existing ledger row (2026-08-05); inherited, not widened.

---

## 12. Files

Read-only inputs (nothing in `internal/` is modified by this document):

| file | why |
|---|---|
| `internal/optimizer/path.go:244` | `RelOptInfo`; `:380` `ConsiderStartup`; `:703` `addPath`; `:487` `newPrebuiltPath` |
| `internal/optimizer/cost_funcs.go:267` | `costSortRun`, and `:254-259` the ledgered missing branch |
| `internal/optimizer/joinpathsmerge.go:430` | `sortPathFor`, the only `PathSort` producer |
| `internal/optimizer/createplansimple.go:148` | `createSortPlan` — the arm that already exists |
| `internal/optimizer/createplan.go:64` | `PathPrebuilt` / `:76` `PathSort` dispatch |
| `internal/optimizer/createplanroot.go:80` | `createPlanAtSearchRoot`, the seam |
| `internal/optimizer/relfromjoinlist.go:184` | `planJoinlistSearch` — returns a `Node` |
| `internal/optimizer/joinsearch.go:113,291,317` | `relMap`, `finalRel`, `finalPath` |
| `internal/optimizer/tuplefraction.go:75,97` | `limitEstimates`, `preprocessLimit` |
| `internal/optimizer/joinsearchseam.go:685` | `searchTupleFraction` — where `count`/`offset` are dropped |
| `internal/optimizer/querypathkeys.go:24-49` | why C-07's second half waits on C-11/C-12 |
| `internal/optimizer/plancost.go:116,143,184` | `DeriveLegacyDisplayCost` and its `*Sort` arm |
| `internal/optimizer/parallel.go:100,294,354,363` | `MaybeAddGather` and its three structural predicates |
| `internal/optimizer/planner.go:1720,1766,1806,1969` | the ORDER BY wraps, the LIMIT wrap, `liftLimitAboveLockRows` |
| `internal/executor/operators.go:790,885,898` | `sortOp`, `SortStat`, `sortChunkBytes` |
| `internal/testutil/tpch/tpch.go:110` | the 22 HammerDB strings — the F2 evidence |
| `bench/tpch/plans-pg/*.txt`, `bench/tpcds/plans-pg/*.txt` | the committed PG captures the censuses are taken from |

PG oracle (read-only, `./postgres`, 18.3):
`src/include/nodes/pathnodes.h:69` (`UpperRelationKind`), `:2340` (`SortPath`);
`src/backend/optimizer/util/relnode.c:1458` (`fetch_upper_rel`);
`src/backend/optimizer/util/pathnode.c:3221` (`create_sort_path`), `:4118`
(`create_limit_path`), `:4174` (`adjust_limit_rows_costs`);
`src/backend/optimizer/path/costsize.c:1898` (`cost_tuplesort`), `:2144`
(`cost_sort`);
`src/backend/optimizer/plan/planner.c:1451`, `:1625`, `:1852`, `:5308`
(`create_ordered_paths`);
`src/backend/executor/execProcnode.c:848` (`ExecSetTupleBound`);
`src/backend/executor/nodeLimit.c:423`, `:431` (`compute_tuples_needed`);
`src/backend/executor/nodeSort.c:102-124` (the bounded-sort consumer).
