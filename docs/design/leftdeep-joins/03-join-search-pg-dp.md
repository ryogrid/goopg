# 03 — The Join Search: a `standard_join_search` Analogue with All Three PG Phases

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-08-02 |
| PG oracle | `postgres/src/backend/optimizer/path/allpaths.c:3457` (`standard_join_search`), `:3352` (`make_rel_from_joinlist`); `postgres/src/backend/optimizer/path/joinrels.c:73` (`join_search_one_level`), `:118` (`make_rels_by_clause_joins`), `:141-198` (phase 2 — bushy), `:200-256` (phase 3 — last-ditch), `:350` (`join_is_legal`), `:696` (`make_join_rel`), `:1066` (`have_join_order_restriction`); `postgres/src/backend/optimizer/path/joinpath.c:124` (`add_paths_to_joinrel`); `postgres/src/backend/optimizer/plan/initsplan.c:39-40` (collapse limits) |
| replaces | `enumerateBushyPlans` (`internal/planner/bushy.go:722`), `enumerateSubsets`/`enumerateSplits` (`:976`/`:994`), the `dp map[uint16]dpEntry` table (`:741`), `reorderCommaFromByCardinality` as an ordering authority (`internal/planner/joinorder.go:83` — demoted to over-ceiling fallback, §7), MHJ packing entirely, and `rewriteJoinsToNLI` **as decision authority** for searched joins (it remains the constructor — §5.2) |

## 1. Architecture

The search is PG's, all three phases:

```
planSelect
  └─ joinSearch(root *searchCtx) *RelOptInfo          // standard_join_search analogue
       joinrels[1] = buildInitialRels(root)            // one RelOptInfo per base rel, pathlist populated
       for lev := 2; lev <= n; lev++ {
           joinSearchOneLevel(root, lev)               // PG's three phases, verbatim (§4):
                                                       //   phase 1: (lev-1) ⋈ initial rels
                                                       //   phase 2: (k) ⋈ (lev−k) composite pairs, k ≥ 2
                                                       //   phase 3: last-ditch clauseless pass
           for each rel in joinrels[lev] { setCheapest(rel) }
       }
       return joinrels[n][0]                           // exactly one final rel
  └─ createPlan(root, finalRel.cheapestTotal)          // Path → planner.Node
```

- `joinrels [][]*RelOptInfo` — level-indexed lists, PG's `root->join_rel_level`
  (`standard_join_search`, allpaths.c:3475-3496). Lookup by relset stays a
  `map[RelSet]*RelOptInfo` beside the lists (PG's join_rel_hash equivalent).
  `RelSet` remains `uint16` (`internal/planner/path.go:29`); the 16-rel
  ceiling is above the search limit (§7).
- **The substrate is already in-tree and test-covered**: `RelOptInfo`
  (`path.go:104`), `Path` (`:67`), `addPath` (`:284`, dominance +
  `comparePathCostsFuzzily` with `stdFuzzFactor = 1.01`, `:142`),
  `setCheapest` (`:319`), `PathKey`/`pathkeysContainedIn`
  (`internal/planner/pathkeys.go`). This bundle promotes it from
  test-only (`pathgen.go` callers) to the live path.
- `createPlan` (`internal/planner/createplan.go:25`) grows real arms
  (PathHashJoin/PathNestLoop/PathMergeJoin/PathSeqScan/PathIndexScan/
  PathSort/PathAgg…) replacing the `PathPrebuilt`-only panic body. It emits
  the **existing** `planner.Node` types — no executor IR change.

## 2. Initial rels (level 1)

`buildInitialRels` produces one `RelOptInfo` per FROM item that today reaches
`tryBushyDP`'s leaf whitelist, **plus** the classes the whitelist currently
excludes (`bushy.go:116-123` allows only SeqScan/IndexScan/MHJ leaves —
subqueries and VALUES disable the DP entirely; ledger rows M0125-0034/-0036):

| initial rel class | pathlist content |
|---|---|
| base table | seqscan path; index paths per usable index (`generateScanPaths`, `pathgen.go:21`, extended); parameterised index paths for NLI (§5.2, discipline in §9) |
| subquery / CTE / VALUES / function scan | exactly one `PathPrebuilt` wrapping the already-planned subtree, rows/cost from its estimate — PG's `set_subquery_pathlist` analogue at our fidelity level |

The pinned unnest spine (`runJoinSearchBelowPinned`,
`internal/planner/predp.go:73`) is **not** an initial rel: the search is
spliced *below* it and the spine consumes the search's output — the spine's
re-resolution machinery survives and consumes the boundary map of §10.

Local single-table quals are attached to the initial rel (today's
`baseRelInfo`, `internal/planner/cardinality.go:259`) so `rows` is
post-filter — the current behaviour, kept. This closes the "DP never sees a
CTE leaf" gap without designing subquery costing here (rows come from the
subtree's own estimate).

## 3. Edges, quals, and connectivity

The join-clause bookkeeping generalises today's `joinEdge` list
(`bushy.go:40`) into a `restrictInfo` list per clause, PG-style:

- each equality/qual spanning ≥ 2 rels gets a `relids RelSet` and, when it is
  a canonical `l = r` cross-rel equality, hash/merge key operands;
- `inferAnchoredEqualities` (`internal/planner/equiv_class.go:236`) keeps
  producing derived clauses, tagged inferred (selectivity treatment moves to
  [04](04-cost-and-cardinality.md) §5 — no admissibility penalty);
- `hasRelevantJoinClause(joinrel, baserel)` = ∃ clause whose relids intersect
  BOTH sides — PG's `have_relevant_joinclause` (`joininfo.c:39`), which is two
  `bms_overlap` tests and **not** a coverage test. *(Corrected at P5.2: this
  bullet previously read "and are covered by their union", which is a
  different rule. A three-rel qual `a.x = b.y + c.z` makes `a ⋈ b` relevant
  upstream even though the qual cannot be evaluated there — it is the "are
  these two worth joining" heuristic, not a placement test. Requiring coverage
  here would push that pair onto the cartesian/last-ditch path and enumerate
  differently from PG.)*
- the coverage rule is a **separate** predicate, `clausesFor(outer, inner)` —
  PG's `build_joinrel_restrictlist` (`relnode.c`): `required_relids` must be a
  subset of the join's relids and must touch both sides. It answers "which
  quals does this joinrel apply", which is §5.4's qual placement, and it is
  what §5's selectivity work reads.

## 4. `joinSearchOneLevel` — PG's three phases, all implemented

PG's `join_search_one_level` (`joinrels.c:73`) has three phases; goopg
implements **all three**, PG-verbatim in structure:

### 4.1 Phase 1 — clause joins against initial rels

```
for each old := joinrels[lev-1]:
    for each base := joinrels[1]:
        if old.relids ∩ base.relids ≠ ∅: continue
        if hasRelevantJoinClause(old, base) || joinOrderRestricted(old, base):
            makeJoinRel(root, old, base)     // finds-or-creates joinrels[lev] entry,
                                             // then addPathsToJoinrel both directions
        else if hasNoJoinClauseAtAll(old):   // PG's per-rel clauseless else-branch,
            makeJoinRel(root, old, base)     // joinrels.c:120-137 — cartesian, every level
```

Mirrors `make_rels_by_clause_joins` (`joinrels.c:118`, `:280`) exactly.
`joinOrderRestricted` is **reserved, constant false in v1** — see §4.4.
The clauseless else-branch is PG's own (`joinrels.c:120-137`): a rel with
no join clause or restriction at all is crossed in eagerly at every level
(so a 1-row disconnected dimension can join at level 2, not only at the
end), costed honestly. At level 2, PG starts the inner loop after the
current rel to avoid duplicate pairs (`joinrels.c:113-116`); goopg mirrors
that.

### 4.2 Phase 3 — the last-ditch fallback (PG-verbatim)

PG's rule (`joinrels.c:200-256`): if a level came up empty (possible only
when join-order restrictions starved it), retry phase 1 without the clause
requirement. PG notes it never considers bushy here either
(`joinrels.c:215-216`).
The `elog(ERROR)` contract must be quoted **with PG's condition**: PG errors
on a still-empty level only when `join_info_list == NIL && !hasLateralRTEs`
(`joinrels.c:252-255`) — with special joins in the mix, an empty level can
be *legal* (see §4.4). goopg v1: since special joins never enter the search
(§4.4), an empty level after last-ditch is a planner bug and errors; if a
future change lets restrictions in, the required behaviour is **fall back to
syntactic shape for the whole search problem, never error**.

### 4.3 Phase 2 — bushy joins: IMPLEMENTED

PG pairs composite rels of sizes (k, lev−k), 2 ≤ k ≤ lev−2
(`joinrels.c:141-198`). goopg implements the loop verbatim:

```
for k := 2; k <= lev-k; k++ {          // PG: for (k = 2;; k++) { other_level = level-k;
                                       //      if (k > other_level) break; }
    for each old := joinrels[k]:
        if old has no join clauses and no restrictions: continue   // joinrels.c:170-172
        first := 0
        if k == lev-k: first = index(old) + 1                     // mirror-image rule, :174-177
        for each new := joinrels[lev-k], starting at first:
            if old.relids ∩ new.relids ≠ ∅: continue
            if hasRelevantJoinClause(old, new) || joinOrderRestricted(old, new):
                makeJoinRel(root, old, new)                       // :190-194
```

Keyed details, all from `joinrels.c:141-198`:

- The k-loop runs only to the halfway point (`k ≤ lev−k`): `make_join_rel`
  is symmetric, so pairing past halfway would duplicate work; at the
  halfway level the `first_rel` offset skips already-considered mirror
  pairs (`:153-157`, `:174-177`).
- Rels with no join clauses (and, later, no restrictions) are skipped
  entirely (`:170-172`) — the phase exists for connected pairs only, which
  is what keeps the search space from exploding (§7).
- The pair condition is `have_relevant_joinclause` **or**
  `have_join_order_restriction` (`:190-191`); v1 has `joinOrderRestricted`
  constant false (§4.4), so the effective condition is the connecting-clause
  test.

No data-structure or costing change is needed over phase 1:
`RelOptInfo`, `makeJoinRel`, and `addPathsToJoinrel` are shape-agnostic — a
composite input is just another RelOptInfo with a pathlist. `RelSet`
remains `uint16`, unchanged.

**Why this phase is load-bearing in PG**: with SpecialJoinInfo ordering
restrictions, some level-N joins have *no legal left-deep extension at
all* — PG's own comment documents accepting failure at level 4 and
recovering with a bushy plan at level 5 (`joinrels.c:234-251`; shape:
`(A JOIN B) LEFT JOIN (C JOIN D)`). With phase 2 in place, goopg has the
structural half PG relies on for that recovery; the remaining half —
`join_is_legal` constraint inference — is what §4.4's pin awaits before
letting restrictions into the search.

### 4.4 v1 pinning rule for special joins (temporary, until `join_is_legal` inference)

In v1, any outer/semi/anti join construct whose right-hand requirement spans
more than one base rel — and, in v1, every outer join, full stop — stays a
**pinned opaque input**: its subtree plans exactly as today and enters the
search (if at all) as a `PathPrebuilt` initial rel. Only INNER-joinable
comma-FROM / flattened-INNER-JOIN rels are searched. This is the coherence
condition that keeps §4.2's error contract sound.

This pin is a **temporary measure, not a design commitment**: PG admits
outer/semi/anti joins into the DP and governs them with `join_is_legal`
ordering-constraint inference over `SpecialJoinInfo` entries
(`joinrels.c:350`). The bushy phase (now implemented, §4.3) is
precisely the machinery PG relies on to make restriction-constrained levels
plannable — its "accept failure at level 4, recover bushy at level 5"
recovery (`joinrels.c:234-251`) is available to goopg. The missing
prerequisite is the inference itself: until `join_is_legal`-equivalent
legality checks land, pinned joins cannot be searched, because an
unconstrained search could emit an illegal join order (e.g. crossing an
outer join's boundary). When the inference lands, the pin relaxes to PG's
actual scope: join-order restrictions enter the search via
`joinOrderRestricted` (`have_join_order_restriction`, `joinrels.c:1066`),
and §4.2's error contract becomes PG's condition-only form. The staged
follow-ups that depend on this — §6 outer-join restriction inference and
[07](07-other-join-operators.md) §5 semi/anti in-DP — are therefore
**implementable now that the bushy phase exists**; what blocks them is the
constraint inference, not the search shape.

`makeJoinRel` also enforces the PG printing convention: the join's inputs
appear as the emitted `Join` node's children in the path's outer/inner
order; input-order variants surface as `BuildLeft`/outer-inner path
attributes, not tree re-shapes ([02](02-plan-shape-contract.md) §2).

## 5. `addPathsToJoinrel` — methods live INSIDE the search

For each `(outer, inner)` pair the search admits — `(composite, base)` from
phase 1, `(composite, composite)` from phase 2 — generate paths (analogue
of `joinpath.c:124`, restricted to goopg's operator inventory). Both sides
of a bushy pair go through the same path generation: a composite input is
just a RelOptInfo with a pathlist, and `addPathsToJoinrel` tries both input
orders for every jointype it supports:

### 5.1 Hash join paths
- `(outer streams, build inner)` — the default pipelining shape; cost =
  `hashJoinCost` (`internal/planner/cost_funcs.go:104`) with the work_mem/
  nbatch model from [06](06-hash-spill-and-memory.md) §5.
- `(build outer)` = `BuildLeft` variant, INNER joins only until
  [07](07-other-join-operators.md) §3 lands — PG's `hash_inner_and_outer`
  commutation (`joinpath.c:2220`).
- Multi-column keys: **all** usable equality clauses become the key set
  ([05](05-executor-pipeline-rework.md) §5); remaining clauses are the
  residual, costed as per-tuple quals.

### 5.2 Nested-loop-index paths
NLI enters the pathlist directly: for each inner-rel index matching a join
clause, a parameterised path (cost = `nestloopCost` + `indexProbeCost`,
`cost_funcs.go:129`/`:165`; parameterisation discipline in §9), plus the
Memoize-wrapped variant when the outer key has low distinct count (PG's
`get_memoize_path` analogue). This replaces cost-delegation-by-cloning
(`nliCostDelegation` probing `tryBuildNLI` on a throwaway clone,
`bushy.go:654-665`) *for searched joins*; `rewriteJoinsToNLI`
(`internal/planner/nl_index_join.go:78`) remains the constructor invoked from
`createPlan`, so there is still exactly one place that builds the operator
(doc-12 §4's "one predicate, one constructor" rule, preserved). Its rewrite
role on non-searched joins (explicit-JOIN trees under the collapse limit,
subquery interiors) is unchanged.

**Binding contract (no silent method substitution).** `tryBuildNLI` has many
decline paths (`nl_index_join.go:300-407`: join-type gauntlet, Filter-unwrap
eligibility, expression forms). Path generation MUST call the **same**
eligibility function construction uses — an NLI path may exist in a pathlist
only if `tryBuildNLI` is guaranteed to accept it at `createPlan` time.
Constructor failure for a DP-chosen path is a **loud planner error**, never a
fallback to another method (that would re-create the costed ≠ executed bug
class this architecture exists to kill). `nliEnabled` /
`enable_nestloop_index` act at **path generation only** for searched joins —
never as construction-time declines.

**Status (P5.4b-ii-b-1, 2026-08-04).** The arm exists:
`addNLIPaths` (`internal/planner/joinpathsnli.go`) iterates
`inner.CheapestParameterized` and prices the rescan from the parameterised
inner PATH's own cost, which is PG's `cost_rescan` for an index scan and the
reason PG moves this pair out of the plain-nestloop arm (joinpath.c:1874)
instead of costing it there. Two things named above are NOT in it and are
P5.4b-ii-b-2's: **Memoize**, and the **binding contract** — both need a built
`*Join` Node, since `tryBuildNLI` analyses one, so they attach to P5.5's
`createPlan` arms rather than to path generation. The half of the contract that
IS expressible on paths landed in P5.4b-ii-a: index eligibility goes through
`pickIndexCoveringAllLeadingColumns`, the constructor's own function.

One further gap is deliberate and sized rather than forgotten: PG admits a
still-parameterised JOIN path in the star-schema case
(`allow_star_schema_join`), and such a path needs its own `ppi_rows` from
`get_parameterized_joinrel_size` (costsize.c:5473). goopg's joinrel sizer is
P5.6's, so the arm admits only fully-discharged parameterisations for now. That
restriction buys an invariant the rest of the search leans on: **every join path
in the search is unparameterised**, so `Path.Rows == Rel.Rows` holds for every
join path and the only parameterised paths in play are base index scans.

### 5.3 Merge join / plain nested loop
- Merge path when both sides can be sorted on the key (explicit Sort paths;
  pathkey propagation via the existing `pathkeys.go`); required for
  RIGHT/FULL until hash-fill lands, then costed on equal footing
  ([07](07-other-join-operators.md) §2–3).
- Plain NL as the fallback **for the jointypes it supports** — PG generates
  nestloops only for INNER/LEFT/SEMI/ANTI (`joinpath.c:1833-1852`
  `nestjoinOK`; RIGHT/RIGHT_SEMI/RIGHT_ANTI/FULL are excluded). For a FULL
  join with no hashable or mergeable clause there is genuinely no path, and
  the user-visible contract is PG's error: *"FULL JOIN is only supported
  with merge-joinable or hash-joinable join conditions"*
  (`joinrels.c:961-964`). Within supported jointypes NL is always generated
  so a path always exists; usually dominated and pruned by `addPath`.

**Status (P5.4c-i, 2026-08-04).** PG has TWO merge arms and they need different
things, so P5.4c was split along that seam. The one that landed is
`sort_inner_and_outer` (joinpath.c:1357) — `joinpathsmerge.go`: it takes the two
cheapest-total paths, sorts BOTH, and needs nothing from the inputs, so it is
expressible in full against the search as it stands. It brings with it the
per-equivalence-class sort-key reduction (`select_outer_pathkeys_for_merge`,
pathkeys.c:1697-1704 — two clauses of one class sort on ONE column and both stay
merge clauses), the pair-local key orientation (the same clause faces the other
way when the sides swap, exactly as `isKeyableFor`'s split does), the
one-path-per-ordering loop (:1447-1466), the outer's ordering as the join's
output ordering (`build_join_pathkeys`, pathkeys.c:1295), and the
still-parameterised refusal (:1073-1081 — merge has no `allow_star_schema_join`
escape, so P5.4b-ii-b-1's "every join path is unparameterised" invariant holds
unchanged). `PathSort` finally has a producer: goopg makes the Sort a real child
path rather than PG's `MergePath.outersortkeys` field, which is the same plan at
the same cost and is what §5.3 asks for by name.

The sort-SKIP branch (`try_mergejoin_path` :1091-1097) is written and tested but
unreachable from the live search, because no path in the search carries pathkeys
yet. That is the deliberate boundary: `generate_mergejoin_paths`
(joinpath.c:1564), inside `match_unsorted_outer`, is the arm that exploits an
already-ordered outer and truncates the mergeclause list to find a cheaper
inner — it needs ordered index paths to exist first, and is **P5.4c-ii**, along
with the jointype gauntlet and the FULL error contract above (both still
unreachable while §4.4 pins every non-INNER construct outside the search).
Landing the consumer first means P5.4c-ii adds a producer rather than both
halves of an untested interface.

**Status (P5.4c-ii-a, 2026-08-04) — the producer is TWO things, not one.**
`build_index_pathkeys` (pathkeys.c:740) landed as `pathkeysindex.go`, and
`addOneParameterizedIndexPath` now records the order its index delivers, so
`addPath`'s pathkey dimension (`comparePathkeysDim`) stops being a constant
`dimEqual` and a better-ordered index path survives a cheaper rival — which is
what PG does, since `build_index_paths` (indxpath.c:750-800) hands the SAME
`useful_pathkeys` to the parameterised path and to the plain one.

The finding is what that does NOT buy. `addMergeJoinPath` refuses a
parameterised path outright (`try_mergejoin_path` :1073-1081), and every ordered
path goopg now has is parameterised, because goopg builds index paths only from
join clauses. An ordered merge OUTER therefore still does not exist. PG's other
half is a plain index path created for its ORDERING ALONE — `build_index_paths`
emits one whenever `useful_pathkeys != NIL` even with no index clauses at all —
and that needs `cost_index` (costsize.c:520) with the correlation model, not
`paramIndexScanCost`, which prices one bound probe and is calibrated off
`indexProbeCost`. Deriving a second index cost model inside a pathkey slice is
the "two cost models inside one comparison" failure [04](04-cost-and-cardinality.md)
§1 forbids, so the split is now **P5.4c-ii-a** (this, the ordering function),
**P5.4c-ii-b** (unparameterised ordered index paths over `cost_index`) and
**P5.4c-ii-c** (`generate_mergejoin_paths` itself).

One representational note that binds the rest of the chain: goopg's pathkeys are
syntactic ([04](04-cost-and-cardinality.md) §2.1), so an index pathkey must BE
the `*ColumnRef` the query's own clauses carry — a re-synthesised, same-named
ColumnRef has a different `Index`/`SourceTableIdx` and `exprEqual` reads it as a
different column. `buildIndexPathkeys` therefore takes the column expressions
from its caller (`paramIndexClause` now keeps the inner operand beside the
column name) instead of manufacturing them, and P5.4c-ii-b's unparameterised
path must find the same expressions from the rel's own binding rather than
inventing them.

**Status (P5.4c-ii-b, 2026-08-04) — the merge outer now exists.**
`pathindexordered.go` builds `build_index_paths`' `useful_pathkeys != NIL` arm
(indxpath.c:750-800): for each base rel, an index path with the index's
ordering, **no index quals, and an empty `RequiredOuter`**. That last property
is the whole slice — it is the only shape `try_mergejoin_path` (:1073-1081)
will accept, so the sort-skip branch P5.4c-i landed as an unreachable consumer
finally has a producer. It is priced by `costIndexScan`, a real `cost_index`
tied to the existing probe calibration
([04](04-cost-and-cardinality.md) §1.1), never by `paramIndexScanCost`.

Two things about the gate are worth recording, because both look like
shortcuts and are not:

- **`has_useful_pathkeys` reduces to its join-clause arm.** PG's other two arms
  read `root->group_pathkeys` and `root->query_pathkeys`; the search boundary
  carries neither (§10), so a rel with no join clause produces nothing — which
  is also what `truncate_useless_pathkeys` would reduce it to.
- **Building the ordering and truncating it are ONE loop, exactly.**
  `pathkeys_useful_for_merging` (pathkeys.c:2166) scans left to right and
  BREAKS at the first key with no mergeable clause, because a merge join can
  only exploit a prefix; `buildIndexPathkeys` scans the index's columns left to
  right and breaks at the first column absent from the map it was handed
  (PG's own STOP-not-skip rule, pathkeys.c:815-822). Handing it a map whose
  keys are exactly the merge-useful columns makes the two breaks the same
  break. goopg could not separate them even if it wanted to: with syntactic
  pathkeys there is no equivalence-class object to name a column no clause
  mentions.

What remains for **P5.4c-ii-c** is unchanged: `generate_mergejoin_paths` inside
`match_unsorted_outer`, with the mergeclause-list truncation and the
materialize-inner decision.

**Status (P5.4c-ii-c, 2026-08-04) — P5.4c is CLOSED.**
`joinpathsmergeouter.go` lands `generate_mergejoin_paths` (joinpath.c:1564) and
the merge half of `match_unsorted_outer` (:1998-2013), wired into
`addPathsToJoinrel` at PG's arm-2 position — after `sort_inner_and_outer`, before
the hash arm, so a merge over an already-ordered outer wins an exact tie against
a hash path exactly as it does in PG.

The arm iterates `outer.Pathlist`, and that is not incidental. An ordered index
path is by construction NOT the cheapest total (P5.4c-ii-b: `indexCorrelationFor`
returns 0, so it prices at `max_IO_cost` and survives `addPath` only on its
pathkeys), so a version of this arm keyed to `CheapestTotal` would find nothing
at all. Three behaviours are transcribed because each changes which plan wins:

- **The mergeclause list is a PREFIX of the outer's ordering, and stops.**
  `find_mergeclauses_for_outer_pathkeys` (pathkeys.c:1631) walks the outer's
  pathkeys and ends at the first with no clause, because a merge consumes its
  input in sort order and cannot skip a leading column. An outer sorted
  `(x, y)` joined only on `y` is therefore unusable — not usable on `y`.
- **The clause list is TRUNCATED to reach a cheaper inner** (:1685-1782), on
  BOTH cost axes, under PG's strictly-cheaper rule. The rule is not an
  optimisation: a shorter key prefix demotes a merge clause to a per-tuple
  qual, so a prefix must buy something to be worth generating.
- **The result carries the outer's FULL ordering**, not the merge keys
  (`merge_pathkeys` = `build_join_pathkeys` of `outerpath->pathkeys`). A merge
  on `(x)` over an outer sorted `(x, y)` emits an `(x, y)`-ordered result that a
  merge above it can consume — the compounding effect the arm exists for. This
  is why `tryMergeJoinPath` now takes the result ordering separately from the
  two sort-key lists.

Two findings, both of which changed code rather than only documentation:

- **A truncated merge must DEMOTE its dropped clauses to residual.** PG carries
  the whole restrictlist to plan time and `create_mergejoin_plan` subtracts the
  mergeclauses to get the qpqual, so the demotion is automatic there. goopg
  decides the key/residual split in path generation (§5.4), so a dropped merge
  clause would have been evaluated by nothing — a wrong answer, not a slower
  plan. `demoteDroppedMergeClauses` is that subtraction, and running it through
  `qualEvalCost` is also what puts a price on the trade the strictly-cheaper
  rule is weighing.
- **One outer sort key can owe SEVERAL inner sort keys**, which P5.4c-i's
  one-inner-key-per-group model could not express. `a.x = c.x AND a.x = c.y` is
  one outer key and two inner ones; both clauses stay merge clauses, so an inner
  sorted only by `c.x` would be handed to an operator comparing on `(c.x, c.y)`.
  PG carries `outersortkeys`/`innersortkeys` as independent lists precisely so
  they may differ in length (`make_inner_pathkeys_for_merge`, pathkeys.c:1858,
  and `find_mergeclauses_for_outer_pathkeys`' note at :1670-1674).
  `mergeInnerSortKeys` is now the single builder used by BOTH merge arms, so the
  siblings cannot drift.

**The materialize-inner decision has no goopg analogue, and that is a finding
rather than an omission.** PG's mergejoin executor rewinds the inner with
mark/restore, so `final_cost_mergejoin` (costsize.c:3986-4040) must decide
whether to interpose a Material node — mandatorily when the inner is used
unsorted and its node type cannot mark/restore. goopg's merge executor never
rewinds: `mergeJoinStream.bufferGroup`
(`internal/executor/join_merge_stream.go:616`) consumes each inner equal-key
group into memory, spilling past `work_mem` to an overflow file, and replays from
there. The materialisation PG chooses per PLAN is already made per GROUP,
unconditionally, in the executor. Two consequences: any presorted inner path is
consumable here regardless of its kind (no `PathMaterial` is introduced, and one
would double-buffer), and the COST of that buffering — PG's `rescanratio` term
for duplicate inner groups, plus the group file — is charged by nothing.
`mergeJoinCost` prices one pass over each input. That is ledgered against the
cost work rather than approximated, because inventing a rescan factor without
`mergejoinscansel`'s duplicate estimate would move plans on a guess.

**Status (P5.5-e-ii-a, 2026-08-04) — `create_mergejoin_plan`, and why goopg's
arm DELETES the Sort nodes PG's arm creates.** `createMergeJoinPlan`
(`internal/planner/createplanjoin.go`) is the plan-time counterpart of the two
arms above, and it inverts PG's central move. PG's `create_mergejoin_plan`
(createplan.c:4444) MATERIALISES a Sort from `outersortkeys`/`innersortkeys`,
because `nodeMergejoin` requires sorted inputs and cannot produce them. goopg's
`JoinAlgoMerge` operator sorts BOTH inputs itself, unconditionally, into
`work_mem`-bounded runs (`openMergeJoin`,
`internal/executor/operators_join_agg.go:315`) — it is a Sort⋈Sort in one node.
So the explicit `PathSort` children this section's arms build (`sortPathFor`,
which exist so `addPath` can compare a candidate that needs a sort against one
that does not) must be ABSORBED at plan time, not emitted: emitting them would
sort each side twice, a cost `tryMergeJoinPath` never charged and a plan no
producer asked for. `absorbMergeSort` steps over the `PathSort` and emits its
child, which is coordinate-neutral because a sort passes its `outputLayout`
through unchanged.

The absorption is only sound while the merge operator's own ordering is the one
the path promised, and goopg's is FIXED: `mergeSortedSource.less`
(`internal/executor/join_merge_stream.go:280`) compares ascending with
NULL-keyed rows last. The arm therefore refuses a result pathkey or an absorbed
sort key that is descending or nulls-first — unreachable from
`sort_inner_and_outer`, whose keys are ascending by construction, and a standing
guard for P5.4c-ii's ordered index paths, which are the first producer that can
offer a merge input ordered any other way. Ledgered: a P5.4c-ii outer that is
already ordered gets no `PathSort` at all, so the operator re-sorts it and the
result's claimed ordering (`merge_pathkeys` = the outer's FULL ordering, which
may be longer than the merge keys) can outrun what the node delivers.

The second merge-only fact this arm pins is that **`Path.HashKeys` order IS the
sort order**. `sortInnerAndOuter` concatenates the key groups in the pathkey
order it chose; `mergeSideKeyExprs` (`internal/executor/join_merge_key.go`)
sorts each side by the key TUPLE in `Join.HashKeys` order. The published list is
therefore `outersortkeys`/`innersortkeys`, which is why the arm preserves the
order it was given AND folds the keys into `Join.Predicate` in that same order —
`fillJoinHashKeys` rebuilds the published list from `Predicate` at the tail of
`Plan()`, so a re-ordering there would re-order the sort.

The jointype gauntlet (`nestjoinOK` / `useallclauses`, joinpath.c:1833-1852 —
RIGHT/RIGHT-ANTI/FULL must use *all* mergeclauses, so the truncation loop is
skipped for them entirely) and the FULL-without-usable-clause contract above
remain unwritten, as this section already anticipated: §4.4 pins every non-INNER
construct outside the search, so `addPathsToJoinrel` carries no jointype to
switch on. Both are ledgered rather than written as dead branches over a value
that does not exist. Still inert — `GOOPG_PGSHAPED_DP` is OFF.

**Status (P5.5-e-ii-b, 2026-08-04) — `create_nestloop_plan`: the arm that
writes into TWO coordinate spaces, and the residual drop that was deleting a
restriction.** `createNestLoopPlan` (`internal/planner/createplannl.go`) closes
the join arms. One path kind, `PathNestLoop`, is produced by two different arms
and emits two different executor nodes: `addNestLoopPath`'s plain loop becomes a
`*Join{JoinAlgoNestedLoop}`, and `addNLIPaths`' parameterised-inner pair becomes
a `*NestedLoopIndexJoin` — a different TYPE, not a flag, because its `Inner`
field is a `*IndexScan` the join driver calls `Rescan` on. The arm dispatches on
the inner child's `RequiredOuter`, which is the same fact PG dispatches on when
it decides to emit `NestLoopParam` entries.

The finding is that **an NLI is the first node in this seam whose expressions do
not all live in one coordinate space.** Every `*Join` predicate — hash, merge,
plain nested loop — is evaluated once per candidate pair against the merged
`outer ++ inner` row, so `joinInputs.index` re-bases all of it. An NLI's probe
key is not evaluated there: `indexScanOp.Rescan`
(`internal/executor/operators_index.go:345`) evaluates `IndexScan.Key`/`Keys`
against the slot the parent bound (`nestedLoopIndexJoinOp.outerMS`), which holds
the OUTER row alone — the inner row does not exist yet, and producing it is what
the probe is for. So the arm translates the probe keys onto the outer layout and
the residual onto the merged layout, and the outer layout is taken as the PREFIX
of the merged one rather than re-derived, so the two maps cannot disagree with
the schema that was actually concatenated. This is worth stating because on a
two-relation query whose outer happens to be first in binding order the two
spaces COINCIDE: a single-space arm builds a runnable node, passes every small
test, and probes the wrong column the first time the search reorders the join.

The second finding is a correctness defect this arm made visible in the
PRODUCER. `create_nestloop_path` (pathnode.c:2478-2500) drops from the join's
restrict clauses every clause movable into the parameterised inner, and
`nestloopResidualClauses` reproduced that test. PG may drop on movability alone
because a PG parameterised path applies every movable clause — movability is
what builds `ppi_clauses`, the index consumes what it can, and
`create_indexscan_plan` places the remainder in the scan's `qpqual`. goopg's
parameterised index path applies only the equalities in `Path.IndexClauses`, and
goopg's `*IndexScan` has no qual field for a remainder to live in. `b.y > a.x`
at inner `{b}` under parameterisation `{a}` was therefore movable, dropped from
the join residual, and enforced by nothing at all. The drop is now narrowed to
the clauses the probe demonstrably enforces (`probeEnforcedClauses`, matched by
`restrictInfo` identity against the same list `createPlan` turns into
`IndexScan.Keys`), with movability kept as the frame it is checked inside.

Two narrowings are ledgered rather than written. `addParameterizedIndexPaths`
now declines a leaf carrying `*Filter` wrappers (`scanLeafIsBare`), because
`NestedLoopIndexJoin.Inner` cannot carry them and hoisting them onto the join
residual is the D6.3b Q9 blowup — the same producer/consumer agreement §5.2's
`scanLeafFor` gate established for non-scan leaves, and it costs goopg every
searched NLI over a filtered leaf. And `InnerMemo` stays nil: Memoize is
`get_memoize_path`'s cost decision and there is no `PathMemoize`, so inserting
one here would be exactly the uncosted opinion 06 §2.1 retires. Still inert —
`GOOPG_PGSHAPED_DP` is OFF.

### 5.4 Qual placement
Every clause attaches at the lowest level whose relids it covers — decided
once, in path generation. The post-hoc placement passes
(`pushSingleSideQualsIntoInnerJoinInputs`, `pushSingleSourceFiltersAfterRemap`)
stop running on searched subtrees ([08](08-migration-and-removal.md) §3 keeps
them for non-searched shapes).

## 6. What enters the search: collapse limits, explicit JOINs

Today only comma-FROM lists of whitelisted leaves reach the DP; explicit
`JOIN … ON` trees are never reordered, and the registered
`join_collapse_limit`/`from_collapse_limit` GUCs (= 8,
`internal/config/defaults.go:1060`/`:1066`) are read by nothing. This bundle
wires them with PG semantics (`initsplan.c:1081-1238`, `deconstruct_jointree`)
— and PG's semantics are narrower than "cap the search size":

- **A flat comma-FROM list is always ONE search problem regardless of the
  limits.** PG collapses single-baserel FROM items into the parent joinlist
  unconditionally (`initsplan.c:1233-1238`: `sub_members <= 1` always
  merges); `from_collapse_limit` governs merging of pulled-up
  **sub-joinlists** (sub-selects), and `join_collapse_limit` governs
  explicit `JOIN` constructs, only. A 15-table comma join is one 15-way DP
  in PG (GEQO aside), and goopg does the same up to the §7 ceiling — the
  greedy pre-reorder must NOT be reintroduced for wide comma lists (that is
  the documented Q2 failure mode).
- INNER `JOIN … ON` chains flatten into the enclosing problem up to
  `join_collapse_limit`; setting it to 1 pins syntactic order — PG's
  standard escape hatch, which goopg has never had;
- outer joins do **not** flatten and do **not** enter the search in v1
  (§4.4 pinning rule — temporary until `join_is_legal` constraint
  inference lands). The PG endgame — flattening them with
  `SpecialJoinInfo` ordering restrictions (`joinOrderRestricted`) — is
  staged behind a ledger row: the bushy phase (now implemented, §4.3) is
  the structural half PG needs to guarantee plannability under
  restrictions; the remaining prerequisite is the constraint inference
  itself, not the search shape.

## 7. Search-size policy (the GEQO question)

PG switches to GEQO at `geqo_threshold` (12) rels (`allpaths.c:3420`). goopg
ports no GEQO. Policy:

- up to the `RelSet` ceiling (16 rels, `path.go:29`): full PG-shaped DP
  (left-deep + bushy). The worst-case number of disjoint rel pairs over
  all levels is (3ⁿ − 2ⁿ⁺¹ + 1)/2 — ~3k at n=8, ~7M at n=15 — but phase 2
  skips clause-less rels (`joinrels.c:170-172`) and only calls
  `makeJoinRel` on clause-connected pairs (`:190-191`), which on real
  workloads prunes the count to PG's own scale: PG runs this identical
  enumeration up to `geqo_threshold − 1` = 11 rels without incident (PG switches to GEQO at 12, `allpaths.c:3420`)
  (`allpaths.c:3420`). This is a different and far smaller space than the
  old subset-bitmask DP's 3ⁿ subset-split enumeration, which enumerated
  every split of every subset regardless of connectivity.
- beyond 16 rels: the search problem is chunked; the greedy connectivity
  order (`reorderCommaFromByCardinality`, `joinorder.go:83`) survives ONLY
  as this over-ceiling sequencer. Documented as
  unsupported-but-functional (plans degrade to greedy order across chunk
  boundaries); a GEQO analogue is the recorded alternative if real
  workloads hit this.

The hardcoded `len(tables) > 12` bail-out (`bushy.go:99`) is deleted with the
bushy DP.

## 8. Determinism and tie-breaking

`addPath`'s fuzzy comparison (1.01) plus a deterministic total order for
exact ties (lower relid-set lexicographic, then method rank Hash < NLI <
Merge < NL) so repeated planning of the same query is bit-stable — required
by the plan-snapshot gates (`make plan-gate`) and the TPC-DS checksum sweep.
PG tolerates nondeterminism here; our test harness does not.

## 9. Parameterised-path discipline (required, or NLI costing is garbage)

A parameterised path (an NLI inner: index scan keyed by the outer's column)
is not interchangeable with an unparameterised one, and PG segregates them
throughout. Three binding rules:

1. **`setCheapest` is parameterisation-aware.** The former test-only
   `setCheapest` minimised Total over the whole pathlist; it must instead
   track `cheapestTotal` among **unparameterised** paths and a separate
   `cheapestParameterized` list (PG's `cheapest_parameterized_paths`). Note
   the dominance side is already param-aware (`addPath`'s `outerDim`) — the
   gap was only selection.
2. **Hash and merge paths consume only unparameterised inputs.** A path
   parameterised by the rel it is being joined to is illegal as a build or
   sort input — PG's `PATH_PARAM_BY_REL` refusal in `hash_inner_and_outer`
   (`joinpath.c:2292-2299`).
3. **Parameterised paths carry their own row estimate.** An index path
   under an NLI produces per-outer-row rows, not the rel's post-filter
   `rows` — PG's `get_parameterized_baserel_size`. This is the one
   structured exception to [04](04-cost-and-cardinality.md) §2's
   "rows once per RelOptInfo": the RelOptInfo row estimate stays canonical
   for the rel as a whole; each parameterisation carries its own
   `ppiRows` beside it.

**Status (M0127-P5.4b-i, 2026-08-04): all three landed**, in
`internal/planner/pathparam.go` and the three consumers they govern, ahead of
the NLI/Memoize paths of §5.2 (P5.4b-ii) rather than beside them. The ordering
is forced: a parameterisation-blind consumer meeting its first parameterised
path does not produce a slow plan, it produces an unbuildable one.

Two refinements the rules above did not say, both found while implementing them:

- **Rule 3 needs no new field.** PG's `ppi_rows` is carried in `path->rows`
  (`create_index_path` assigns it there), so the rule is really a discipline on
  the COST primitives: they must read the child PATH's rows, never
  `child.Rel.Rows`. `Path.Rows` is goopg's carrier and `addHashJoinPath` /
  `addNestLoopPath` / `generateNLIPath` read it.
- **There is a fourth rule, implicit in PG because it is just how the
  constructors work: a join path computes its own parameterisation from its
  children's, and for a nested loop that is a SUBTRACTION.** A nested loop is
  precisely the operator that discharges an inner parameterised by the outer
  (`calc_nestloop_required_outer`, `pathnode.c:2592`), so an NLI subtree over
  unparameterised inputs is itself unparameterised — which is what makes it a
  legal hash-join input under rule 2 rather than something rule 2 forbids.
  Merge and hash discharge nothing and simply union
  (`calc_non_nestloop_required_outer`, `:2618`). Miss this and `RequiredOuter`
  is read as "what I depend on below" instead of "what I still need supplied
  from above" — which is how `generateNLIPath` came to declare
  `RequiredOuter: inner.Relids`, naming a relation the joinrel contains.

## 10. Coordinate translation at the search boundary (owned here, not hoped away)

[02](02-plan-shape-contract.md) §3 deletes the *internal* layout machinery —
but the search's chosen tree is still a cost-chosen reordering of syntactic
order, and everything **above** the searched subtree (Aggregate/projection/
Sort expressions, retained Filters, the pinned unnest spine) references
pre-search coordinates. That translation is currently done by
`buildBindingsPosMap`/`applyJoinTreePosMap` and the predp spine
re-resolution — the family the project's own analysis calls its index-skew
bug generator. The replacement must be named, not implied:

- `createPlan` computes **one boundary map** at the search root. With the
  bushy phase, a single prefix-sum composition over a chain order π is no
  longer sufficient — a composite can be assembled from any split — so
  goopg adopts a **canonical relid-order layout** — a design choice
  stricter than PG's ([02](02-plan-shape-contract.md) §3): every joinrel's
  output columns are in relid order of its relset, a pure function of the
  relset. PG's `build_joinrel_tlist` appends outer-then-inner and resolves
  ordering later in `setrefs` (see the NOTE at
  `postgres/src/backend/optimizer/util/relnode.c:780-782`); goopg resolves
  at construction time. The
  boundary map is then the single composition relid-order output index →
  syntactic (binding-order) column index — no per-subset layouts, no
  per-node remaps, computed once from the final relset.
- Every enclosing-tree expression is rewritten through that single map at
  plan-creation time (the `setrefs.c` moment, at our fidelity level), OR —
  the simpler v1, decided at implementation — `createPlan` emits one
  `Project` at the search root that **reorders the final rel's output from
  canonical relid order into the syntactic (binding) order the enclosing
  tree expects**, so the map is invisible above the search root; the cost
  is one narrow pass-through node (fused away by the existing projection
  elision if the two orders happen to coincide).
- The pinned-spine re-resolution in `predp.go` survives and consumes this
  map; it is NOT in the deletion inventory until the boundary map is proven
  ([08](08-migration-and-removal.md) §4 note).

Debug tripwire: a build-mode assertion that every `ColumnRef` above the
search root resolves within its input schema — the M0097-0058
out-of-bounds class must fail loudly at plan time, never at execution.

**Amendment 2026-08-04 (P5.5-e-i) — the map's INPUT now exists, and it is
produced by the recursion rather than re-derived.** The scan and sort arms
could ignore coordinates entirely (a scan's schema is its leaf's; a sort moves
no column), but the hash-join arm is the first that MERGES two schemas, and it
cannot emit a key without answering the question. So `createPlan` gained a
layout-carrying sibling, `createPlanNode(p) (Node, outputLayout)`
(`internal/planner/createplanjoin.go`), where `outputLayout[i]` is the
**pre-search binding coordinate of the emitted node's output column `i`** — the
inverse of the map this section asks for. It is built the one way that cannot
drift from the tree: a base rel's layout is the range `RelOptInfo.baseOffset`
recorded at `buildInitialRels` (the companion field to `baseLeaf` — `baseLeaf`
says what a relid MEANS, `baseOffset` says where it USED TO BE), a sort passes
its child's through unchanged, and a join concatenates its children's in the
same statement that concatenates the schema.

Two consequences for the rest of this section:

- The **inside-the-tree** half of the translation is now done, per node, by
  `translateToLayout` — `set_join_references` (setrefs.c:2557) at goopg's
  fidelity. PG rewrites a join qual's Vars into OUTER_VAR/INNER_VAR because its
  executor addresses the two input slots separately; goopg's evaluates one
  merged `outer ++ inner` row, so the same job is a single renumbering. This is
  what the section above did not say out loud: the clauses the search reasons
  about are written in binding coordinates *by construction* — `relidsOfExpr`
  DECIDES a clause's relset by bucketing its `ColumnRef.Index` against those
  same offsets — so a join arm that copied a clause across unchanged would key
  on whichever column happened to land at that index.
- The **above-the-search-root** half is still P5.5-f's, and this amendment does
  not pre-empt its choice. Whichever variant is taken — rewriting the enclosing
  expressions, or emitting one reordering `Project` at the root — its input is
  the root's `outputLayout`, which the recursion now hands back. The canonical
  relid-order commitment above therefore remains a decision about what the
  search root PUBLISHES, not about how each joinrel is assembled internally.
