# M0148\-0001c3a2 — BitmapOr paths over OR restriction clauses

**Parent:** M0148\-0001c3a1 · **Status:** slice 1 landed 2026\-11\-12 \(BitmapOr producer \+ parallel lossy page fix\); the other c3a2 items stay open

## Problem

goopg had a BitmapOr plan node, an executor operator \(`bitmapOrOp`, a TBM
union\) and a cost function, but no path producer ever built a
`PathBitmapOr`. An OR restriction such as `unique1 = 1 OR unique2 = 3` was
always a Seq Scan with the OR as a Filter, where PG plans

```
Bitmap Heap Scan on tenk1
  Recheck Cond: ((unique1 = 1) OR (unique2 = 3))
  ->  BitmapOr
        ->  Bitmap Index Scan on tenk1_unique1
              Index Cond: (unique1 = 1)
        ->  Bitmap Index Scan on tenk1_unique2
              Index Cond: (unique2 = 3)
```

That accounted for 23 BitmapOr lines across the regress create\_index, join,
partition\_join and select diffs.

## PG behaviour \(the oracle\)

- `create_index_paths` \(indxpath.c\) calls `generate_bitmap_or_paths(root,
  rel, rel->baserestrictinfo, NIL)` and adds the results to `bitindexpaths`.
- `generate_bitmap_or_paths` goes through each OR restriction clause and
  works on the result of `group_similar_or_args`. For each arm:
  - **AND arm:** `build_paths_for_OR(andargs, all_clauses)`, plus a
    recursive call for the sub\-ORs.
  - **Grouped sub\-OR:** `make_bitmap_paths_for_or_group`, which builds a
    SAOP clause.
  - **Plain clause:** `build_paths_for_OR([arm], all_clauses)`.

  An arm that no index serves abandons the whole OR. Each arm's paths go
  through `choose_bitmap_and`, and the arms are combined by
  `create_bitmap_or_path`.
- `build_paths_for_OR`:
  - Skips an index unless the arm's own clauses match it
    \(`clauseset.nonempty`\) or the arm proves the index's partial predicate
    \(`useful_predicate`\).
  - Then adds the "other" restriction clauses to the clause set, so they can
    complete an index prefix: `thousand = 42 AND (tenthous = 1 OR …)` gives
    `Index Cond: ((thousand = 42) AND (tenthous = 1))`.
- `cost_bitmap_or_node` \(costsize.c\):
  - Selectivity is the sum of the arms' selectivities, capped at 1.
  - Cost is the sum of the arms' costs, plus `100 * cpu_operator_cost` per
    union **except where the input is an IndexPath**.
- `create_bitmap_subplan`'s BitmapOr arm \(createplan.c\):
  - Each child's qual becomes one AND \(`make_ands_explicit`\), and the arms
    are ORed together.
  - A Bitmap Index Scan child's qual is its index clauses, plus its partial
    predicate when those clauses do not imply it.
- `create_bitmap_scan_plan`: the Filter \(qpqual\) is every scan clause
  except those the recheck list \(indexquals\) contains or implies
  \(`predicate_implied_by`\):
  - a conjunct repeated in every arm is dropped;
  - the OR clause itself is dropped when every arm's qual covers its arm;
  - an arm the index serves only in part keeps the OR in the Filter.

## goopg implementation

- `optimizer/pathbitmap.go`:
  - `buildOneBitmapPath` is split in two:
    - `buildBitmapIndexChild` builds the Bitmap Index Scan path from an
      explicit conjunct list, i.e. build\_index\_paths' ST\_BITMAPSCAN arm;
    - `bitmapHeapPathOver` wraps any bitmap qual tree in the heap path,
      i.e. create\_bitmap\_heap\_path.
  - Plain bitmap paths behave exactly as before: the same conjuncts, and the
    index path keeps its lossy\-corrected row count.
  - `addBaseRelBitmapPaths` adds the OR heap paths beside the single\-index
    and AND paths.
- `optimizer/pathbitmapor.go`:
  - `bitmapOrHeapPaths` is generate\_bitmap\_or\_paths over the leaf's
    conjuncts.
  - `bitmapOrArmPath` is build\_paths\_for\_OR followed by choose\_bitmap\_and.
    It builds the clause context as arm clauses first, then the rel's
    conjuncts, so an arm clause wins its index column. It keeps an index
    only when the path uses an arm clause and every clause is exact
    \(`local` set\).
  - `bitmapOrArmsWouldGroup` declines an OR that PG 18 would partly fold
    into a SAOP group \(see Not covered\).
  - `bitmapArmSubqual` is create\_bitmap\_subplan's `*qual`.
  - `bitmapOrRecheck` builds the Recheck Cond and the set of dropped
    Filter conjuncts.
  - `orOfAndsImplies` / `andItemsImply` are the AND/OR rules of
    predicate\_implied\_by over `exprEqual` and
    `provePartialIndexPredicate` atoms.
- `optimizer/costbitmap.go`: `costBitmapOrCost` now applies PG's IndexPath
  exemption, and reads an index arm's own `Cost.Total`.
- `optimizer/createplanbitmap.go`: a `PathBitmapOr` child takes the
  `bitmapOrRecheck` arm instead of the single\-index recheck logic.

## Defect found on the way \(fixed here\)

`bitmapHeapScanOp.nextParallel` lost rows on **lossy** pages:

- **Symptom:** it returned a page's first match and fell out, so the next
  `Next()` claimed a new page and the rest of the page was dropped. A
  Parallel Bitmap Heap Scan therefore returned one row per lossy page.
- **History:** this is the same bug the exact\-page arm had, which was fixed
  earlier with the `parOffsets` cursor.
- **Evidence:** regress bitmapops' `a = 1` under `work_mem = 64` gave 1273
  rows, which is the lossy page count, against the true 1321. This
  reproduced on HEAD without this change. BitmapOr made the passing
  bitmapops case `a = 1 OR b = 1` return 1273 instead of 2485.
- **Fix:** the lossy walk is now a cursor across calls \(`inLossyPage` /
  `lossyOff`\), resumed at the loop head, as the serial path already did.
- **Test:** pinned by `TestBitmapOrOverOrClauses`'s parallel\-lossy cases,
  which fail with 1273 without the fix.

## Not covered \(ledgered, M0148\-0001c3a2 stays open\)

- **SAOP grouping:** PG 18's `group_similar_or_args` /
  `make_bitmap_paths_for_or_group`, where similar arms become one
  `col = ANY (…)` bitmap arm. The bitmap probe has no SAOP carrier, so such
  an OR gets no BitmapOr at all.
- **Sub\-ORs inside an AND arm:** the recursive `generate_bitmap_or_paths`
  call.
- **useful\_predicate:** an arm that proves a partial predicate without any
  index clause.
- **Combining with single\-index paths:** the OR path is not a candidate in
  `chooseBitmapAnd`, so `hundred = 42 AND (…)` cannot become
  BitmapAnd\(index, BitmapOr\). chooseBitmapAnd reads
  `Children[0].IndexInfo`.
- **Join OR clauses:** `bitjoinpaths`, i.e. generate\_bitmap\_or\_paths over
  `joinorclauses`.
- **Double per\-tuple charge:** `costBitmapTree` charges a Bitmap Index Scan
  the `0.1 * cpu_operator_cost * rows` term a second time
  \(costBitmapIndexScan already includes it\). This affects chooseBitmapAnd
  comparisons.
- **Bitmap node cost line:** Bitmap Index Scan EXPLAIN shows startup 0.29
  rather than PG's 0.00, a total that includes the per\-tuple charge, and
  width 84 rather than 0. BitmapOr width is also 84 rather than 0.
- **One bitmap heap path per rel:** PG adds only one, from choose\_bitmap\_and
  over every bitmap path. goopg adds each candidate separately.
- **subselect:** goopg rewrites `(a, b) IN (VALUES …)` into an OR, which now
  plans as a BitmapOr. PG never makes that rewrite and plans a Nested Loop
  over the VALUES. The divergence is older; the diff only changed shape.

## Verification

- `TestBitmapOrOverOrClauses`:
  - five PG 18.3 plans, byte\-for\-byte;
  - row equality against a non\-indexable twin, both exact and lossy;
  - parallel lossy cases.
- Probe on scratch PG \(port 5545\) against goopg: six EXPLAIN \(COSTS OFF\)
  shapes identical. Costs identical too on `unique1 = 1 OR unique2 = 3`
  \(8.59..15.91\).
- Regress A/B over 16 suites, HEAD worktree against candidate:
  - create\_index 3286→3271, join 17792→17724;
  - subselect 2452→2462 \(the VALUES divergence above\);
  - bitmapops still passes.
- TPC\-DS SF0.25 sweep: 99/99 plan shapes identical, PASS.
- tpch\-spotcheck: Q12/Q13 PASS.
- Unit gate: PASS.
