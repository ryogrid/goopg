# Optimizer (Part 1) — Bug Review 2026-08-31

Files: cardinality.go, collapse.go, copy.go, cost_funcs.go, costbitmap.go, costindex.go, createplan.go, createplanbitmap.go, createplanindex.go, createplanjoin.go, createplannl.go, createplanroot.go, createplansimple.go, cte_inline_pushdown.go, enclosingtree.go, equiv_class.go, exists_to_any.go, expr_result_type.go, exprkey.go, exprwalk.go
Findings count: 6

Files read fully. No-bug notes at the bottom.

---

### `copy.go:validateCopyOptions` — REJECT_LIMIT with ON_ERROR=STOP is accepted instead of rejected
- **Bug**: The incompatible-combination pass only checks `if rejectLimitSpecified && !onErrorSpecified` (line 484). It never requires the ON_ERROR value to actually be IGNORE. PostgreSQL's `ProcessCopyOptions` rejects any `reject_limit` unless `on_error == COPY_ON_ERROR_IGNORE`, so `COPY t FROM STDIN (ON_ERROR stop, REJECT_LIMIT 5)` must fail with "COPY REJECT_LIMIT requires ON_ERROR to be set to IGNORE" — here it is accepted and the limit is silently ignored at execution (the error message even says "to be set to IGNORE", but the code does not verify that).
- **When it triggers**: Any COPY FROM specifying `on_error stop` together with `reject_limit`. The test suite (copy_test.go:236) only covers the `reject_limit`-without-ON_ERROR case, so the gap is uncovered.
- **Fix**: Change the condition to `rejectLimitSpecified && (!onErrorSpecified || !onErrorIsStop)`.
- **Severity**: low (validation-only divergence; PG compatibility is claimed byte-for-byte).

---

### `exprwalk.go:exprChildSlots` — FuncCall child slots silently omit Filter/Over/OrderBy/WithinGroup/Variadic
- **Bug**: The `*FuncCall` arm returns only `&x.Args` as child slots (lines 162-167). `parser.FuncCall` also carries `Filter`, `Over`, `OrderBy`, `WithinGroup` and `Variadic`, which are semantically significant — the same package's `funcCallTailKey` (exprkey.go:85) exists precisely because omitting them caused the M0125-0009/M0097-0032 wrong-answer class (aggregates collapsing onto one slot). But every driver here (`walkExprRefs`, `cloneExprRefs`, `rewriteExprRefsInPlace`, `exprIdentityKey`) is built on `exprChildSlots`, so a `ColumnRef` inside `FILTER (WHERE x)`, `OVER (PARTITION BY x)`, in-arg `ORDER BY`, `WITHIN GROUP`, or a `Variadic` element is invisible to all of them.
- **When it triggers**: (a) `exprIdentityKey`/`exprEqual` produce identical keys for two calls that differ only in these tail positions — e.g. `sum(x) FILTER (WHERE a)` vs `sum(x) FILTER (WHERE b)` both key as `(fn:sum/f/f (col))` — the exact identity-collision class M0125-0024 was written to eliminate; (b) any positional rewriter (e.g. `translateToLayout`, `cloneExprRefs`) fails to re-base a reference buried in a FILTER/OVER clause, leaving it in the wrong coordinate space silently. The exhaustiveness gate (exprwalk_exhaustive_test.go) does not catch this because it compares against `plan.go`'s `exprNode()` receivers, which have the same single-Args coverage — the test only proves the two agree with each other, not that either sees the tail.
- **Fix**: Add slots for `Filter`, `Over`, `OrderBy` (as `SortKey`-style expr slots), `WithinGroup` and `Variadic` in the `*FuncCall` arm, and mirror them in `exprSelfKey` (currently `"fn:%s/%t/%t"` ignores all five) and `shallowCloneExpr`.
- **Severity**: medium (silent; latent wrong rows / dropped qual enforcement once such a call reaches a traversed position).

---

### `createplannl.go:createNestLoopBitmapJoinPlan` — `bhs.BitmapQual = nil` drops the lossy-page / partial-index recheck quals
- **Bug**: The bitmap NLI arm unconditionally clears `bhs.BitmapQual` (line 369). `createBitmapHeapScanPlan` populated it with the original index quals plus the partial-index predicates (`collectBitmapPartialPredicates`) precisely so the BitmapHeapScan can re-verify tuples on lossy bitmap pages and re-apply partial-index predicates — the correctness safety net. The arm instead moves only the probe keys onto `bis.Key/Keys` and discards the recheck expression list, so on a lossy page every tuple on the page would be emitted without re-check, returning rows the index qual rejects.
- **When it triggers**: Currently unreachable — `createNestLoopPlan` dispatches here only when `innerPath.RequiredOuter != 0` and the inner is a `PathBitmapHeapScan`, while the only parameterised producer (`addOneParameterizedIndexPath`, pathparamindex.go) emits `PathIndexScan`. But the arm is live code with a producer-side admission contract; the moment a parameterised bitmap path is generated (the codebase already plans keyed bitmap probes elsewhere, see unnest.go's bitmap arms) this silently returns wrong rows.
- **Fix**: Re-express the recheck quals into the merged/leaf coordinates (like `Predicate`) instead of dropping them, or refuse the shape until the recheck is carried.
- **Severity**: medium (latent wrong-rows hazard; unreachable today).

---

### `cardinality.go:indexScanRows` — unique-index shortcut ignores range bounds
- **Bug**: The unique short-circuit (lines 336-338) returns 1 as soon as `idx.Unique && nEq >= len(idx.Columns)`, without considering `lowKey`/`highKey`. If a scan carries both full equality coverage AND a range bound, the range's `defaultIneqSel` selectivity (applied later for non-unique scans) is skipped, so the estimate is exactly 1 row instead of `relRows × (∏ eq-sel × ineq-sel)`.
- **When it triggers**: Any keyed index scan on a unique index whose node also sets LowKey/HighKey. Whether the planner currently builds that shape is unclear from this file alone, but the function explicitly accepts both inputs simultaneously (`keyed` and `bounded` are independent booleans) and the uniqueness arm is evaluated before the bound terms — a structural inconsistency: a two-sided range on a unique PK would be priced at 1 row.
- **Fix**: Only return 1 when `!bounded` (an all-columns-equality probe); otherwise fall through to the selectivity loop.
- **Severity**: low (estimate-only; PG's `btcostestimate` applies the inequality selectivity even on a unique index).

---

### `costbitmap.go:computeBitmapPagesLooped` — lossy-page adjustment computes results and discards them
- **Bug**: The lossiness block (lines 192-206) computes `lossyPages`, `exactPages`, `lossyTuples`, `exactTuples` and then discards all of it (`_ = lossyTuples + exactTuples`), returning `pages` unchanged. PostgreSQL's `compute_bitmap_pages` performs the same split but the *caller* (`cost_bitmap_heap_scan`) uses the lossy/exact split to inflate `tuples_fetched` for the CPU term (a lossy page yields every tuple on the page). goopg's `costBitmapHeapScan` always charges `cp.cpuTupleCost * tuplesFetched` with the un-inflated count, so a memory-limited (lossy) bitmap scan is systematically under-charged for its CPU work, which can tip a lossy bitmap ahead of a competing index/seq scan on cost.
- **When it triggers**: Any bitmap path where `maxEntries < pages` (i.e. the TIDBitmap budget derived from work_mem is exceeded). The two intermediate `_ =` discards also leave dead code that masks the omission from review.
- **Fix**: Thread the adjusted `tuples_fetched` out (as PG does through the returned page count plus a lossy-tuples estimate) and use it in `costBitmapHeapScan`'s CPU term.
- **Severity**: low (estimation-only; plan-shape effect under low work_mem).

---

### `cardinality.go:EstimateRows` — `*Limit` with `LIMIT 0` returns 0, which callers read as "no estimate"
- **Bug**: The `*Limit` arm (lines 64-71) returns `lim` (=0) when the limit is a constant 0 and `child <= 0 || lim < child` fires, and `*Values` (line 57) returns 0 for an empty VALUES. Every caller of `EstimateRows` treats `<= 0` as "no estimate available" (documented at line 36-38 and used throughout: `applyLocalFilterSelectivity`, `estimateSetOp`, `estimateJoin`, hash-table sizing, Memoize decisions). A genuinely zero-row plan therefore reads as "unknown", so cost/algorithm decisions above it fall back to the no-stats path.
- **When it triggers**: `SELECT ... FROM t LIMIT 0` (estimate becomes 0 instead of the correct "0 rows known"), or `VALUES ()`.
- **Fix**: Distinguish "known zero" from "unknown" (e.g. return the limit 0 only where callers can handle it, or clamp the Limit arm to 1 so the documented "0 = no estimate" contract holds).
- **Severity**: low (estimate/decision semantics; conservative direction in most callers).

---

## Files with no findings

- **collapse.go** — `deconstructJointree`'s merge rule, `combineJoinlists` and `soleItemOr` reproduce upstream's limit arithmetic exactly (sub_members<=1 arm, `remaining` decremented before the test); `innerPrefixBelowOuterSpine` declines safely on unexpected shapes; `pinnedOuter`'s zero-value-default polarity is deliberate and documented. No bug found.
- **cost_funcs.go** — cost formulas match PG (hash/nestloop/merge/gather/agg, `costSortRun`'s external-merge arm, `tuplesortMergeOrder` with `2*TAPE_BUFFER_OVERHEAD`). `getParallelDivisor`, `indexPagesFetched` and `spillPages` checked against the oracle. No bug found.
- **costindex.go** — `index_pages_fetched` branches (including the `T <= b` and `lim` arms), `btcostestimate` descent charge, `loop_count>1` pro-rating, `heapPagesAfterVM` at all four sites, and `estimateIndexGeometry`'s real-pages override all checked. No bug found.
- **createplan.go / createplanbitmap.go / createplanindex.go / createplanjoin.go / createplansimple.go / createplanroot.go** — the coordinate machinery (`outputLayout`, `translateToLayout`, `boundaryMap` hole/out-of-range/duplicate checks, `absorbMergeSort`, `baseRelLayout` name fallback) is internally consistent; panics are fail-loud producer guards. No bug found beyond the createplannl.go finding above.
- **cte_inline_pushdown.go** — the per-layer remap declines (never rewrites incorrectly) and the refcount==1 gate is sound. No bug found.
- **enclosingtree.go** — tripwire walk's stop-on-unknown + "reached the searched subtree" guard, and the Semi/Anti merged-row width handling, are correct. No bug found.
- **equiv_class.go** — union-find, `orderedPair` canonicalisation, type/source-table-aware identity, and deterministic ordering are correct. No bug found.
- **exists_to_any.go** — the OR-only/qual-position gating (NULL vs FALSE), single-correlation accounting, and decline-before-mutation ordering are correct; `escapes`/Level accounting checked. No bug found.
- **expr_result_type.go** — `exactTypeOID`'s distrust-the-text-fallback guard and the pg_operator/pg_proc lookups are sound. No bug found.
- **exprkey.go** — `structuralKeyWriter` length-prefixes strings, sorts map keys, path-marks cycles, and skips unexported `pos` fields; correct. No bug found.

Note on exprwalk.go: the only finding is the FuncCall tail omission above; the three drivers themselves (walk/clone/rewrite) and the identity driver's fail-closed contracts are correct.
