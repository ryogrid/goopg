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
