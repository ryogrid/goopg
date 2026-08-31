# Optimizer (Part 1) — Code Review 2026-08-31

Files: cardinality.go, collapse.go, copy.go, cost_funcs.go, costbitmap.go,
costindex.go, createplan.go, createplanbitmap.go, createplanindex.go,
createplanjoin.go, createplannl.go, createplanroot.go, createplansimple.go,
cte_inline_pushdown.go, enclosingtree.go, equiv_class.go, exists_to_any.go,
expr_result_type.go, exprkey.go, exprwalk.go

Findings count: 14

---

### `cardinality.go:estimateNumGroups` — per-relation full-tree walk plus subtree re-estimation
- **Issue**: Inside the per-relation loop, `relFilteredRows(child, rel)` is called once per distinct relation in the GROUP BY. Each call runs `relFilteredRowsWalk(root, rel)`, a full descent of the plan tree, and at every passthrough node on the path it calls `EstimateRows(n)`, which itself recursively re-estimates the entire subtree below `n`.
- **Why**: For a GROUP BY over a deep left-deep join chain of `R` relations and `M` nodes, the per-relation work is `O(M * subtree)` and the total is `O(R * M * subtree)` — cubic-ish in the number of relations. `EstimateRows` is a pure function with no memoization, so every call re-walks from scratch. `estimateNumGroups` runs once per `*Aggregate` in the statement, so a query with several aggregates pays it multiple times.
- **Suggestion**: Compute a single bottom-up `EstimateRows` pass (memoized per node) before the loop and have `relFilteredRowsWalk` read the memo instead of re-estimating; or at minimum cache `EstimateRows` results keyed by node pointer.
- **Severity**: medium

### `cardinality.go:semiPairMatchFraction` — O(n·m) MCV matching nested loop
- **Issue**: The two MCV lists are matched with a doubly-nested loop (`for i := range st1.MCV { for k := 0; k < clamped2; k++ { … } }`), comparing string values one at a time.
- **Why**: Worst case is `len(st1.MCV) × len(st2.MCV)` string comparisons per semi/anti join key pair (MCV lists are sized by the stats target, typically tens to hundreds of entries). `semiJoinMatchFraction` iterates every equi-pair and `estimateJoin`/`EstimateRows` can be invoked repeatedly during plan-time costing, so this is a real per-estimate hotspot on stats-rich semi-joins.
- **Suggestion**: Build a `map[string]int` from `st2.MCV` value → index once (respecting the `clamped2` prefix), then a single O(n) pass over `st1.MCV` with the used-up flag tracked in the map value. Same result, linear time.
- **Severity**: medium

### `cardinality.go:EstimateRows` — no memoization on recursive estimates
- **Issue**: `EstimateRows` recurses without any caching; a left-deep chain of `N` joins re-estimates its own left subtree at every level (`estimateJoin` calls `EstimateRows(j.Left)` then `EstimateRows(j.Right)`, and each of those re-estimates their children, etc.).
- **Why**: A left-deep chain of `N` joins costs `O(N²)` node visits for one top-level estimate, and this is compounded by `relFilteredRows` (above) and by the per-key `resolveBaseColumn` walks in `keyNDistinct` / `rightExprNDistinct` / `keyColumnStats` / `rightExprStats` (each walks the child subtree from the top). For deep TPC-H/DS joins this is pure redundant computation at plan time.
- **Suggestion**: If an estimate pass can be made explicitly bottom-up (children before parents) with per-node memoization, all of these call sites can read precomputed values. At minimum, dedupe the `resolveBaseColumn`-style lookups so a key's nd/stats are resolved once per node rather than once per consumer.
- **Severity**: medium

### `cardinality.go:indexScanRows` — linear column lookup inside the key loop
- **Issue**: The `for i := 0; i < nEq; i++` loop calls `columnStatsByName(tbl, idx.Columns[i])`, which is a linear scan of `tbl.Columns` per iteration (pathparamindex.go:569).
- **Why**: `columnStatsByName` has no map; for an index with `k` key columns this is `O(k × nCols)`. Small in absolute terms, but the function is called per index-scan node during planning and re-planning.
- **Suggestion**: `Table.Stats.Columns` is positional over `Table.Columns`; resolve the index-column names to ordinals once (or precompute an ordinal per index) and index `tbl.Stats.Columns` directly instead of name-searching in the loop.
- **Severity**: low

### `costbitmap.go:computeBitmapPagesLooped` — results computed then discarded
- **Issue**: `heapPages := math.Min(pages, T)` is computed and then immediately discarded via `_ = heapPages` (line 184). Likewise the whole lossiness block (lines 192-206) computes `lossyPages`, `exactPages`, `lossyTuples`, `exactTuples` and then discards the sum with `_ = lossyTuples + exactTuples`.
- **Why**: Pure wasted computation on every bitmap cost estimate: values are built and thrown away, and the comments admit they do nothing ("Pages fetched doesn't change"). It is also misleading to future readers (looks like the lossiness adjustment is applied when it is not).
- **Suggestion**: Delete the dead computations. If the lossiness adjustment is intentionally not yet implemented, replace the block with a short comment stating so rather than computing phantom numbers.
- **Severity**: low

### `costindex.go:indexTupleWidth` — allocation + nested scan per call
- **Issue**: `for _, name := range append(append([]string{}, idx.Columns...), idx.IncludeColumns...)` allocates a fresh combined slice on every call, and the inner `for i := range tbl.Columns` is a linear name scan per column.
- **Why**: `estimateIndexGeometry` (which calls this) is invoked per index path during path generation / bitmap cost estimation, so these small allocations and linear scans are repeated many times per query.
- **Suggestion**: Avoid the concatenated allocation by iterating `idx.Columns` and then `idx.IncludeColumns` in two loops; precompute a name→ordinal map for `tbl.Columns` once (or rely on `columnStatsByName`-style positional resolution) instead of the nested scan.
- **Severity**: low

### `cost_funcs.go:hashJoinCost` — recomputes per-row geometry
- **Issue**: `hashJoinCost` calls `hashsize.Choose(...)`, and then, when `NBatch > 1`, calls `spillPages` twice — each `spillPages` re-invokes `hashsize.EntryBytes` for the same `ncols`/`avgVarBytes` the `Choose` call already sized.
- **Why**: Minor duplicated arithmetic on a plan-time hot path (every hash path generated by the DP); not a correctness issue, just redundant work and a re-derivation of the same byte model.
- **Suggestion**: Have `hashsize.Choose`'s result (or a small helper) carry the per-row bytes so `spillPages` need not re-derive `EntryBytes` per call.
- **Severity**: low

### `createplanroot.go:missingBindingCoords` — sorting an already-sorted slice
- **Issue**: `missingBindingCoords` collects holes by iterating `m` in ascending `bind` order and then calls `sort.Ints(missing)`.
- **Why**: The slice is appended in strictly ascending order by construction, so the sort is pure wasted work (and a needless import). Trivial in size, but it is an unconditional redundant operation on every boundary map.
- **Suggestion**: Drop the `sort.Ints` call; the order is already guaranteed by the loop.
- **Severity**: low

### `createplanindex.go:createIndexScanPlan` (index-only arm) — linear schema search per covered column
- **Issue**: For an index-only scan, each covered column is located in the leaf schema by a linear scan: `for j := range id.schema { if id.schema[j].Name == c.Name { … } }`.
- **Why**: `O(len(IndexOnlyCovered) × len(id.schema))` per index-only path. Column counts are small so this is minor, but it runs on every index-only path build.
- **Suggestion**: Build a name→ordinal map of `id.schema` once before the loop (the same map shape used elsewhere in this package) instead of re-scanning per column.
- **Severity**: low

### `createplanjoin.go:baseRelLayout` — linear name scan for narrowed leaves
- **Issue**: In the `width != len(leaf)` branch, each output column is matched to the leaf schema by an inner `for j := range leaf { if leaf[j].Name == col.Name { … } }` scan.
- **Why**: Same pattern as the index-only arm above: `O(width × len(leaf))` per narrowed leaf, on the plan-build path for every index-only scan.
- **Suggestion**: Precompute a name→ordinal index for `leaf` once, then look up each column in O(1).
- **Severity**: low

### `exists_to_any.go:existsToAny` — predicate split twice
- **Issue**: `splitAnd(pred)` is computed twice on the same predicate: once in the correlation-pair scan loop (line 305) and again when building `remaining` (line 350).
- **Why**: `splitAnd` allocates a fresh slice and walks the AND tree each call. Duplicated work on a per-EXISTS plan-time pass; small, but trivially avoidable.
- **Suggestion**: Split once into a local `conjuncts := splitAnd(pred)` and iterate it in both places.
- **Severity**: low

### `exprwalk.go:exprChildSlots` — fresh slice allocation per node per walk
- **Issue**: `exprChildSlots` returns a newly allocated `[]exprSlot` for every container node, and the three drivers (`walkExprRefs`, `rewriteExprRefsInPlace`, `cloneExprRefs`) allocate it again at every level of recursion. `cloneExprRefs` additionally shallow-clones every node (including leaves) and duplicates every slice field.
- **Why**: A rewrite of one nested predicate allocates a slot slice per node, on top of the per-node clone allocations. This is acknowledged in the code ("The cost is a few extra allocations per conjunct on a plan-time path"), so it is a documented trade-off — but the drivers are called from several plan passes, and a per-node stack-allocated slot buffer (or an explicit capacity hint when the arity is known statically) would remove most of it.
- **Suggestion**: For the fixed-arity cases (BinaryOp, IsDistinctFromExpr, LikeEscapePattern, the single-operand nodes) return a stack `[...]exprSlot` backed slice to avoid the heap allocation; keep the slice-allocating path only for the variable-arity nodes.
- **Severity**: low

### `exprwalk.go:exprSelfKey` — fmt.Sprintf for plain scalars
- **Issue**: `exprSelfKey` uses `fmt.Sprintf` for trivial scalar rendering throughout, e.g. `fmt.Sprintf("int:%d", x.Value)`, `fmt.Sprintf("col:%d", x.Index)`, `fmt.Sprintf("bool:%t", …)`, `fmt.Sprintf("binary:%d", x.Op)`.
- **Why**: `fmt`'s reflection-based formatting is significantly slower than `strconv.AppendInt` / `strconv.FormatInt`, and these keys are built by `exprIdentityKey` on hot expression-comparison paths (aggregate dedup, canonicalization).
- **Suggestion**: Use `strconv.AppendInt`/`FormatInt` (and `strconv.FormatBool`) into a reused `strings.Builder`; reserve `fmt` for the genuinely composite strings.
- **Severity**: low

### `enclosingtree.go:enclosingNodeScopeOf` / `walkEnclosingTree` — allocations on the assertion walk
- **Issue**: Every node on the assertion walk allocates: `enclosingNodeScopeOf` builds `exprs` slices via `append`/`nonNilExprs`/`sortKeyExprs` (a fresh slice even for a single-element predicate), and `walkEnclosingTree` calls `fmt.Sprintf("%s %T", what, n)` per node.
- **Why**: This is a plan-time debug tripwire, so the cost is bounded, but it runs on every production plan (the walk only returns early when the flag is off — it does not return early on node kind) and it is trivially reducible.
- **Suggestion**: Reuse a small preallocated buffer for the per-node expression slices; avoid the per-node `Sprintf` (format only when a failure is actually reported).
- **Severity**: low

---

Files with no issues worth reporting:

- **collapse.go** — pure joinlist construction; all loops are single-pass, no repeated lookups or allocations beyond the natural ones.
- **copy.go** — per-option validation loop is single-pass; `strings.ToLower` per option is fine. (Minor: `strconv.ParseInt(strings.TrimSpace(o.Value), …)` is evaluated even when `o.Value == ""`, a trivially wasted call, not worth a finding.)
- **cost_funcs.go** — see `hashJoinCost` finding above; the pure cost functions are allocation-free.
- **createplan.go** — thin dispatch, no loops.
- **createplanbitmap.go** — `collectBitmapPartialPredicates` recursion is minimal and allocation-light; `bitmapQualExprs` preallocates correctly.
- **createplannl.go** — single-pass translation loops, preallocated slices.
- **createplansimple.go** — single-pass key loop, preallocated.
- **cte_inline_pushdown.go** — walkers are single-visit; `splitAnd` runs once per conjunct set.
- **equiv_class.go** — union-find with path compression; class iteration is O(n log n)-bounded and deterministic; `orderedPair` map keying is fine.
- **expr_result_type.go** — recursive type resolution, no loops over data; `exactTypeOID` lowercases per call but is not in a hot loop.
- **exprkey.go** — reflection-based key building is heavy by deliberate design (documented in the file header) and bounded by `maxStructuralKeyDepth`; used only for plan-time keys. Not worth optimizing without a measured need.
