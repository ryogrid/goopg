# 0128-0001 — Bitmap Heap Scan: Design

| field | value |
| --- | --- |
| status | **draft** — filed 2026-08-07 |
| date | 2026-08-07 |
| milestone | M0128 — Special-join inference + M0127 residuals |
| supersedes | none |
| upstream reference | `postgres/src/backend/nodes/tidbitmap.c`, `postgres/src/backend/executor/nodeBitmapIndexscan.c`, `postgres/src/backend/executor/nodeBitmapHeapscan.c`, `postgres/src/backend/optimizer/path/costsize.c:1009-1208`, `postgres/src/include/nodes/tidbitmap.h` |

## 1. Motivation

goopg has zero bitmap machinery. The planner chooses between SeqScan and
IndexScan on cost alone — there is no middle ground where a bitmap index scan
accumulates TIDs from multiple index probes, ORs/ANDs them, and then visits
the heap in physical (page-sorted) order.

PostgreSQL uses bitmap scans pervasively. In the TPC-H SF1 reference capture
`parallel-query/` analysed, PG 18.3 chose a bitmap scan for at least one
relation in **Q4, Q5, Q7, Q8, Q9, Q10, Q12, Q14, Q17, Q18, Q19, Q20, Q21,
and Q22** — 14 of 22 queries. The shape is always a single-index bitmap
(`Bitmap Index Scan → Bitmap Heap Scan`); PG's BitmapAnd/BitmapOr (multiple
indexes combined per relation) appear in the TPC-DS corpus.

Bitmap scans matter for correctness AND performance:

- **Correctness**: a composite B-tree index key prefix may match rows PG
  locates via separate smaller indexes — the planner cannot fold an arbitrary
  WHERE combination into one `Key`/`Keys` probe if no single index covers
  all conditions.
- **Performance**: an IndexScan fetches heap tuples in index order (random
  physical I/O) and re-fetches the same heap page for each index entry that
  points to it. A bitmap scan visits each heap page **at most once**, reading
  the page, extracting all matching tuples from it, then moving to the next
  physical page. For an index returning >~1–2% of a relation, this is the
  dominant effect.

The goopg analogue of PG's `table_scan_bitmap_next_tuple` / `heapam_bitmap_next_tuple` will reuse goopg's existing `scanPage` page-level decode logic, augmented with a bitmap-driven tuple-visibility filter.

## 2. Architecture overview

PG's bitmap scan is three cooperating components:

```
BitmapIndexScan ──→ TIDBitmap ──→ BitmapHeapScan
  (index probe)     (accumulator)    (heap visitor)
```

### 2.1 TIDBitmap (`tidbitmap.c`)

The TIDBitmap is a sparse, page-keyed set of TIDs. It stores:

- **Exact pages**: a per-page bitset (one bit per `OffsetNumber` on that
  page). Bit *k* represents tuple offset *k+1*.
- **Lossy pages**: when memory is exhausted (`maxbytes`, derived from
  `work_mem`), the bitmap degrades to one-bit-per-page — the page must be
  visited, but every tuple on it must be rechecked against the index qual
  (the `recheck` flag is forced true).

Key API:
- `tbm_create(maxbytes)` — allocate a new empty bitmap. `maxbytes` is the
  memory ceiling; when exceeded, `tbm_lossify()` degrades heaviest pages to
  lossy until the total is under budget.
- `tbm_add_tuples(tbm, tids, ntids, recheck)` — insert TIDs from one index
  probe. `recheck` is set when the index AM cannot guarantee the row
  satisfies the index qual (e.g. GiST, GIN, or lossy index conditions).
- `tbm_union(tbm, other)` / `tbm_intersect(tbm, other)` — OR/AND two
  bitmaps. Intersection of an exact page and a lossy page forces the result
  to be lossy.
- `tbm_begin_iterate` → `tbm_iterate` → `tbm_end_iterate` — iterator
  producing `TBMIterateResult{blockno, lossy, recheck, internal_page}`.
  Results are emitted in **block-number order** (sorted ascending), which
  is the physical ordering guarantee.

Internal structure (PG `struct TIDBitmap`, tidbitmap.c:140-161):
- A pagetable hash (open-addressing, block-number keyed) of
  `PagetableEntry` items, each containing `blockno`, `ischunk` (lossy
  flag), `recheck`, and a `bitmapword[]` — either `WORDS_PER_PAGE` words
  for exact pages, or `WORDS_PER_CHUNK` for lossy chunks.
- Three-state escalation: `TBM_EMPTY` → `TBM_ONE_PAGE` (single-entry
  fast path, no hash table allocated) → `TBM_HASH`.
- `nentries`, `maxentries`, `npages` (exact), `nchunks` (lossy).
- On `tbm_begin_iterate`, the hash entries are sorted into `spages[]` and
  `schunks[]` arrays; iteration walks both in merged physical order.
- `tbm_extract_page_tuple(iteritem, offsets, max_offsets)` produces the
  sorted list of `OffsetNumber`s for an exact page in one call.

### 2.2 BitmapIndexScan (`nodeBitmapIndexscan.c`)

A bitmap index scan is an index probe that feeds a TIDBitmap instead of
returning heap tuples. In PG it uses the `MultiExecProcNode` convention (not
the standard `ExecProcNode` pull model) — the parent `BitmapHeapScan` calls
`MultiExecProcNode(outerPlan)` once to run the whole index scan and receive
the filled bitmap.

The key function (`MultiExecBitmapIndexScan`, :48-97):
1. Resolves runtime keys (parameterised values from outer query).
2. Creates (or reuses) a TIDBitmap sized to `work_mem`.
3. Calls `index_getbitmap(scandesc, tbm)` — the index AM's bitmap entry
   point — which runs the index scan and calls `tbm_add_tuples` for each
   matching entry.
4. Returns the filled TIDBitmap as a `Node *`.

### 2.3 BitmapHeapScan (`nodeBitmapHeapscan.c`)

A bitmap heap scan is a standard pull-model operator (`ExecProcNode` →
`ExecBitmapHeapScan` → `BitmapHeapNext`):

1. **Setup** (`BitmapTableScanSetup`, :62-117): calls
   `MultiExecProcNode(outerPlan)` to get the filled TIDBitmap, begins a
   bitmap iteration (`tbm_begin_iterate`), and creates a table scan
   descriptor with `table_beginscan_bm` (the bitmap-aware heap scan API).

2. **Per-tuple** (`BitmapHeapNext`, :125-172): calls
   `table_scan_bitmap_next_tuple` which, under the hood:
   - Advances the TBM iterator page by page.
   - Pins the page.
   - For exact pages: walks the per-offset bitmap, fetches each matching
     tuple.
   - For lossy pages: walks every tuple on the page.
   - Sets `recheck` = true for lossy pages OR when the index AM required
     recheck on this entry.

3. **Recheck**: when `recheck` is true, the bitmap's original index qual
   (`bitmapqualorig`) is evaluated against the fetched tuple — only
   matching tuples are returned. This is the **MVCC safety net**: an
   index entry may still exist for a tuple the heap no longer contains (or
   whose xmin/xmax make it invisible), and a lossy page fetches every tuple
   regardless.

4. **Instrumentation**: `BitmapHeapScanState.stats` accumulates
   `exact_pages` and `lossy_pages` counters; EXPLAIN ANALYZE renders them
   as `Heap Blocks: exact=N lossy=M`.

### 2.4 Plan tree shape

```
BitmapHeapScan
  ├── bitmapqual: the original index qual (recheck predicate)
  └── outer: BitmapIndexScan (or BitmapAnd/BitmapOr tree)
        └── index scan on <index>
```

In PG's `create_bitmap_scan_path` (pathnode.c), the `bitmapqual` is a tree
whose leaves are `IndexPath` nodes and whose internal nodes are
`BitmapAndPath`/`BitmapOrPath`. A single-index bitmap is just one
`IndexPath` as the entire tree.

## 3. goopg design

### 3.1 TID bitmap data structure

goopg's TID bitmap is simpler than PG's, because goopg does not need DSA
(shared memory for parallel workers) — parallel workers share a Go pointer
behind `ParallelGroup.Go`'s barrier.

```go
// internal/executor/tidbitmap.go

// TIDBitmap is a sparse, page-keyed set of tuple IDs.
// Zero value is an empty bitmap ready for use.
type TIDBitmap struct {
    // entries maps BlockNumber → pageEntry. nil until the first insert.
    entries map[uint32]*pageEntry
    // maxEntries is the soft memory ceiling; when exceeded, the heaviest
    // exact page is degraded to lossy. 0 = unlimited.
    maxEntries int
    // npages counts exact entries; nchunks counts lossy entries.
    npages  int
    nchunks int
}

type pageEntry struct {
    block   uint32          // block number (key)
    isLossy bool            // true → visited entirely; bitmap ignored
    recheck bool            // true → evaluate original index qual per tuple
    // bitmap[k/8] & (1<<(k%8)) means OffsetNumber (k+1) is present.
    // Only used when isLossy == false. Size = ceil(MaxOffsetNumber/8).
    bitmap []byte
}
```

Key operations:
- `tbmAddTuples(tbm *TIDBitmap, tids []ItemPointerData, recheck bool)` —
  OR the given TIDs into the bitmap. Creates/updates page entries.
- `tbmUnion(tbm, other *TIDBitmap)` — OR another bitmap in.
- `tbmIntersect(tbm, other *TIDBitmap)` — AND with another bitmap. An
  exact page vs a lossy page → lossy result.
- `tbmIsEmpty(tbm *TIDBitmap) bool`
- `tbmLossify(tbm *TIDBitmap)` — when `len(entries) > maxEntries`,
  find the page with the most tuple bits set, promote it to lossy,
  repeat until under budget.

Iterator:
```go
type tbmIterator struct {
    tbm     *TIDBitmap
    blocks  []uint32        // sorted block numbers
    idx     int
    // For the current exact page:
    offsets []uint16       // sorted offset numbers extracted from bitmap
    offIdx  int
}

func tbmBeginIterate(tbm *TIDBitmap) *tbmIterator
func (it *tbmIterator) next() (block uint32, offset uint16, lossy, recheck bool, ok bool)
```

The iterator:
1. Sorts `tbm.entries` keys on first call.
2. For lossy pages: yields `(block, 0, true, true, true)`, then
   `(block, 1, true, true, true)` ... caller uses block-only (the
   heap-scan loop fetches the page once and visits every offset).
3. For exact pages: extracts sorted offsets from the bitmap, yields each
   `(block, offset, false, entry.recheck, true)`.

### 3.2 Plan nodes

Three new plan node types in `internal/planner/plan.go`:

```go
// BitmapIndexScan is a leaf node: scan one index and produce a TIDBitmap.
// It is NEVER executed via the standard pull-model Next() — it is a
// MultiExec-style whole-result producer called once by BitmapHeapScan.
type BitmapIndexScan struct {
    pos   int
    Table *catalog.Table
    Alias string
    Index *catalog.Index
    Key   Expr  // single-column equality
    Keys  []Expr // multi-column equality (M0054 pattern)
    // Pred is the full index condition (for recheck). When Key/Keys
    // cover only a prefix, Pred holds the remaining index quals.
    Pred      []Expr
    schema    Schema
}

// BitmapHeapScan reads a relation via a TID bitmap produced by its outer
// (a BitmapIndexScan or BitmapAnd/BitmapOr tree).
type BitmapHeapScan struct {
    pos   int
    Table *catalog.Table
    Alias string
    // BitmapQual is the original index qual, re-evaluated per tuple when
    // the bitmap entry is lossy or the index AM requires recheck.
    BitmapQual []Expr
    schema     Schema
}

// BitmapAnd / BitmapOr combine multiple bitmap sub-trees.
// BitmapAnd intersects; BitmapOr unions.
type BitmapAnd struct {
    pos    int
    Inputs []Node // []*BitmapIndexScan or nested []*BitmapAnd/[]*BitmapOr
    schema Schema
}

type BitmapOr struct {
    pos    int
    Inputs []Node
    schema Schema
}
```

These are `PlanNode` enum arms added to `internal/planner/plannode.go`.

### 3.3 Executor operators

**`bitmapIndexScanOp`** (`internal/executor/operators_bitmap.go`):

Implements the MultiExec convention — it has no `Next(slot)` in the
standard sense. Instead it exposes:

```go
type bitmapIndexScanOp struct {
    plan *planner.BitmapIndexScan
    ctx  *Context
}

// buildBitmap runs the index scan and returns a filled TIDBitmap.
func (o *bitmapIndexScanOp) buildBitmap() (*TIDBitmap, error)
```

`buildBitmap`:
1. Opens the B-tree index (reuses the existing `btree.RangeScan` /
   `btree.Probe` path).
2. Runs the scan.
3. For each matching index entry, calls `tbmAddTuples` with the TID.
4. Returns the bitmap.

The `recheck` flag for `tbmAddTuples` is set to `true` when the index
scan used only a prefix of the index key (the remaining key columns'
conditions must be checked against the heap tuple) — matching PG's logic
where `index_getbitmap` sets `recheck` based on
`scandesc->xs_recheck`.

**`bitmapHeapScanOp`** (`internal/executor/operators_bitmap.go`):

Standard `Operator` with `Open`/`Next`/`Close`:

```go
type bitmapHeapScanOp struct {
    plan       *planner.BitmapHeapScan
    ctx        *Context
    tbl        *catalog.Table
    rel        storage.RelFileNode

    tbm     *TIDBitmap
    iter    *tbmIterator

    // Current page state
    pageBuf   []byte
    pageBlock uint32
    pageLossy bool

    // scanRow is reused per Next() — one allocation per page, zero per tuple.
    scanRow Row

    // Stats
    exactPages  int64
    lossyPages  int64
    filterLost  int64   // rows removed by BitmapQual recheck
}
```

`Next(slot)`:
1. If `tbm` is nil, call `outer.buildBitmap()` to get the TIDBitmap and
   begin iteration.
2. Advance the TBM iterator to the next tuple TID.
3. If the page changed: pin and read the new page via the buffer pool
   (`ctx.BufPool.Pin(rel, block)`). Increment `exactPages` or
   `lossyPages`.
4. Decode the tuple at the given offset into `scanRow`.
5. If the page is lossy OR the entry has `recheck` set: evaluate
   `plan.BitmapQual` against `scanRow`. If it fails, increment
   `filterLost` and loop to step 2.
6. Return the slot filled with `scanRow`.

The page-pinning strategy: the TBM iterator visits pages in ascending
block order, so `bitmapHeapScanOp` pins each page once, extracts all
matching tuples, and unpins before moving to the next block. This is the
"one visit per page" guarantee — the core performance advantage over
IndexScan.

For lossy pages: the iterator visits every `OffsetNumber` on the page.
The heap scan op decodes each one, evaluates `BitmapQual`, and returns
only those that pass. This is PG's exact algorithm in
`heapam_bitmap_next_tuple`.

**EXPLAIN ANALYZE** counters:
```
Bitmap Heap Scan on lineitem  (cost=... rows=... width=...)
  Heap Blocks: exact=3742 lossy=128
  -> Bitmap Index Scan on l_orderkey_idx  (cost=... rows=... width=...)
```

The `Heap Blocks:` line is emitted when `exactPages > 0 || lossyPages > 0`.
`Rows Removed by Filter:` uses the existing P5.2 mechanism.

### 3.4 Path types and cost model

New `PathKind` values in `internal/planner/path.go`:

```go
PathBitmapHeapScan
PathBitmapIndexScan
PathBitmapAnd
PathBitmapOr
```

New path payloads:

```go
// BitmapHeapPath is the outer container — it is what add_path considers.
// Its cost is the sum of the index-bitmap cost (from BitmapQual.TotalCost())
// plus the heap-access cost.
type BitmapHeapPath struct {
    BitmapQual Path // an IndexPath, BitmapAndPath, or BitmapOrPath
}
```

`BitmapAndPath` / `BitmapOrPath` carry `[]Path` children and a combined
selectivity.

Costing (in `internal/planner/cost_funcs.go`):

- **`costBitmapIndexScan`**: identical to `costIndexScan` (the index probe
  itself costs the same regardless of whether TIDs go into a bitmap or
  directly to the heap). Plus PG's small per-tuple bitmap-manipulation
  charge (`0.1 * cpu_operator_cost * rows`).

- **`costBitmapHeapScan`**: ports PG's `cost_bitmap_heap_scan`
  (costsize.c:1009-1115):
  1. `pages_fetched = compute_bitmap_pages(...)` — estimates the number
     of distinct heap pages the bitmap will visit, given the index's
     selectivity and the relation's correlation/clustering. For goopg v1,
     use the Mackert-Lohman formula PG uses (costsize.c:776-878):
     `pages_fetched = (index_pages + baserel_pages) * (1 - exp(-indexSelectivity * baserel_pages))`...→ simplified in PG to
     `pages_fetched = (2^index_pages_fetched * tuples_fetched / T)` where T
     is the table page count.
  2. Per-page cost: interpolates between `random_page_cost` (few pages)
     and `seq_page_cost` (nearly the whole table) using `sqrt(pages/T)`.
  3. CPU cost: `cpu_per_tuple * tuples_fetched` — PG charges the full
     restriction qual per tuple (conservative, assuming lossy recheck
     may be needed).

- **`costBitmapAnd`** / **`costBitmapOr`**: ports PG's
  `cost_bitmap_and_node` (costsize.c:1165-1201) /
  `cost_bitmap_or_node` (costsize.c:1208-1253). AND multiplies
  selectivities; OR unions them with the inclusion-exclusion formula.
  Each intersection/union charges `100 * cpu_operator_cost`.

`compute_bitmap_pages` in goopg: PG's formula (costsize.c:776-908) is the
Mackert-Lohman estimator:

```
tuples_fetched = selec × baserel->tuples
pages_fetched  = 2 × T × tup / (2 × T + tup)    (costsize.c:863)
```
where `T = baserel->pages`. This is the single-term form — PG also has a
two-term form when `indexCorrelation` is available (`costsize.c:871-900`),
which goopg cannot use yet.

For v1, use PG's single-term Mackert-Lohman directly, plus the lossiness
adjustment: when `maxentries < heap_pages`, estimate how many pages go
lossy and recompute `tuples_fetched` assuming a tuple is as likely to be
on an exact page as a page is to be exact (matching PG's logic in
`compute_bitmap_pages`, costsize.c:889-908).

```go
func computeBitmapPages(root *RelOptInfo, bitmapQual Path, loopCount float64, workMem int64) (
    pagesFetched, tuplesFetched float64, indexTotalCost float64) {

    cost_bitmap_tree_node(bitmapQual, &indexTotalCost, &selec)
    tuplesFetched = selec * root.Rows
    T := max(root.Pages, 1)

    // PG's single-term Mackert-Lohman (costsize.c:863):
    pagesFetched = (2.0 * T * tuplesFetched) / (2.0*T + tuplesFetched)

    // Lossiness adjustment: if the bitmap would overflow work_mem,
    // some pages become lossy and every tuple on them is fetched.
    maxEntries := tbmCalculateEntries(workMem)
    if float64(maxEntries) < T {
        lossyPages := max(0.0, T - float64(maxEntries)/2.0)
        exactPages := T - lossyPages
        exactFraction := exactPages / T
        // Recompute: tuples on exact pages found via bitmap;
        // tuples on lossy pages = all tuples on those pages.
        tuplesFetched = exactFraction*tuplesFetched + lossyPages*max(root.Rows/T, 1.0)
    }
    return
}
```

The v1 cost model defers the two-term Mackert-Lohman formula
(`costsize.c:871-900`) — it requires `indexCorrelation` (the
`pg_stats.correlation` statistic), which goopg does not collect yet. A
ledger row records this.

### 3.5 Planner integration

**Path generation** (`internal/planner/pathgen.go`):

`createIndexPaths` (which currently generates `PathIndexScan` and
`PathIndexOnlyScan`) gains a third arm:

For each usable index on the relation, optionally generate a
`BitmapHeapPath` wrapping a `BitmapIndexScan`-equivalent `IndexPath`.
The bitmap path is NOT generated when the index returns a single row
(cheaper as plain IndexScan).

The bitmap path is always generated — PG's `create_index_paths`
(indxpath.c) generates both indexscan and bitmap paths for every index,
and `add_path` keeps the cheaper one in each cost regime.

**Plan creation** (`internal/planner/plan.go`, `createPlanNode`):

New `PathKind` arms in `createPlanNode`:
- `PathBitmapHeapScan` → construct `BitmapHeapScan{Table, Alias, BitmapQual}`
  with the outer plan built from `BitmapQual`.
- `PathBitmapIndexScan` → construct `BitmapIndexScan{Table, Index, Key, Keys}`
  (the outer of the heap scan).
- `PathBitmapAnd` / `PathBitmapOr` → construct `BitmapAnd{Inputs}` /
  `BitmapOr{Inputs}`.

**Executor integration** (`internal/executor/`):

`buildRec` (in whichever file maps plan nodes to operators) gains arms for
`PlanBitmapHeapScan` → `*bitmapHeapScanOp` and
`PlanBitmapIndexScan` → `*bitmapIndexScanOp`.

A `bitmapIndexScanOp` under a `bitmapHeapScanOp` is wired specially:
`bitmapHeapScanOp.outer` is type-asserted to an interface
`bitmapProducer { buildBitmap() (*TIDBitmap, error) }` rather than the
standard `Operator`. The `bitmapIndexScanOp` has a no-op `Next` that
panics (it is never called via the pull model — matching PG's
`ExecBitmapIndexScan` which `elog(ERROR, "...does not support ExecProcNode")`).

### 3.6 Index AM glue

goopg's B-tree index AM (`internal/access/btree/`) already supports the
exact operation a bitmap index scan needs:

- **`(*BTree).RangeScanWithPos(lo, hi []byte, fn func(key []byte, ptr storage.ItemPointer, pos ScanPos) (bool, error)) error`** (`btree.go:3188`)
  emits every matching entry's key, TID, and physical leaf location.
  This is already used by `indexScanOp.Rescan` (operators_index.go) for
  the "TID-list-eager" pass that collects all matching TIDs before
  fetching any heap pages.

- **`(*BTree).RangeScan(lo, hi []byte, fn func(key []byte, ptr storage.ItemPointer) (bool, error)) error`** (`btree.go:3192`)
  is the simpler variant without scan positions.

The bitmap index scan operator calls `RangeScanWithPos` and feeds
`tbmAddTuples` per entry — no heap fetch, no tuple decode. This is a
thinner wrapper around the existing B-tree than `indexScanOp.Rescan`,
which additionally collects `ScanPos` entries for kill-list
maintenance and evaluates probe keys.

**Recheck flag**: the index scan sets `recheck=true` in `tbmAddTuples`
when the index qual is not fully proven by the index entry alone —
matching PG's `xs_recheck` on `IndexScanDesc`. In goopg v1 (B-tree
only), recheck is needed when:

1. The scan uses a prefix of a composite index key (remaining key
   columns' conditions must be checked against the heap tuple).
2. The index has a partial-index predicate (the predicate must be
   re-evaluated against the heap tuple — PG's `predicate_implied_by`
   may still recheck).

**MVCC constraint**: PG requires an MVCC snapshot for any bitmap scan
(`nodeBitmapHeapscan.c`'s `ExecInitBitmapHeapScan` asserts
`IsMVCCSnapshot`). The reason: index and heap scans are decoupled, so
the index entry that prompts a heap visit might no longer exist, or
its TID might have been recycled by a concurrent UPDATE. goopg's MVCC
is pure snapshot comparison (no snapshot-type distinction), and
`indexScanOp.Next` already handles the "index entry exists, heap tuple
is dead/invisible" case via `followHOTChainNoCopy` + visibility check
— the bitmap heap scan inherits the same safety net.

**Page pin/fetch per TID**: the existing `indexOnlyScanOp.Open`'s heap
fetch fallback (operators_indexonly.go:181-210) is the model:
```go
slot, _ := ctx.Pool.Pin(storage.BufferTag{Rel: heapRel, Block: ptr.Block})
defer slot.RUnlock()
page := storage.PageGetItemID(pageBuf, ptr.Offset)
// followHOTChain / visibility check / decode
```
The bitmap heap scan uses the identical sequence, amortised: it pins each
page once and extracts all matching tuples before unpinning.

For GiST/GIN (future): the index AM must expose a `getBitmap` entry
point. This is tracked as a deferral.

### 3.7 Parallel bitmap scan

Deferred to a follow-up. PG's parallel bitmap heap scan
(`ParallelBitmapHeapState`, `nodeBitmapHeapscan.c:46-110`) partitions the
TBM iterator across workers — each worker claims a range of pages from the
sorted iterator via a shared atomic counter. goopg's `ParallelGroup` and
existing `ParallelScanState` provide the same infrastructure. The design
keeps the TBM iterator in the local executor (no DSA needed — the leader
builds the bitmap and passes the `*TIDBitmap` pointer to workers behind
the `ParallelGroup.Go` barrier).

## 4. GUC interactions

- `enable_bitmapscan` (bool, PG default `on`): controls whether
  `BitmapHeapPath` is considered in `add_path`. When off,
  `path.disabled_nodes` is incremented and the path is dominated by any
  path without disabled nodes (PG 18's `compare_path_costs_fuzzily` rule,
  already reproduced in goopg's `add_path`).
- `work_mem`: sizes `TIDBitmap.maxEntries` — the memory ceiling before
  lossification. PG derives `maxbytes = work_mem * 1024`; goopg will use
  the same formula.

## 5. EXPLAIN output

### 5.1 TEXT format (non-ANALYZE)

```
Bitmap Heap Scan on lineitem l  (cost=X..Y rows=Z width=W)
  Recheck Cond: (l.l_orderkey = 1)
  ->  Bitmap Index Scan on l_orderkey_idx  (cost=X..Y rows=Z width=0)
        Index Cond: (l_orderkey = 1)
```

### 5.2 TEXT format (ANALYZE)

```
Bitmap Heap Scan on lineitem l  (cost=X..Y rows=Z width=W) (actual time=A..B rows=C loops=D)
  Heap Blocks: exact=3742 lossy=128
  Recheck Cond: (l.l_orderkey = 1)
  Rows Removed by Filter: 15
  ->  Bitmap Index Scan on l_orderkey_idx  (cost=X..Y rows=Z width=0) (actual time=A..B rows=C loops=D)
        Index Cond: (l_orderkey = 1)
```

The `Heap Blocks:` line is emitted by the heap scan node when
`exactPages > 0 || lossyPages > 0`. It mirrors PG's output exactly.

`Rows Removed by Filter:` on the index scan node counts recheck
rejections — TIDs the index AM found but the recheck qual rejected.
`Rows Removed by Filter:` on the heap scan node counts tuples fetched
from the heap that the bitmap's original qual (`BitmapQual`) rejected.

### 5.3 JSON/XML/YAML

```json
{
  "Node Type": "Bitmap Heap Scan",
  "Heap Blocks": {"exact": 3742, "lossy": 128},
  ...
}
```

## 6. Deferrals and ledger items

| what | why | resume point |
|------|-----|-------------|
| Full Mackert-Lohman two-term formula with `indexCorrelation` | goopg does not collect `pg_stats.correlation`; v1 uses the single-term formula `2*T*tup/(2*T+tup)` (costsize.c:863) plus lossiness adjustment, without correlation | `internal/planner/cost_funcs.go` `computeBitmapPages` — upgrade when ANALYZE collects correlation |
| BitmapAnd / BitmapOr path generation | goopg currently has no AND/OR index-path combination (the TPC-H corpus uses only single-index bitmaps); PG's `choose_bitmap_and` (indxpath.c:1785) greedily selects AND-able indexes | `internal/planner/pathgen.go` `createIndexPaths` — add when the pg_index intersection/union stats become available |
| GiST/GIN `getBitmap` index AM entry point | goopg has no GiST/GIN index AM at all; the B-tree is the only index today; PG dispatches through `index_getbitmap` → `amgetbitmap` | `internal/access/` — add alongside the GiST/GIN AM implementations |
| Parallel bitmap scan | PG's `ParallelBitmapHeapState` / shared TBM iterator partitions sorted pages across workers via an atomic counter under an LWLock; goopg's `ParallelGroup` + `ParallelScanState` provide the same infrastructure without DSA | `internal/executor/operators_bitmap.go` `bitmapHeapScanOp` — follow-up after P2.3 |
| `tbm_extract_page_tuple` bulk-offset extraction | PG extracts all offsets for a page in one call; goopg v1 iterates one-at-a-time through the iterator — simpler, same result, slightly more overhead | `internal/executor/tidbitmap.go` `tbmIterator.next` — micro-optimisation, not correctness |
| Read-stream prefetch for bitmap heap scan | PG 18 uses a read-stream architecture (`read_stream_begin_relation` + `bitmapheap_stream_read_next`) that feeds the I/O layer a window of future blocknos for prefetching; goopg v1 pins pages synchronously one at a time | `internal/executor/operators_bitmap.go` — follow-up, gated on a general I/O prefetch layer |
| Partial-index predicate recheck | PG's `create_bitmap_subplan` appends index predicate conditions to `bitmapqualorig` so partial-index quals are re-evaluated when the scan goes lossy; goopg v1's B-tree-only scope means partial indexes are deferred | `internal/planner/createplanjoin.go` `createBitmapSubplan` — add when partial indexes land |
| Missing `pg_stats.correlation` statistic collection (separate item) | Blocks the full Mackert-Lohman formula; also needed for `costIndexScan` correlation adjustment | `internal/executor/operators_analyze.go` — M0128-P3.1's `avgVarBytes` is the nearest neighbour; file as a separate ledger item |

## 7. Implementation staging

This is split into three tasks already in fix_plan.md:

- **P2.2** (this doc): design. Status `draft`, bar = doc review.
- **P2.3**: executor — `TIDBitmap` + `bitmapIndexScanOp` + `bitmapHeapScanOp` +
  EXPLAIN ANALYZE counters. Bar: UNITS + RACE + unit tests for exact/lossy
  transition and recheck.
- **P2.4**: planner — `BitmapHeapPath`/`BitmapIndexPath` types, cost functions,
  path generation in `createIndexPaths`, `createPlanNode` arms, `buildRec`
  arms. Bar: UNITS + SPOT + DS05 (zero row/checksum deltas; plans
  adjudicated) + PLAN + a TPC-H A/B on queries PG plans bitmap paths for.

The executor (P2.3) is designed to work **planner-invisible** until P2.4
lands — `buildRec` maps the plan node to the operator, but no path
generation produces it yet, so no existing query changes.

## 8. Testing strategy (P2.3 + P2.4)

### P2.3 unit tests
- `TestTIDBitmapEmpty` — empty bitmap, `IsEmpty()==true`, iterator yields none.
- `TestTIDBitmapExactSingle` — add one TID, iterate, verify block + offset.
- `TestTIDBitmapExactMultiple` — add many TIDs across multiple pages, verify
  iteration order is block-ascending, offsets within a page are ascending.
- `TestTIDBitmapLossify` — create at maxEntries=2, add 100 distinct TIDs across
  100 pages, verify some entries are lossy.
- `TestTIDBitmapUnion` — two bitmaps, union, verify combined contents.
- `TestTIDBitmapIntersectExact` — two exact bitmaps, intersect, verify only
  common TIDs remain.
- `TestTIDBitmapIntersectLossy` — exact AND lossy → lossy result.
- `TestBitmapHeapScanExact` — create a small test table with an index, scan
  via a manually-constructed bitmap, verify row counts.
- `TestBitmapHeapScanLossy` — force lossy by setting a tiny maxEntries,
  verify recheck still returns correct rows.
- `TestBitmapHeapScanRecheck` — verify recheck-filtered rows are correctly
  excluded when the index qual is stricter than the bitmap entries.
- `TestExplainAnalyzeBitmapHeapScan` — golden test for EXPLAIN ANALYZE
  output including `Heap Blocks:` line.

### P2.4 integration tests
- `TestBitmapPathGenerated` — verify `createIndexPaths` generates a
  `BitmapHeapPath` for a relation with an index.
- `TestBitmapPathCost` — verify the bitmap path is cheaper than seq scan when
  selectivity is low enough, and seq scan wins when selectivity is high.
- TPC-H spot-check (`scripts/tpch-spotcheck.sh`) — Q12=2, Q13=35 must hold.
- TPC-DS SF0.5 (`scripts/tpcds-sf05-regression.sh sweep`) — zero row/checksum
  deltas.

## 9. Risks

1. **Heap-page repinning**: the TBM iterator visits each page once, but if a
   page is evicted from the buffer pool between the bitmap creation and the
   heap visit (for large scans where the bitmap build and heap traversal are
   separated by seconds), the page must be re-read from disk — a hidden cost
   PG amortises by sizing the bitmap to `work_mem`.
   *Mitigation*: the bitmap is built eagerly in `bitmapHeapScanOp.Open`
   immediately before heap traversal; the window is milliseconds, not seconds.

2. **Lossy-page tuple enumeration**: a lossy page visits every offset on the
   page, decoding each tuple. For a page with `MaxOffsetNumber` entries
   (typically ~256), this decodes ~256 rows to return perhaps 1-2 that match
   — the worst-case overhead of the lossy regime.
   *Mitigation*: `tbmLossify` picks the *heaviest* exact page to degrade
   first (most tuple bits set), so lossy pages are by construction the pages
   that were going to return the most tuples anyway.

3. **MVCC visibility during bitmap-to-heap window**: the index scan builds
   the bitmap using the same snapshot as the heap scan, and goopg's MVCC is
   lock-free (pure snapshot comparison), so a concurrent UPDATE between index
   scan and heap scan is invisible to the bitmap scan's snapshot — exactly
   as in PG.
   *No additional mitigation needed*.

4. **Missing enable_bitmapscan default**: if the GUC defaults to `off`
   (matching PG's real default of `on`), the bitmap path is never chosen
   and the feature is inert.
   *Mitigation*: register `enable_bitmapscan` with `BootVal = true` from
   the start.
