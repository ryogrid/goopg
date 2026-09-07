# E-20 / E-21 — the parallel dimension below the aggregate, and the
# single-relation path search

Owner rows: `docs/design/not_ralph/minimize_datum/TODO_ALL.md` §7, **E-20** and
**E-21**, filed 2026-09-07. They are worked as ONE track: E-21 is E-20's
prerequisite.

**The owner's ruling that bounds this document.** Parallel cost calculation
must match PG 18.3. goopg will NOT implement its own. Everything below is
therefore a **transcription** plan, and every PG function named is cited by
`file:line` against the read-only oracle at `postgres/` (PG 18.3). Where a
goopg-original parallel cost or verdict function stands in the way, this
document says **drop**, never **extend**, and §3.3 inventories every one of
them.

Paths are repo-relative. PG citations are relative to `postgres/`, e.g.
`allpaths.c` is `postgres/src/backend/optimizer/path/allpaths.c`.

---

## 0. Verdict

**REVISED 2026-09-07, by measurement — see §1.1a.** E-21 is NOT small. Its
operative gate is `isSimpleSingle` in `planSelect` (`planner.go:1235, 1377`),
one frame above the seam every source pass examined, and closing it means
replacing the legacy rule-based access-method chooser for every single-table
statement in both corpora. Cut 1 (the seam floor) is landed, flagged and
**measured inert**; Cut 1b (the `isSimpleSingle` re-route) is scoped and not
built. The original verdict line, kept for the record:

**PROCEED on E-21, in two cuts, both PG-faithful and both small.**
**PROCEED on E-20 in one cut (partial merge join) and DEFER its second
(partial nested loop) behind an executor prerequisite it does not own.**

But **both rows are wrong as written**, in ways that change what gets built.
Three corrections, each verified against source in §1 and not absorbed
silently:

1. **E-21 names the wrong site.** `makeRelFromJoinlist`'s `len(items) == 1`
   return (`internal/optimizer/relfromjoinlist.go:357`) is NOT what stops a
   single-table statement, and there is already a carve-out immediately above
   it (`relfromjoinlist.go:213`, M0134-0188) that runs the one-relation search
   protocol precisely to defeat it. The operative gate is one layer out, in
   the seam: `joinsearchseam.go:230`, `if nrels < 2 { decline }`. A statement
   with one FROM item never reaches `planJoinlistSearch` at all.

2. **The PG citation both the row and the goopg code rely on does not say what
   they say.** `allpaths.c:3400-3405` is about the joinlist. Partial paths are
   created earlier and unconditionally, by `set_base_rel_pathlists` at
   `allpaths.c:221`, which runs BEFORE `make_rel_from_joinlist` at
   `allpaths.c:226` in the same function (`make_one_rel`). Joinlist length is
   irrelevant to partial-path creation, exactly as the row suspects — but the
   in-tree comment at `relfromjoinlist.go:693-694` also cites a condition,
   `bms_membership(root->all_baserels) != BMS_SINGLETON`, that **does not exist
   anywhere in PG 18.3's optimizer**. PG's real condition is
   `!bms_equal(rel->relids, root->all_query_rels)` (`allpaths.c:555-557`), and
   it means something materially different (§1.2).

3. **E-20 names a function that does not exist.** PG 18.3 has no
   `create_partial_join_paths`. The partial-join dispatch is inlined into the
   three strategy functions in `joinpath.c` (§2.4), and the merge-join half has
   **two independent producers**, not one: `sort_inner_and_outer` calls
   `try_partial_mergejoin_path` directly (`joinpath.c:1535-1545`, once per
   mergeclause ordering) AND `match_unsorted_outer` reaches it through
   `consider_parallel_mergejoin` → `generate_mergejoin_paths(is_partial=true)`
   → `try_mergejoin_path` (`joinpath.c:1029-1057`). Cut 3 must transcribe both
   (§4).

And one thing the rows are right about that matters more than any of it:
**no values gate can see either defect.** Both fail in the safe direction, and
`estimate-audit --serial` defaults TRUE with `plans-pg/` captured the same way,
so every "plans byte-identical" artifact in this repository is a serial control
arm that cannot support a claim about parallel plans (TODO_ALL row
`tooling: serial captures`). §6 is a parallel-mode measurement plan, not a
values plan.

---

## 1. What the two rows claim, checked against source

### 1.1 E-21's cited site is not the gate (CORRECTION)

The row says `makeRelFromJoinlist` "returns immediately when the joinlist has
one element, so `SELECT … FROM t WHERE …` never reaches path generation".

The return exists (`relfromjoinlist.go:357-362`) and does what the row says.
But it is not reachable for the statement class the row is about, and it is
already compensated for the class that does reach it:

- `planJoinlistSearch` (`relfromjoinlist.go:185`) calls `makeRelFromJoinlist`
  and then, at `relfromjoinlist.go:213`, re-runs the one-relation protocol
  when `jl.nrels() == 1` and the leaf is rebuildable:

  ```go
  if _, _, rebuildable := scanLeafFor(r.node); rebuildable && r.info.table != nil && jl.nrels() == 1 {
      r, err = prob.searchOneProblem([]joinlistRel{r}, prob.tupleFraction)
  ```

  This is M0134-0188 and its comment is explicit: returning the raw leaf
  "silently skips base-rel path generation". So the `len(items) == 1` return
  is ALREADY known-and-patched at the problem's own entry.

  The one-relation search protocol it invokes is not hypothetical: the bitmap
  toggle tests already drive it directly, building a `searchCtx` from a
  one-binding `buildInitialRels` (`enable_scan_methods_test.go:23-45`,
  `scanSearchWithCP`). Cut 1 does not have to make a one-rel search work; it
  has to stop declining to run one.

- The reason a plain `SELECT … FROM t WHERE …` still gets nothing is that it
  never reaches `planJoinlistSearch`. `tryPGShapedJoinSearch`
  (`joinsearchseam.go:216`) computes `nrels := len(ctx.bindings)` — the
  statement's FROM items — and declines at `joinsearchseam.go:230`:

  ```go
  if nrels < 2 || nrels > maxSearchRels || len(ctx.joinlist) == 0 {
      traceSeamDecline("size-or-no-joinlist", nrels, len(ctx.joinlist))
      return node, pred, false
  }
  ```

  with a header comment that states the belief being corrected here: *"One
  relation is not a search (`make_rel_from_joinlist` returns the item)"*.

  A second, narrower guard follows at `joinsearchseam.go:249`:
  `if nprefix < 2 && len(spine) == 0 { decline }` — a one-leaf prefix is
  searched only when an outer spine was peeled above it. That is the case
  `relfromjoinlist.go:213` exists to serve (TPC-H Q13's
  `customer LEFT JOIN orders`).

`planJoinlistSearch` has exactly one non-test caller
(`joinsearchseam.go:512`), so this is total: **`nrels == 1` ⇒ no
`searchCtx`, no `RelOptInfo`, no `Pathlist`, no `PartialPathlist`, no
`generateUsefulGatherPaths`.**

**Reviewer correction, folded in (§7.2 finding 11).** An earlier draft claimed
`addBaseRelPartialPaths` "reinforces" the decline with its own
`len(s.joinrels) < 2` return (`considerparallel.go:530`). It does not.
`newSearchCtx` (`joinsearch.go:222-233`) already accepts `nrels >= 1` and
allocates `joinrels` with `nrels+1` entries, so for a one-rel search
`len(s.joinrels) == 2` and that guard is already false. **The seam is the ONLY
thing keeping a one-relation statement out**, which makes Cut 1 smaller than
the draft assumed.

### 1.1a THIRD CORRECTION, found by measuring — the seam is not the first gate

**Added 2026-09-07 after Cut 1 was built, flagged and measured. Cut 1 as
designed in §1.1 is INERT, and the reason invalidates §1.1's "total" claim.**

Cut 1 lowered the seam floor to one FROM item and was measured on the TPC-H
SF=1 clone at `max_parallel_workers_per_gather = 4` (§6's method, private
datadir `/tmp/e20d`, port 5542, own cgroup unit). Three arms, on the two c19h
census probes plus a small-relation negative twin:

| arm | `select * from lineitem where l_extendedprice > 90000` |
|---|---|
| A0 — HEAD | `Gather (cost=0.00..62325.07) → Parallel Seq Scan` |
| B — `GOOPG_ONEREL_SEARCH=on` | **byte-identical to A0** |
| C — `=on` + `GOOPG_GATHER_PATHS=all` | **byte-identical to A0** |

`startup = 0.00` identifies all three as the `MaybeAddGather` post-pass's
Gather, not `cost_gather`'s (which adds `parallel_setup_cost = 1000` to
startup). So the path model produced nothing, in every arm.

The cause is not a cost decline. With `GOOPG_PGSHAPED_DP_TRACE=1` the
statement emits **no seam trace at all** — neither a decline nor a search
block — while a two-relation control query on the same server emits three
`DPTRACE` lines. The seam is never CALLED.

`planSelect` classifies the statement one layer above the seam
(`planner.go:1235`):

```go
isSimpleSingle := len(s.From) == 1 && (len(s.FromExprs) == 0 ||
    (len(s.FromExprs) == 1 && len(s.FromExprs[0].Joins) == 0))
```

and at `planner.go:1377` the WHERE arm branches on it: `if isSimpleSingle`
takes `planIndexScanFromWhere` — the legacy rule-based access-method chooser —
and returns; only the `else` reaches the `*Filter` arm that calls
`tryJoinSearch` (`planner.go:1470`). The `else if isSimpleSingle` at
`planner.go:1261` routes the FROM itself to `planScanRangeVar` for the same
class.

**So E-21's gate chain has three links, not one**, and §1.1 named only the
second:

1. `planner.go:1235` / `:1377` — `isSimpleSingle` diverts the statement to the
   legacy single-table planner before any seam exists. **This is the operative
   one.**
2. `joinsearchseam.go:230` — `nrels < 2`, which Cut 1 lowered. Necessary, not
   sufficient.
3. `relfromjoinlist.go:357` — the row's cited site, already compensated at
   `:213`.

**What this costs the row.** E-21 is not a floor change. Closing it means
routing `isSimpleSingle` statements through the path search *instead of*
`planIndexScanFromWhere`, i.e. replacing the rule-based access-method choice
for every single-table statement in both corpora with `add_path`. That is a
far larger change than either the row or §4's first draft assumed, and it is
the change §5.2 warned about — except that the warning understated it: the
legacy chooser is not merely bypassed, it is the *incumbent*, and the two
disagree by construction (`enable_scan_methods_test.go`'s header records that
"the rule-based legacy scan choice keeps its own declines … it has no cost
competition to express a preference in").

**Cut 1 is therefore landed as a PREREQUISITE, not as E-21's fix**, and is
honestly labelled inert: it removes link 2 so that link 1 can be attacked on
its own, and its own gate is that it changes nothing (arms B and C above).
Cut 1b — the `isSimpleSingle` re-route — is scoped in §4 and NOT built here.

**Method note.** This was found by measuring an arm that was expected to work
and then instrumenting the exit path, not by reading further. Three source
passes (two mapping, two adversarial) all read `joinsearchseam.go:230` as the
gate and none of them looked one frame up the call stack, because the question
they were asked was "is this the gate" rather than "is the seam reached". The
trace answered it in one line.

**Consequence for the fix.** Deleting or weakening the `len(items) == 1`
return would be the wrong change: it would alter the nested sub-joinlist
unwrap, where returning the leaf is correct because the ENCLOSING problem owns
that leaf's paths. The change belongs at the seam.

This correction is also consistent with what was already measured and
attributed to the wrong line. C-19d's DESIGN §5.1a records
`EXPLAIN select * from lineitem where l_extendedprice > 90000` byte-identical
across `GOOPG_GATHER_PATHS` off/all, and the c19h census
(`analysis/planner-refactor-take3/c19h-census-rerun-20260907/README.md` §2)
records the same query losing its Gather entirely when the post-pass is
removed, at `GOOPG_GATHER_PATHS=all`. Both observations are explained by the
seam decline; neither requires `relfromjoinlist.go:357`.

### 1.2 PG creates the base-rel partial path before the joinlist is looked at

`make_one_rel` (`allpaths.c:171-234`), in order:

| step | line | what |
|---|---|---|
| `set_base_rel_consider_startup` | `allpaths.c:178` | |
| `set_base_rel_sizes` | `allpaths.c:183` | per rel: `set_rel_consider_parallel` then `set_rel_size` |
| `total_table_pages` | `allpaths.c:199-215` | |
| **`set_base_rel_pathlists`** | **`allpaths.c:221`** | per rel: `set_rel_pathlist` → `set_plain_rel_pathlist` → **`create_plain_partial_paths`** |
| `make_rel_from_joinlist` | `allpaths.c:226` | the joinlist walk, `levels_needed == 1` at `:3400-3405` |

`make_rel_from_joinlist`'s single-node branch returns
`linitial(initial_rels)`, and `initial_rels` was filled from
`find_base_rel(root, varno)` (`allpaths.c:3383`) — a rel that already carries
its full `pathlist` AND `partial_pathlist`. **PG's `levels_needed == 1` return
skips a join search. It does not skip path generation, because path generation
already happened.** goopg's transcription of that one line inherited the
return without the preceding `set_base_rel_pathlists`, which is the whole
defect.

**The `BMS_SINGLETON` citation is wrong.** `relfromjoinlist.go:697-700` says
`generate_useful_gather_paths` runs "ONLY when the statement has more than one
base rel (`bms_membership(root->all_baserels) != BMS_SINGLETON`)". Grepped
across `postgres/src/backend/optimizer/`, `BMS_SINGLETON` appears at
`indxpath.c:4276`, `costsize.c:5696`, `analyzejoins.c:2024-2025`,
`clauses.c:1673`, `initsplan.c:2986` and `allpaths.c:898` — none of them a
gather gate (`allpaths.c:898` is the tablesample repeatable-scan check). PG's
actual condition, at `allpaths.c:555-557`:

```c
	if (rel->reloptkind == RELOPT_BASEREL &&
		!bms_equal(rel->relids, root->all_query_rels))
		generate_useful_gather_paths(root, rel, false);
```

with the comment at `allpaths.c:552-553`: *"Also, if this is the topmost
scan/join rel, we postpone gathering until the final scan/join targetlist is
available (see grouping_planner)."* The same predicate is applied per finished
join level in `standard_join_search` (`allpaths.c:3512-3518`, the predicate and its call at `:3517-3518`).

So PG's rule is **not** "more than one base rel". It is **"not the topmost
scan/join rel"** — and the topmost rel's gather is *deferred*, not skipped.
It is taken later, in `apply_scanjoin_target_to_paths` (`planner.c`), at two
sites:

- `planner.c:7880` — the early, forced one, when the final scan/join target is
  NOT parallel-safe; it then permanently clears `rel->partial_pathlist` and
  `rel->consider_parallel` (`planner.c:7883-7884`);
- `planner.c:8035-8036` — the normal one, after the final target has been
  applied to `pathlist` (`planner.c:7903-7921`) and to `partial_pathlist`
  (`planner.c:7924-7942`), immediately before `set_cheapest(rel)`:

  ```c
	if (rel->consider_parallel && !IS_OTHER_REL(rel))
		generate_useful_gather_paths(root, rel, false);
  ```

For a one-relation query the base rel IS the topmost scan/join rel, so **all**
of its gathering happens at `planner.c:8036`. That is the shape E-21 has to
land, and it is a different shape from "call `addBaseRelGatherPaths` for one
rel".

### 1.3 E-20's `create_partial_join_paths` does not exist (CORRECTION)

There is no such function in PG 18.3. `joinpath.c` inlines the guard-and-
dispatch into each strategy:

| strategy | guard | dispatch |
|---|---|---|
| merge (`sort_inner_and_outer`) | `joinpath.c:1429-1444` | `try_partial_mergejoin_path` **directly**, at `joinpath.c:1535-1545`, inside the per-ordering loop: `if (cheapest_partial_outer && cheapest_safe_inner) try_partial_mergejoin_path(...)` |
| nestloop + merge (`match_unsorted_outer`) | `joinpath.c:2015-2054` | `consider_parallel_nestloop` (`joinpath.c:2111`), `consider_parallel_mergejoin` (`joinpath.c:2071`) |
| hash (`hash_inner_and_outer`) | `joinpath.c:2415-2432` | `try_partial_hashjoin_path` (`joinpath.c:1299`), both `parallel_hash` values |

`consider_parallel_mergejoin` reaches `try_partial_mergejoin_path`
(`joinpath.c:1145`) indirectly, through
`generate_mergejoin_paths(..., is_partial=true)` →
`try_mergejoin_path` (`joinpath.c:1029-1057`), which dispatches to the partial
variant and returns.

**CORRECTION recorded (§7.1 finding 8).** An earlier draft of this document
asserted, in its own list of corrections to the row, that
`sort_inner_and_outer`'s parallel block computes `cheapest_partial_outer` /
`cheapest_safe_inner` and never uses them. That is FALSE. Reading the function
to its close (`joinpath.c:1357-1547`) shows the locals are set at
`joinpath.c:1437-1443` and consumed at `joinpath.c:1535-1545`. The false claim
came from a mapping pass and was repeated without checking — the same failure
mode the c19h census's own README records about a sub-agent summary. Both PG
call sites are live in production: `add_paths_to_joinrel` runs
`sort_inner_and_outer` as step 1 whenever mergejoin is allowed
(`joinpath.c:279-283`) and `match_unsorted_outer` as step 2. **Cut 3's scope is
widened accordingly**, and this correction is the single largest change the
reviews made to this document.

Transcribing a function named `create_partial_join_paths` would produce a
structure PG does not have. The plan in §4 follows PG's actual shape.

### 1.4 What E-20 gets right, and where its evidence now stands

E-20's mechanism — "the join shape is chosen on a serial cost and a Gather is
bolted on afterwards" — is correct and is confirmed by three prior rows. Two
of its three supporting facts have MOVED since the row was written, and the
design must not re-derive them wrongly:

- **C-19d's arithmetic (`parallel_tuple_cost` 0.1/row vs ~0.0075/row) has been
  superseded.** C-19d DESIGN §5.1a, "LANDED 2026-09-07": both the serial scan
  and its partial twin now resolve inputs through `baseSeqScanCostInputs`
  (`joinsearch.go:479`), so the CPU term counts tuples SCANNED
  (`baserel->tuples` over `baserel->pages`) while the Gather term counts rows
  CROSSING (`rel.Rows`) — PG's two row counts, and hence PG's crossover.
  `TestBaseRelGatherCannotWinAtAnySelectivity` was **inverted** into
  `TestBaseRelGatherCrossesOverOnASelectiveScan`. So "a base-rel Gather can
  never win" is no longer true, and E-21's fix lands on a cost model that can
  now price it. This is the single largest reason E-21 is worth doing now
  rather than earlier.
- **C-19f's crossover `N > 106,667 + 9.87*J` still stands** for the join-tree
  Gather, and C-19g's partial aggregation is the shipped default
  (`GOOPG_PARTIAL_AGG_PATHS=on`).
- **D-05 stays blocked and its mechanism is exactly E-20's.** The c19h census
  §5 reproduces it: an aggregate over a *merge* join gets no Gather even with
  `partialaggupper` live, because the hash-vs-merge choice happens at the join
  rel where the only partial-path producer is the hash one.

---

## 2. PG 18.3 reference — the transcription targets

Every function this work transcribes, with the citation the owner's ruling
requires. Bodies were read; the arithmetic below is the arithmetic to
reproduce.

### 2.1 `create_plain_partial_paths` — `allpaths.c:806-819`

```c
parallel_workers = compute_parallel_worker(rel, rel->pages, -1,
                                           max_parallel_workers_per_gather);
if (parallel_workers <= 0)
    return;
add_partial_path(rel, create_seqscan_path(root, rel, NULL, parallel_workers));
```

Call guard, `set_plain_rel_pathlist` `allpaths.c:794-795`:
`if (rel->consider_parallel && required_outer == NULL)`, placed between the
serial `add_path(create_seqscan_path(...))` (`allpaths.c:791`) and
`create_index_paths` (`allpaths.c:798`).

**Already transcribed in goopg**, faithfully, as `addBaseRelPartialPaths`
(`considerparallel.go:529`) + `addPartialSeqScanPath` (`considerparallel.go:656`).
Nothing to write. It is simply never called for a one-relation statement.

### 2.2 `compute_parallel_worker` — `allpaths.c:4274-4350`

Reloption override (`:4283-4284`); below-threshold return 0 for
`RELOPT_BASEREL` (`:4295-4298`); heap log-3 ladder from
`min_parallel_table_scan_size` (`:4300-4322`); index log-3 ladder from
`min_parallel_index_scan_size` (`:4324-4343`); **min** of the two when both
apply (`:4339-4343`); clamp to `max_workers` (`:4347`). Defaults, `guc_tables.c:3727-3746`:
`min_parallel_table_scan_size` = 1024 blocks (8 MB),
`min_parallel_index_scan_size` = 64 blocks (512 kB);
`max_parallel_workers_per_gather` = **2** (`guc_tables.c:3626-3634`).

**Already transcribed twice in goopg** — `computeParallelWorker`
(`considerparallel.go:601`, the path-model one, both arms, correct) and
`computeParallelWorkers` (`parallel.go:786`, the post-pass's independent
re-derivation off a live smgr block count). §3.3 lists the second as a drop
candidate; this work does not drop it, because the post-pass is still the only
producer for the non-aggregate root (§5.4).

**Recorded divergence, not changed here:** goopg's
`max_parallel_workers_per_gather` BootVal is **4**
(`internal/utils/misc/defaults.go:726`), deliberately, with the reason at
`cost_funcs.go:160` ("workers here are goroutines, not scarce slots"). It is a
GUC-default divergence from PG's 2 and it is orthogonal to both rows; it is
noted so that no A/B in §6 is read as a transcription error when goopg plans 4
workers where PG plans 2. Ledger, do not fix inside this track.

### 2.3 `cost_gather` / `cost_gather_merge` / `get_parallel_divisor` / `compute_gather_rows`

- `cost_gather` — `costsize.c:446-472`: `startup += parallel_setup_cost`;
  `run += parallel_tuple_cost * rows`.
- `cost_gather_merge` — `costsize.c:485-539`: `N = num_workers + 1`,
  `logN = LOG2(N)`, `comparison_cost = 2.0 * cpu_operator_cost`; heap creation
  `comparison_cost * N * logN` at startup; per-row `rows * comparison_cost *
  logN` plus `cpu_operator_cost * rows`; `parallel_setup_cost`; and
  `parallel_tuple_cost * rows * 1.05` — the explicit +5% IPC bump — the `* 1.05` at `costsize.c:533`, its comment at
  `costsize.c:526-531`.
- `get_parallel_divisor` — `costsize.c:6474-6500`: `divisor = parallel_workers`;
  if `parallel_leader_participation`, add `max(0, 1.0 - 0.3 * parallel_workers)`.
- `compute_gather_rows` — `costsize.c:6625-6630`: `path->rows * divisor`.
- `parallel_setup_cost` = 1000.0, `parallel_tuple_cost` = 0.1
  (`optimizer/cost.h:29-30`).

**Already transcribed in goopg**: `gatherCost` (`cost_funcs.go:669`),
`gatherMergeCost` (`cost_funcs.go:705`, with `gatherMergeIPCFactor` at `:719`),
`getParallelDivisor` (`cost_funcs.go:175`), `computeGatherRows`
(`cost_funcs.go:739`). Nothing to write.

### 2.4 The three `try_partial_*` producers — `joinpath.c`

| function | line | required-outer rules | costing |
|---|---|---|---|
| `try_partial_nestloop_path` | `joinpath.c:950-1021` | outer must be unparameterised (assert); inner's required-outer must be a subset of the outer rel's top-parent relids | `initial_cost_nestloop` → `add_partial_path_precheck(joinrel, disabled_nodes, total_cost, pathkeys)` → `add_partial_path(create_nestloop_path(..., required_outer=NULL))` |
| `try_partial_mergejoin_path` | `joinpath.c:1145-1214` | outer AND inner both fully unparameterised | `pathkeys_count_contained_in` for `outer_presorted_keys` → `initial_cost_mergejoin` → precheck → `add_partial_path(create_mergejoin_path(...))` |
| `try_partial_hashjoin_path` | `joinpath.c:1299-1343` | outer AND inner both fully unparameterised; extra `bool parallel_hash` | `initial_cost_hashjoin(..., parallel_hash)` → precheck with `NIL` pathkeys → `add_partial_path(create_hashjoin_path(..., parallel_hash, ...))` |

`add_partial_path_precheck` (`pathnode.c:924-925`) takes no `startup_cost` — partial paths do not keep
a cheap-startup variant.

The worker count of a partial JOIN path is not derived at all: all three
`create_*_path` constructors set `parallel_workers = outer_path->parallel_workers`
verbatim (`pathnode.c:2734, 2800, 2866`), each carrying upstream's own comment
*"a foolish way to estimate parallel_workers, but for now…"*. Transcribe the
foolishness; it is the ruling.

### 2.5 The two `consider_parallel_*` drivers — `joinpath.c`

- `consider_parallel_mergejoin` — `joinpath.c:2071-2097`. For each path in
  `outerrel->partial_pathlist`: `build_join_pathkeys` → `generate_mergejoin_paths(
  root, joinrel, innerrel, outerpath, jointype, extra, /*useallclauses=*/false,
  inner_cheapest_total, merge_pathkeys, /*is_partial=*/true)`.
- `consider_parallel_nestloop` — `joinpath.c:2111-2206`. Optionally
  materialises the cheapest total inner (`create_material_path`) unless
  `JOIN_UNIQUE_INNER` / `!enable_material` / inner not parallel-safe / inner
  parameterised by outer / inner already materialises; then for each partial
  outer × each `innerrel->cheapest_parameterized_paths` entry (skipping
  non-parallel-safe inners) calls `try_partial_nestloop_path`, plus a memoize
  variant and the materialised-inner variant (`joinpath.c:2186-2205` — a range
  an earlier draft's citation excluded; §7.1 finding 7).

Both are called from `match_unsorted_outer` under the shared guard
(`joinpath.c:2015-2054`):

```c
if (joinrel->consider_parallel &&
    save_jointype != JOIN_UNIQUE_OUTER &&
    save_jointype != JOIN_FULL &&
    save_jointype != JOIN_RIGHT &&
    save_jointype != JOIN_RIGHT_ANTI &&
    outerrel->partial_pathlist != NIL &&
    bms_is_empty(joinrel->lateral_relids))
```

`hash_inner_and_outer`'s guard (`joinpath.c:2415-2432`) is the same list plus
`JOIN_RIGHT_SEMI`.

### 2.6 `generate_gather_paths` / `generate_useful_gather_paths` — `allpaths.c:3098-3144`, `3235-3342`

`generate_gather_paths`: return if `partial_pathlist == NIL`; one plain Gather
over `linitial(partial_pathlist)` with `rows = compute_gather_rows(...)`; one
Gather Merge per partial path with non-NIL pathkeys.
`generate_useful_gather_paths`: that, plus a sort (full or incremental) of a
partial path up to each entry of `get_useful_pathkeys_for_relation`
(`allpaths.c:3167-3223`) followed by a Gather Merge.

**goopg has the first half** (`generateUsefulGatherPaths`, `gatherpaths.go:139`);
the sort half is C-19e's and is behind `GOOPG_PARTIAL_SORT_PATHS` (default
off). Neither row owns it. Not in scope.

### 2.7 `add_partial_path` — `pathnode.c:798`

Same fuzzy-dominance sweep as `add_path` but with **no parameterisation
comparison** (partial paths are asserted unparameterised), comparing only
`disabled_nodes`, `total_cost` and `pathkeys`. Keeps the list cost-ordered, so
`linitial` is always the cheapest — which is why `generate_gather_paths` can
index `[0]` without searching.

**Already transcribed** as `addPartialPath` (`path.go:934`) with
`addToPartialPathlist`.

---

## 3. goopg's current state

### 3.1 What already exists and is faithful

| goopg | PG | file:line |
|---|---|---|
| `parallelModeOK`, `setBaseRelConsiderParallel`, `relConsiderParallel`, `joinrelConsiderParallel` | `set_rel_consider_parallel` (`allpaths.c:589`), `build_join_rel`'s propagation (`relnode.c:829-845`), `is_parallel_safe` (`clauses.c:706`) | `considerparallel.go:60, 73, 124, 379` |
| `addBaseRelPartialPaths` + `addPartialSeqScanPath` | `create_plain_partial_paths` (`allpaths.c:806`) | `considerparallel.go:529, 656` |
| `computeParallelWorker` (both arms) | `compute_parallel_worker` (`allpaths.c:4274`) | `considerparallel.go:601` |
| `addPartialIndexPath` | `build_index_paths` partial arm (`indxpath.c:1039-1062`) | `pathindexordered.go:275` |
| `addPartialHashJoinPath` | `try_partial_hashjoin_path(parallel_hash=false)` (`joinpath.c:1299`) + `hash_inner_and_outer`'s guard | `joinpathsparallel.go:80` |
| `generateUsefulGatherPaths`, `makeGatherPath`, `makeGatherMergePath` | `generate_gather_paths` (`allpaths.c:3099`), `create_gather_path` (`pathnode.c:1974`), `create_gather_merge_path` (`pathnode.c:2020`) | `gatherpaths.go:139, 184, 214` |
| `gatherCost`, `gatherMergeCost`, `getParallelDivisor`, `computeGatherRows` | `cost_gather`, `cost_gather_merge`, `get_parallel_divisor`, `compute_gather_rows` | `cost_funcs.go:669, 705, 175, 739` |
| `addPartialPath` | `add_partial_path` (`pathnode.c:798`) | `path.go:934` |

**The cost model this work needs is already written.** Neither row requires a
new cost function or a new constant, which is what makes the owner's
transcription ruling satisfiable rather than aspirational.

### 3.2 What is missing

1. **No partial path for a one-relation statement** — the seam declines
   (§1.1). E-21.
2. **No `try_partial_mergejoin_path`, no `try_partial_nestloop_path`, no
   `consider_parallel_mergejoin`, no `consider_parallel_nestloop`.** The merge
   arms (`sortInnerAndOuter`, `matchUnsortedOuterMerge` — `joinpaths.go:333,
   340`) and the nested-loop arms (`addNestLoopPath`, `addNLIPaths` —
   `joinpaths.go:361, 371`) have no partial sibling; only the hash arm does
   (`joinpaths.go:359`). E-20.
3. **No deferred top-rel gather.** goopg has no analogue of
   `apply_scanjoin_target_to_paths`'s `generate_useful_gather_paths`
   (`planner.c:8036`). `addBaseRelGatherPaths` (`gatherpaths.go:384`) covers
   `set_rel_pathlist`'s site and `joinsearchlevel.go:325` covers
   `standard_join_search`'s; the third site has no counterpart.
4. **`GOOPG_GATHER_PATHS` defaults `off`**, so items already built (C-19c/d/f)
   are inert in production. Every partial path is priced and none is read.

### 3.3 goopg-original parallel costing — the drop inventory

The owner's ruling: found in scope, it is dropped, not extended. Exhaustive
list, so that a later reader does not have to re-derive it:

| # | function | file:line | why it is goopg-original |
|---|---|---|---|
| 1 | `splitAggregateIsProfitable` (+ `groupsToRowsRatio`, `aggColumnStats`) | `parallel_agg.go:282, 204, 253` | five hand-picked constants (`cXfer=2.0, cTrans=1.0, cHash=0.25, cMerge=4.0, cOut=1.0`), none a PG cost constant; its own header says "calibrated against one query" |
| 2 | `sortPartialRootPays` | `parallel.go:474` | a single hard-coded `*IndexScan`/`*IndexOnlyScan` special case from one TPC-H A/B; its own comment says it "needs a cost model goopg's parallel post-pass does not have" |
| 3 | `computeParallelWorkers` (post-pass twin) | `parallel.go:786` | an independent re-derivation off a live smgr block count; PG has no post-pass and therefore no counterpart |
| 4 | `findPartialSubtree`'s placement heuristic | `parallel.go:334` | "push the Gather as low as possible by construction" — no PG cost comparison backs it |

**Neither E-20 nor E-21 drops any of them, and this document argues that
dropping them now would be a PG-parity REGRESSION.** (1) and (2) are already
superseded-when-enabled by priced tournaments (`partialaggpaths.go:448`,
`partialsortpaths.go:165`) and survive only as `off`-mode fallbacks. (3) and
(4) belong to `MaybeAddGather`, which the c19h census measured as the ONLY
producer for the non-aggregate root: removing it turns
`select * from lineitem where l_extendedprice > 90000` from
`Gather → Parallel Seq Scan` (which is what vanilla PG 18.3 emits) into a plain
`Seq Scan`, at every `GOOPG_GATHER_PATHS` setting. E-21 is the change that
makes their retirement *possible*; it is not itself their retirement. That
sequencing is stated here so the ruling is honoured without buying a
regression: **drop nothing until E-21 has landed and a parallel-mode capture
shows the path model producing the shape the post-pass produces today.**

---

## 4. The design

### Cut 1 (E-21a) — admit the one-relation statement into the path search

**Change.** In `tryPGShapedJoinSearch`, replace the `nrels < 2` decline
(`joinsearchseam.go:230`) with `nrels < 1`, and relax the prefix guard at
`joinsearchseam.go:249` from `nprefix < 2 && len(spine) == 0` to
`nprefix < 1`. Everything downstream already handles the one-rel case: the
`jl.nrels() == 1` carve-out at `relfromjoinlist.go:211` runs
`searchOneProblem` on it, and that runs the full base-rel protocol
(`buildInitialRels` → `setBaseRelConsiderParallel` → `addBaseRelPartialPaths`
→ `addBaseRelIndexPaths` → `addBaseRelGatherPaths` → `joinSearch` with
`nrels == 1`, which enumerates nothing → `finalPath`).

**No other guard needs touching.** `addBaseRelPartialPaths`'s
`len(s.joinrels) < 2` (`considerparallel.go:530`) is a bounds check on
`joinrels[1]`, not a rel count, and it already passes for a one-rel search
(`joinrels` has `nrels+1 == 2` entries — `joinsearch.go:222-233`). An earlier
draft of this section said it "must become `len(s.joinrels) < 2`", which was a
drafting slip the source review caught (§7.2 finding 11); the correct statement
is that it needs no change at all.

**PG justification.** `set_base_rel_pathlists` (`allpaths.c:221`) runs for
every base rel unconditionally, before the joinlist is consulted
(`allpaths.c:226`). §1.2.

**This is the risky cut, and the risk is not parallelism.** Admitting
single-table statements to the path search changes *access-method selection*
for every one-table query in both corpora — seq vs index vs index-only now
decided by `add_path` instead of by the legacy rule-based planner. That is a
much larger blast radius than a Gather. It is also, precisely, what
M0134-0188's comment says the carve-out exists to deliver, and TPC-DS is full
of one-table statements. **Mitigation: land Cut 1 behind
`GOOPG_ONEREL_SEARCH` (default off), measure, then flip in a separate commit
with its own gate run.** Do not bundle the flip with the mechanism.

### Cut 1b (E-21a, second half) — route `isSimpleSingle` through the search

**NOT BUILT. Scoped here because §1.1a showed Cut 1 alone cannot close E-21.**

**Change.** In `planSelect`, stop diverting a one-FROM-item statement to the
legacy single-table planner: the `isSimpleSingle` arms at `planner.go:1261`
(FROM) and `planner.go:1377` (WHERE) must fall through to the `*Filter` arm
that calls `tryJoinSearch` (`planner.go:1470`), under the same
`GOOPG_ONEREL_SEARCH` knob. Cut 1 has already made the seam accept what
arrives.

**What it displaces.** `planIndexScanFromWhere` — the rule-based chooser that
picks the access method for every single-table statement today. After Cut 1b
that choice is `add_path`'s, made among the paths
`addBaseRelIndexPaths` / `addBaseRelPartialPaths` file. This is PG's
arrangement (`set_plain_rel_pathlist`, `allpaths.c:768-799`) and it is also
the single largest plan-shape change in this track by a wide margin.

**Why it is not built here.** It needs its own measured round: the TPC-DS
corpus is dominated by single-table statements, the two choosers disagree by
construction, and no values gate can see an access-method regression — only a
timing and plan-shape A/B can. Landing it inside E-21's mechanism commit would
bundle a large, unmeasured plan change with a change whose whole gate is that
it changes nothing.

**Prerequisites already discharged**: Cut 1 (seam floor);
`relfromjoinlist.go:213`'s one-relation protocol; `newSearchCtx`'s `nrels >= 1`
support; and `addBaseRelPartialPaths` filing a partial path on a one-rel
`searchCtx` (`TestOneRelSearchFilesAPartialPath`).

### Cut 2 (E-21b) — the deferred top-rel gather

**Change.** `addBaseRelGatherPaths` (`gatherpaths.go:384`) currently gathers
every base rel in a `len(s.joinrels) >= 2` search. Give it PG's predicate
instead: gather a rel iff its `Relids` is NOT the whole problem
(`relLevel(rel.Relids) != s.nrels`, the same test `gatherPathsMode ==
gatherPathsTop` already applies at `gatherpaths.go:154`), and add a single new
call site for the topmost scan/join rel, after `finalPath` has chosen and
before the upper-rel chain reads it.

**PG justification.** `allpaths.c:555-557` skips the topmost scan/join rel;
`planner.c:8035-8036` takes it after the final scan/join target is applied.
§1.2.

**Interaction with the existing `top` mode.** `gatherPathsTop` already means
"only the final search rel", which is the OPPOSITE of PG's `set_rel_pathlist`
rule and the SAME as PG's `apply_scanjoin_target_to_paths` rule. So `top` is
already a partial, accidental transcription of the deferred site. Cut 2 makes
it the real one and makes `all` mean PG's two sites together, which is what
`all`'s header comment (`gatherpaths.go:52-54`) already claims it is.

**For a one-relation statement, Cut 2 is the only gather site that fires**, and
it fires with the rel's `Relids` equal to the whole problem — exactly PG.

### Cut 3 (E-20a) — `try_partial_mergejoin_path`, at BOTH of PG's call sites

**Change.** New producer `addPartialMergeJoinPath`, transcribing
`try_partial_mergejoin_path` (`joinpath.c:1145-1214`), offered at the two
places PG offers it — this is the scope widened by §7.1 finding 8:

| PG site | PG line | goopg site |
|---|---|---|
| `sort_inner_and_outer`, inside the per-mergeclause-ordering loop, directly | `joinpath.c:1535-1545` | inside `sortInnerAndOuter`'s `for front := range groups` loop (`joinpathsmerge.go:249`), beside the serial offer |
| `consider_parallel_mergejoin` ← `match_unsorted_outer` | `joinpath.c:2071-2097` | beside `matchUnsortedOuterMerge` (`joinpaths.go:340`) |

A first draft scoped only the second. Offering only there would leave the
sorted-both-sides shape — which is precisely the shape that wins when a hash
join's cost rises, i.e. D-05's shape — without a partial path, and Cut 3 would
then fail to remove the blocker it exists to remove.

At each site: build the merge pathkeys and offer a partial merge
join against the inner's cheapest unparameterised parallel-safe total path
(`cheapestParallelSafeTotalInner`, `joinpathsparallel.go:218`, already
written). Cost via the existing `mergeJoinCost`, with the outer's per-worker
`Rows` and the inner's whole `Rows` — the same asymmetry
`addPartialHashJoinPath` already implements and the same one
`final_cost_mergejoin` implements. `Path.Rows = clampRowEst(joinrel.Rows /
divisor)` — PG's own rule, `final_cost_mergejoin`'s *"For partial paths, scale
row estimate"* stanza at `costsize.c:3875-3881`, the merge-join sibling of the
`costsize.c:4307-4313` stanza `addPartialHashJoinPath` already transcribes (the
third is `final_cost_nestloop`'s at `costsize.c:3377-3383`);
`ParallelWorkers = outer.ParallelWorkers` (`pathnode.c:2800`).

**One deliberate divergence, matching the existing hash twin.** PG's
`consider_parallel_mergejoin` loops over the WHOLE `outerrel->partial_pathlist`
(`joinpath.c:2077`). goopg's join arms are driven from `outer.CheapestTotal`
(`joinpaths.go:302`) and `addPartialHashJoinPath` correspondingly reads only
`outer.PartialPathlist[0]` (`joinpathsparallel.go:104`), the cheapest — which
`addToPartialPathlist` keeps at the front, as `add_partial_path` does. Cut 3
follows the sibling, not upstream, because a producer that enumerated more
outers than every other arm in `addPathsToJoinrel` would change the search's
shape for a reason unrelated to parallelism. Recorded, not hidden.

**Why this is E-20's payload.** D-05's structural finding, restated by the
c19h census §5: raising a hash-join cost term flips the plan to a merge join,
and the merge join has no partial path, so the whole plan goes serial and the
correct cost fix reads as a +10..22% regression. With Cut 3 the hash-vs-merge
comparison at the join rel is made with both candidates able to carry
parallelism, which is the exact condition E-20 says is missing. **It does not
by itself unblock D-05** — D-05 must be re-derived afterwards — but it removes
D-05's stated blocker.

**Executor prerequisite, and it is already scoped.**
`attachParallelScan` (`internal/executor/parallel_scan.go:112`) fails closed on
any `joinOp` whose `Algo != JoinAlgoHash` (`parallel_scan.go:156`), and its
comment names this exact work:

> A partial merge join (`try_partial_mergejoin_path`, joinpath.c:1218) is a
> real PG shape and goopg has no producer for it; when one is written, this
> arm gains a `JoinAlgoMerge` case that descends the OUTER (left) side
> explicitly, together with its own serial-vs-parallel identity test.

Three planner-side twins must gain the same arm, or the path is priced and
then refused (or worse, admitted and mis-executed):
`partialPathDrivingKind` (`gatherpaths.go:322`), `drivingScan`
(`parallel.go:594`) and `stampParallelScan` (`parallel.go:~523`) — the
sibling-agreement rule those files state about themselves.

Semantics: each worker merge-joins ITS partition of the outer against the
WHOLE inner, which it builds independently. That is PG's partial merge join
exactly, and it is why `final_cost_mergejoin` does not divide the inner's
cost.

### Cut 4 (E-20b) — `try_partial_nestloop_path` — **DEFERRED, with reason**

Transcribing `joinpath.c:950-1021` and `joinpath.c:2111-2206` is no harder
than Cut 3, but its executor prerequisite is not scoped anywhere:

- goopg's most valuable nested-loop shape is `*NestedLoopIndexJoin`, a
  distinct node listed in `terminatesPartial` (`parallel.go:483`) as a node a
  Gather must sit at or below. Admitting it needs a per-worker story for the
  inner index probe that nothing has written.
- `consider_parallel_nestloop` iterates `innerrel->cheapest_parameterized_paths`
  (`joinpath.c:2160`), and goopg's parameterised-path story at the join rel is
  C-08's `param_source_rels` derivation, landed but *provably inert until
  C-04*.
- `create_material_path` — the materialised-inner variant
  (`joinpath.c:2130-2145`) — has no goopg path kind.

**Deferring is the honest call**: Cut 4 would be a producer whose paths every
downstream whitelist refuses, i.e. dead code with a cost function attached.
Ledger row: `e20-partial-nestloop-deferred-on-executor`.

---

## 5. Feasibility, stated plainly

### 5.1 What pays

E-21 is the parity gap the c19h census isolated and could not close: vanilla
PG 18.3 emits `Gather → Parallel Seq Scan` for
`select * from lineitem where l_extendedprice > 90000`, goopg emits it only
through a post-pass whose retirement is blocked on exactly this. After
C-19d §5.1a's landing the cost model can price it correctly, so E-21 is now
mechanism-only.

Cut 3 removes D-05's stated blocker, which is worth more than any plan it
moves by itself: three *correct* cost fixes are currently unbankable.

### 5.2 What might not

Cut 1's blast radius is access-method selection, not parallelism, and it is
large. If the one-relation search regresses TPC-DS (which is full of
single-table statements) the flag stays off and E-21 delivers nothing but a
measured negative — **which is a valid outcome and must be reported as one.**

Cut 3 may move zero plans. C-19f's crossover `N > 106,667 + 9.87*J` is a real
bar and a partial merge join is not obviously cheaper than the partial hash
join it competes with. Its value is structural (D-05), not necessarily a
timing win, and the report must not claim one it did not measure.

### 5.3 What is out of scope, explicitly

- `parallel_hash = true` (PG's cooperative build). goopg's shared prebuild
  (`prebuildSharedHashJoins`, E-09a/E-09b) is a different and, for memory, a
  better mechanism; `joinpathsparallel.go:37-58` states the structural refusal.
- C-19e's sort half of `generate_useful_gather_paths`.
- Retiring `MaybeAddGather` (C-19h). §3.3.
- goopg's `max_parallel_workers_per_gather` = 4 default. §2.2.

### 5.4 The ordering constraint

`MaybeAddGather` stands down whenever the tree already carries a Gather
(`subtreeHasGather`, `parallel.go:220`). So Cut 2 firing on a one-relation
statement will SUPPRESS the post-pass for that statement. If Cut 2's cost
verdict differs from the post-pass's size rule, the plan changes even though
both produce "a Gather". **Every arm in §6 must therefore compare against the
post-pass shape, not merely count Gathers.**

---

## 6. Measurement

The usual gate is blind here by construction. `estimate-audit`'s `--serial`
defaults TRUE and sets `max_parallel_workers_per_gather = 0`
(`cmd/estimate-audit/main.go:285, 355-360`), and `bench/tpch/plans-pg/` was
captured through the same function — **zero Gathers on either side**. A
"plans byte-identical" result from those captures cannot support any claim
about a parallel plan.

**Method**, following
`analysis/planner-refactor-take3/q9-parallel-plans-20260907/README.md`: both
engines at `max_parallel_workers_per_gather = 4`, read back from the live
servers; goopg on a private SF=1 clone of the TPC-H data dir, a private 55xx
port, its own `GOOPG_CG_UNIT`, started through `scripts/goopg-test-run.sh`;
PG 18.3 reference on :65432, db `tpch`. `estimate-audit -serial=false`.

**Arms.**

| arm | build | what it answers |
|---|---|---|
| A0 | HEAD, parallel mode | the parallel baseline this tree does not have for 22 queries (only Q9 exists) |
| A1 | HEAD + Cut 1 off | A/A control — must be byte-identical to A0 |
| B | Cut 1 on | access-method blast radius of the one-relation search; the number to watch is plan MOVES, not Gathers |
| C | Cuts 1+2 on | does a one-relation statement now get PG's Gather, by cost, at PG's crossover? |
| D | Cuts 1+2+3 on | does the hash-vs-merge comparison move at the join rel? |
| PG | PG 18.3, same settings | the oracle |

**The witness queries E-21 owns are not in TPC-H at all** — all 22 are
aggregate-rooted. The direct probes are the c19h census's two:
`select * from lineitem where l_extendedprice > 90000` and
`select l_orderkey from lineitem where l_shipdate < '1992-02-01'`, plus their
negative twins (a small relation, and a `LIMIT 1` shape) to show the crossover
is PG's in BOTH directions — the row's own gate wording.

**Gates before any commit** (per CLAUDE.md and the row):
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`;
`scripts/tpch-spotcheck.sh` (canonical Q12=2 / Q13=35); values on both corpora;
TPC-DS SF0.5 `PASS=95` all-zero; `-race`; `make plan-gate`. Never `-count=1`.

**Bench isolation.** Peers are live. Private clone + 55xx port + own
`GOOPG_CG_UNIT`; never `goopg stop -D` a datadir this work did not start;
never `pkill -f goopg` (it self-matches the invoking shell, exit 144).

---

## 7. Review record

Four subagent passes ran against this document: two MAPPING passes that
produced the raw material (one over `internal/`, one over `postgres/`), and
then the two ADVERSARIAL reviews the owner's process requires, both against
the drafted document rather than against the code. Their corrections are
recorded INLINE where they apply and summarised here. Nothing was absorbed
silently, including the corrections that falsified this document's own claims.

### 7.1 Adversarial oracle pass (PG 18.3) — verdict **REQUEST-CHANGES**, addressed

- **Finding 8, the material one, ACCEPTED and re-verified by hand.**
  `sort_inner_and_outer` DOES call `try_partial_mergejoin_path`
  (`joinpath.c:1535-1545`); this document's earlier claim that it computes two
  locals it never uses was false, and it had been promoted into §0's list of
  corrections to the row. §1.3 now records the correction in place and **Cut 3's
  scope is widened to both call sites**. Verified independently against
  `joinpath.c:1429-1444` (the locals being set) and `joinpath.c:1531-1546` (the
  call) before accepting.
- **Cut 2's target site VINDICATED.** The reviewer traced a plain
  `SELECT * FROM t WHERE x > c` end to end: `grouping_planner` sets
  `current_rel = query_planner(...)` (`planner.c:1654`) and calls
  `apply_scanjoin_target_to_paths` **unconditionally** at `planner.c:1773` —
  not gated on `have_grouping` — so the one-relation Gather is produced at
  `planner.c:8036`. Independently confirmed here. This was the single
  question that could have invalidated Cut 2, and it did not.
- **`BMS_SINGLETON` refuted** (already folded into §1.2 before this review, by
  the oracle mapping pass): the symbol appears nowhere in PG 18.3's optimizer
  as a gather gate. This refuted the hypothesis this document was first written
  around.
- **Systematic citation drift**, 1-8 lines nearly everywhere and 3-22 lines in
  four places (`compute_parallel_worker`'s reloption line, `consider_parallel_
  nestloop`'s range, `cost_gather_merge`'s 1.05 line, the `pathnode.c` triple).
  Every PG citation in this document has since been re-derived mechanically
  from the source rather than by hand; §2's ranges and every `pathnode.c` /
  `costsize.c` / `joinpath.c` anchor were replaced.
- **Cost-division asymmetry CONFIRMED**: nothing in `final_cost_mergejoin`
  divides the inner's rows or run cost by the parallel divisor; only the
  joinrel's output rows are scaled (`costsize.c:3875-3881`). Cut 3's costing
  rule stands.
- `add_partial_path`'s dominance rule and `add_partial_path_precheck`'s
  signature (no `startup_cost`) CONFIRMED at `pathnode.c:798` and `:924-925`.

### 7.2 Adversarial source pass (`internal/`) — verdict **APPROVE-WITH-NITS**, addressed

- **§1.1's central claim SURVIVED a deliberate falsification attempt.** The
  reviewer searched for a second `RelOptInfo` producer and found that
  `newRelOptInfo` has exactly two call sites (`joinsearch.go:394` in
  `buildInitialRels`, `joinsearchlevel.go:601` for join rels), both reachable
  only through the search the seam gates; that `planJoinlistSearch` has exactly
  one non-test caller; and that `ctx.bindings` is one entry per FROM
  `RangeVar` (`planner.go:2895`, `planFromRangeVars`) with no inflation path.
  **There is no alternative route by which a single-table statement acquires a
  `Pathlist` or a `PartialPathlist`.**
- **Finding 11, a real drafting error, FIXED.** An earlier §4 Cut 1 said
  `addBaseRelPartialPaths`'s guard "must become `len(s.joinrels) < 2`" — the
  expression it already has. Worse, §1.1 had cited that guard as
  *reinforcing* the decline. It does not: `newSearchCtx`
  (`joinsearch.go:222-233`) accepts `nrels >= 1` and allocates `nrels+1`
  entries, so the guard already passes for a one-rel search. **The seam is the
  only thing keeping a one-relation statement out**, which makes Cut 1 smaller
  than the draft claimed. Both passages rewritten.
- **Cut 1's downstream sufficiency CONFIRMED by trace, not by assertion**:
  `buildInitialRels`, `finalRel`/`finalPath` (`joinsearch.go:291-303, 317`),
  `relLevel`'s bit arithmetic and `stampNeededColsOnRels` are all generic in
  `s.nrels`; `joinSearch`'s level loop (`joinsearchlevel.go:303-327`) simply
  does not execute for `nrels == 1`; and `setCheapest` (`path.go:1124`)
  recomputes from scratch at every path-adding site, so no stale
  `CheapestTotal` hazard exists on the one-rel path. No panic and no silent
  no-op was found.
- **Cut 2's "no existing site" claim CONFIRMED**: the `PathGather`
  constructions in `partialaggupper.go:181, 261` and `partialsortpaths.go:274`
  all sit above an aggregate or a sort, never over the raw topmost scan/join
  rel.
- **Citation drift** in ten places, largest `parallel_agg.go:186 → 204`,
  `:236 → 253` and `joinpathsparallel.go:207 → 218`. All corrected in §2-§4.
- Every GUC-default claim CONFIRMED exactly: `GOOPG_GATHER_PATHS` off,
  `GOOPG_PARTIAL_AGG_PATHS` on, `GOOPG_PARTIAL_SORT_PATHS` off,
  `max_parallel_workers_per_gather` BootVal `"4"` at
  `internal/utils/misc/defaults.go:726`.

### 7.3 What the reviews did NOT settle

Neither review can answer whether Cut 1 regresses access-method selection, or
whether Cut 2's crossover agrees with PG's on a real relation. Those are
measurements (§6), and no amount of source reading substitutes for them.

---

## 7.4 What the measurement changed, after the reviews

Both adversarial reviews passed on §1.1's gate claim — one confirmed it after
deliberately trying to falsify it. Neither was wrong about what it read; both
answered "is `joinsearchseam.go:230` the gate?" and neither asked "is the seam
reached at all?". A one-line trace answered the second question and moved the
gate one frame up the call stack (§1.1a).

Recorded as a method finding, not as a complaint about the reviews: **a
source-falsification pass over the site a design names cannot find a gate the
design does not name.** The arm that found it was an arm expected to succeed.

---

## 8. Open questions this document does not answer

1. Does the one-relation search change access-method selection on TPC-DS, and
   in which direction? Cut 1's flag exists to find out.
2. After Cut 2, does goopg's crossover for a one-relation Gather agree with
   PG's on the two census probes AND on their negative twins? If it fires where
   PG does not, the fix is in the scan's inputs (C-19d §5.1a's
   `baseSeqScanCostInputs`), not in `cost_gather`.
3. Does Cut 3 move any plan at SF=1? If not, it is banked as structural
   (D-05's blocker removed) and reported as moving nothing.
