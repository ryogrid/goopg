# GEQO: Genetic Query Optimizer

## Status

**IMPLEMENTED and MERGED** — not a draft. The implementation landed in
`internal/optimizer/geqo.go` (commit `7609d15ef`), wired at
`relfromjoinlist.go:searchOneProblem` (`GeqoEnabled() && len(items) >=
GeqoThreshold()`), with the GUC bridge in `cmd/goopg/main.go` and wiki
documentation in `docs/wiki/modules/optimizer.md`.

Both required agent reviews were performed and are recorded in
`review/geqo-0190/README.md`:

- **Design-doc review** (pre-implementation): 4 findings, all fixed
  (the >16 dispatch contradiction, pool-sizing `ceil`/bypass details,
  line-reference drift, missing initial `sort_pool`).
- **Implementation review** (post-implementation logic-bug check): 5 findings,
  all fixed — including one CRITICAL (`mergeClump` missing `setCheapest`
  meant GEQO could never plan ≥3-relation queries). `go build ./...` and the
  full `go test ./internal/optimizer/ -count=1` suite PASS after the fixes.

## Motivation

goopg's current join-order search is a faithful reproduction of PG's
`standard_join_search` (level-list DP over 16 base relations, `RelSet uint16`).
When a query exceeds 12 relations, PG's DP has the same combinatorial explosion
that PG itself avoids by switching to a **genetic query optimizer (GEQO)** —
a randomised search that treats the join-order problem as a constrained
Traveling Salesman Problem (TSP) and uses a genetic algorithm to find a
near-optimal plan without enumerating every subset.

goopg's existing `geqo*` GUCs are no-op compatibility stubs (`defaults.go:1246`).
The planner's explicit policy is "ceiling-16 no-GEQO" (03 §7). This document
proposes removing that restriction and implementing GEQO matching PG's
conditions and scope.

## Design

### Integration point

PG dispatches GEQO in `make_rel_from_joinlist` (allpaths.c:3418-3423):

```c
if (join_search_hook)
    return (*join_search_hook)(root, levels_needed, initial_rels);
else if (enable_geqo && levels_needed >= geqo_threshold)
    return geqo(root, levels_needed, initial_rels);
else
    return standard_join_search(root, levels_needed, initial_rels);
```

goopg's equivalent is `tryPGShapedJoinSearch` in `joinsearchseam.go:182`.
The seam currently declines when `nrels > maxSearchRels` (16). The GEQO
integration point is placed in the same seam, matching PG's dispatch exactly:
when `geqo` GUC is ON and `nrels >= geqo_threshold` (default 12), the seam
routes to the GEQO search instead of the DP. There is **no** separate
`nrels > maxSearchRels` arm — PG has none (its dispatch is only
`enable_geqo && levels_needed >= geqo_threshold`, allpaths.c:3420), and
goopg's `RelSet uint16` cannot represent more than 16 base relations, so the
seam still declines past 16 (unchanged v0 behaviour). Widening `RelSet` so
GEQO can exceed 16 relations — matching PG's full scope, where GEQO exists
precisely for very large joins — is a separate follow-up task, not part of
this one.

### GUC activation

The existing `geqo*` GUCs in `defaults.go` are currently no-op stubs. The
implementation makes **`geqo`** and **`geqo_threshold`** live (bridged to
process-global atomics via `OnChange` in `cmd/goopg/main.go`, the same pattern
as `enable_memoize`): `geqo` (bool, default on) and `geqo_threshold` (int,
default 12) control whether GEQO is used.

The remaining tuning GUCs — `geqo_effort`, `geqo_pool_size`,
`geqo_generations`, `geqo_selection_bias`, `geqo_seed` — are accepted (they
stay registered so `SET` succeeds) but v0 uses PG's default values for them
(effort 5, pool = `2^(nrels+1)` clamped to `[10·effort, 50·effort]`,
generations = pool size, bias 2.0, fixed seed), because the planner has no
session in scope at plan time to read them (the same gap the cost model's
`work_mem`/`effective_cache_size` threading has; see cost_funcs.go).
Threading them is a follow-up, not part of the activation condition — which
matches PG's condition (`geqo` on AND `nrels >= geqo_threshold`) exactly.

### Algorithm structure

PG's `geqo()` (geqo_main.c) is a straightforward GA:

1. **Pool initialisation**: create `pool_size` random join-order tours
   (permutations of 1..nrels). Each tour is evaluated by `geqo_eval` which
   constructs a join tree from the order and returns the cheapest path cost.
   Invalid tours (DBL_MAX) are discarded. The pool is then sorted once by
   fitness (ascending cost) — this establishes the sorted invariant that
   `spread_chromo` relies on for the binary-search insert.

2. **Selection**: pick two parent tours using linear bias selection
   (`geqo_selection`), where fitter (lower-cost) tours are more likely to
   be chosen.

3. **Crossover**: combine the parents into a child tour using the default
   **Edge Recombination (ERX)** crossover operator, which preserves as many
   adjacency relationships from the parents as possible. Alternative operators
   (PMX, CX, PX, OX1, OX2) are compile-time choices in PG; goopg implements
   ERX only (the PG default).

4. **Evaluation**: compute the fitness of the child tour via `geqo_eval`.

5. **Replacement**: insert the child into the pool, displacing the worst
   individual (`spread_chromo`). The pool remains sorted by cost.

6. **Repeat**: steps 2-5 for `number_generations` iterations.

7. **Result**: the best tour (lowest cost) in the final pool is converted to
   a join tree via `gimme_tree`.

### Pool and generation sizing

PG's defaults (geqo_main.c `gimme_pool_size`, `gimme_number_generations`):

```
if user sets Geqo_pool_size >= 2: pool_size = Geqo_pool_size
else: pool_size = ceil(2^(nrels+1)), clamped to [10*effort, 50*effort]
generations = Geqo_generations if > 0, else pool_size
```

### `gimme_tree` — tour to join tree

The core of `geqo_eval` is `gimme_tree` (geqo_eval.c:162), which converts a
permutation (tour) into a `RelOptInfo` by greedily merging relations:

1. Walk the tour in order. For each relation, make it a "clump" of size 1.
2. Merge the clump into the list of existing clumps: try to join it with
   each existing clump, preferring joins that have a relevant join clause
   (`desirable_join` = `have_relevant_joinclause` or
   `have_join_order_restriction`). When a merge succeeds, the combined clump
   may be merged with further clumps (recursively).
3. After the tour is exhausted, force-join any remaining clumps (even
   cartesian joins) to produce a single result.
4. If no single join relation can be formed, return NULL (DBL_MAX fitness).

This produces bushy plans when restrictions require them, unlike the
original left-deep-only approach.

### goopg-specific implementation

#### `makeJoinRel` reuse

`gimme_tree` calls `make_join_rel` (relnode.c) to construct each joinrel.
goopg has an equivalent: `makeJoinRel` (joinsearchlevel.go) which calls
the `joinRelBuilder` interface (`sizeJoinRel` + `addPaths`). The GEQO
implementation reuses the same `joinRelBuilder` — the same `calcJoinrelSize`
and `addPathsToJoinrel` that the DP search uses — so both search strategies
produce comparable costs.

#### Memory management

PG's `geqo_eval` creates a temporary memory context for each `gimme_tree`
call and discards it after computing fitness. goopg follows the same pattern:
a temporary `searchCtx`-like workspace is created per tour evaluation, the
joinrel is built via `makeJoinRel` (which adds entries to the workspace's
relMap), and the workspace is discarded after reading the cheapest path cost.

#### Thread safety

The GA uses a seeded PRNG (Go's `math/rand` with `rand.NewSource(seed)` or
`crypto/rand` for the default seed=0). The PRNG is local to the GEQO
invocation, so concurrent planning sessions do not interfere.

#### RelSet width for GEQO

The DP search is limited to 16 rels by `RelSet uint16` (path.go:30, the
declaration; comment at path.go:26-29). GEQO does not need this limit — the
tour is a `[]Gene` (int) permutation, not a RelSet. However, the `RelOptInfo`
still uses `RelSet uint16` for its relids, so base relations beyond 16 need a
wider representation. For v0, GEQO queries are limited to the same 16-relation
maximum as the DP search (`geqo_threshold` default 12 activates GEQO within
that range); widening the RelSet representation is a separate task.

### Files to create

| File | Role |
|---|---|
| `internal/optimizer/geqo.go` | The whole GEQO implementation in one file: the GA driver (`geqoSearch`), pool/generation sizing, `geqoEval`/`gimmeTree` (clump merging), linear-bias selection, ERX edge-recombination crossover, the edge table, the seeded PRNG, and the pool/chromosome types. PG's per-file split (geqo_main.c/geqo_eval.c/geqo_pool.c/geqo_selection.c/geqo_erx.c/geqo_random.c) is collapsed into one Go file because the pieces are small and tightly coupled; the function names and semantics mirror the PG originals. |
| `internal/optimizer/geqo_test.go` | Unit tests: pool sizing, generations, initTour permutation, randint range, linearRand range, RNG determinism, edge-table construction, gimmeTour validity, spreadChromosome ordering, eval panic guard. |

### Files to modify

| File | Change |
|---|---|
| `internal/optimizer/relfromjoinlist.go` | `searchOneProblem` routes to `geqoSearch` when `GeqoEnabled() && len(items) >= GeqoThreshold()`, instead of the DP `joinSearch`. |
| `cmd/goopg/main.go` | GUC bridge: `SET geqo = off\|on` → `SetGeqoEnabled`, `SET geqo_threshold = N` → `SetGeqoThreshold` (same pattern as `enable_memoize`). |
| `docs/wiki/modules/optimizer.md` | Document GEQO integration. |

## Acceptance criteria

1. GEQO produces a valid plan for any query with 12-16 relations
   (default threshold) — no crash, no infinite loop.
2. The same plan shape is produced deterministically for a given `geqo_seed`.
3. GUCs `geqo`, `geqo_threshold`, `geqo_effort`, `geqo_pool_size`,
   `geqo_generations`, `geqo_selection_bias`, and `geqo_seed` are accepted
   and honoured.
4. `SET geqo = off` restores the DP search for queries below
   `maxSearchRels`.
5. Unit tests cover pool init, selection, crossover, and gimmeTree.
6. Existing planner tests continue to pass (GEQO only activates at
   >= threshold, so existing small-query tests are unaffected).