# 0036-0001 — Lazy Hash Join Materialization

**Status:** draft
**Parent milestone:** M0036
**Date:** 2026-05-02

## 1. Objective

Eliminate the `o.rows` materialization in hash joins. Instead of collecting all
joined rows in a slice during `Open()`, yield each joined row on demand via `Next()`.
The hash table and probe operator reference are stored as operator state.

## 2. Current Behaviour (M0035)

```
Open():
  drainRows(build side) → buildRows
  build hash table from buildRows
  for each probeRow from probeOp.Next():      # streaming probe
    matches = hashTable.lookup(probeRow.key)
    for each match:
      o.rows = append(o.rows, concatRows(probeRow, match))  # MATERIALIZES

Next():
  return o.rows[o.idx]; o.idx++
```

Problem: `o.rows` holds ALL output rows. For a join producing 800K rows,
this is ~1.4 GB of DeepCopy'd Row slices.

## 3. Proposed: Lazy Model (M0036)

```
struct joinOp:
  probeOp     Operator       # streaming probe source
  hashTable   map[string][]Row  # build side, keyed
  probeRow    Row             # current probe row (held across Next() calls)
  matches     []Row           # current matches for probeRow
  matchIdx    int             # index into matches
  inProgress  bool            # true when in middle of probeRow's matches
  leftWidth   int
  rightWidth  int

Open():
  drainRows(build side) → buildRows
  build hashTable from buildRows
  probeOp.Open(ctx)
  inProgress = false

Next():
  if inProgress:
    if matchIdx < len(matches):
      out = concatRows(probeRow, matches[matchIdx])
      matchIdx++
      return out
    inProgress = false
  # Pull next probe row
  probeRow, err = probeOp.Next()
  if err == EOF: return EOF
  key = evalHashKey(LeftKey, concatRows(probeRow, nullRight))
  matches = hashTable[key]
  matchIdx = 0
  if len(matches) == 0:
    if plan.Type == JoinTypeLeft:
      return concatRows(probeRow, nullRight)
    # No matches, not LEFT — skip to next probe row.
    goto pull_next
  inProgress = true
  out = concatRows(probeRow, matches[0])
  matchIdx = 1
  return out
```

### 3.1 Memory comparison

| State | Before | After | Notes |
|-------|--------|-------|-------|
| buildRows (drainRows) | 400 MB | 400 MB | Needed (build side) |
| hashTable (map) | 20 MB | **20 MB** | Same |
| **o.rows** | **1.4 GB** | **0** | **ELIMINATED** |
| probe state | 0 | ~1 KB | Current row + match index |

Peak per join: **420 MB** instead of **1.8 GB** — **77% reduction** in join operator memory.

### 3.2 LEFT JOIN semantics

When a probe row has zero matches and `plan.Type == JoinTypeLeft`:
- Emit `concatRows(probeRow, nullRight)` as the single output.
- Do NOT set `inProgress` (no further matches possible).
- The next `Next()` call pulls the next probe row.

### 3.3 Sort interaction

The sort operator (`sortOp`) still fully buffers in `o.rows` because sorting
requires random access. Q2 has a final `ORDER BY`, which needs the sort.
However, the sort input is only the filtered result (~2K rows for Q2, not 800K),
so the sort's `o.rows` is tiny.

### 3.4 Aggregate interaction

The aggregate operator gathers all input rows into groups in `Open()`. With
lazy hash join as a child, `aggregateOp.Open()` calls `child.Next()` in a loop,
pulling rows one at a time. This is already the existing pattern — no change
needed.

## 4. Implementation Plan

### 4.1 Changes to `joinOp` struct

Add fields:
```go
type joinOp struct {
    // existing fields...
    plan   *planner.Join
    left   Operator
    right  Operator
    schema planner.Schema
    ctx    *Context
    rows   []Row   // retained for nested-loop/merge; UNUSED for hash after M0036
    idx    int

    // M0036 lazy-output state (hash join only):
    probeOp    Operator         // streaming probe source (left or right child)
    hashTbl    map[string][]Row // build-side hash table
    lazyRow    Row              // current probe row for lazy emission
    lazyMatches []Row           // matches for current probe row
    lazyMatchIdx int
    lazyActive  bool            // true when mid-probe-row
    lazyLW      int             // left width (for null padding)
    lazyRW      int             // right width
    lazyBuildLeft bool          // true when left is build side
}
```

### 4.2 `Open()` changes

- `openStreamingHashJoin()` → renamed to `openLazyHashJoin()`.
- Build hash table from drained build side (same as M0035).
- Store `probeOp`, `hashTbl`, widths, `BuildLeft` flag.
- Set `lazyActive = false`.
- Do NOT drain the probe side.
- Do NOT populate `o.rows`.

### 4.3 `Next()` changes

- If `lazyActive`, continue serving matches from `lazyMatches`.
- Else, pull next probe row from `probeOp.Next()`.
- On EOF, return `EOF`.
- Look up hash table, set `lazyMatches` and `lazyMatchIdx`.
- Return joined row via `concatRows`.

### 4.4 `Close()` changes

- Nil all lazy state fields (`lazyRow = nil`, `lazyMatches = nil`, `hashTbl = nil`).
- Close left, right, and probeOp (same as left/right — probeOp IS one of them).

### 4.5 `Schema()` unchanged

Total 2 files changed. No planner changes.

## 5. Verification

### 5.1 Unit tests

- `TestLazyHashJoinINNER`: 3-table join with 100+ rows, results match buffered.
- `TestLazyHashJoinLEFT`: LEFT JOIN with unmatched probe rows, results correct.
- `TestLazyHashJoinBuildLeft`: `BuildLeft=true` variant.
- `TestLazyHashJoinIntermediate`: Verify peak `o.rows` is zero for hash joins.
- `TestBuildTPCHQueries`: All 22 TPC-H queries execute.
- `TestPlanTPCHQueriesPlannable`: 22/22 plannable.

### 5.2 Integration

- Q2 on partial SF=1 data: completes, peak RSS < 15 GB.
- All 22 queries: no regressions from lazy output.

## 7. 2026-07-07 update: residual-filter-only misses dropped LEFT JOIN rows (M0119-0004)

**Symptom:** `pg_dump`'s `getTableAttrs` (`pg_attribute a LEFT JOIN pg_constraint co ON
(a.attrelid = co.conrelid AND co.contype = 'n' AND co.conkey = array[a.attnum])`)
failed with `invalid column numbering in table "pnnl_1"` — pg_dump's client-side
sequential-attnum check (`pg_dump.c:9370`) tripped because goopg's query silently
omitted the row for the table's first column whenever that column had no matching
`pg_constraint` row and a *different* column of the same table did (two rows sharing
the hash-join key `attrelid=conrelid`, only one of them also satisfying the residual
`conkey = array[attnum]` conjunct).

**Root cause:** `nextLazy`'s hash-match loop (§4.3) iterates every candidate in
`lazyMatches` and applies the join's full `Predicate` — necessary because the hash
key alone (`LeftKey`/`RightKey`) doesn't cover residual conjuncts the planner ANDed on
via `pushOneConjunct` (see the loop's own comment, e.g. TPC-H Q9's
`ps_partkey=l_partkey`). But the loop only recognised two outcomes: "yield a match"
(a candidate passes `Predicate`) or "the whole probe row has zero hash-level
candidates" (`len(matches) == 0`, handled separately below the loop, which correctly
null-pads for LEFT JOIN). It never handled the third case — **the hash key matched,
`lazyMatches` is non-empty, but every candidate fails the residual `Predicate`** — the
inner loop just exhausts, `lazyActive` goes false, and control falls straight into
"pull next probe row", silently dropping the outer row instead of null-padding it.
This is a genuine correctness bug (not restricted to system catalogs — reproduces with
plain user tables, e.g. `outer_t(id,k) LEFT JOIN inner_t(rid,k2) ON id=rid AND k=k2`
where two `outer_t` rows share the same `id` but only one shares `k`).

**Fix** (`internal/executor/operators_join_agg.go`): added a `lazyProbeMatched bool`
field, set `true` the moment any candidate in the current probe's `lazyMatches` passes
`Predicate`, and reset to `false` each time a new probe row's `lazyMatches`/`lazyActive`
are set up. When the per-match loop exhausts (`lazyActive` was true, now nothing left),
check `!lazyProbeMatched`: if the join is `LEFT` and `!BuildLeft` (the same direction
the pre-existing `len(matches)==0` branch already covers), emit the null-padded row for
`lazyRow` exactly as that branch does, instead of continuing to the next probe row.
`BuildLeft=true` LEFT JOINs are untouched (nextLazy's existing "preserve unmatched"
logic was already scoped to `!BuildLeft` before this fix; extending it to the
build-left direction needs the classic "track consumed build rows, emit leftovers at
EOF" algorithm, a separate, larger change, not attempted here since the planner does
not appear to combine `BuildLeft` with `JoinTypeLeft` in the reachable query shapes —
Semi/Anti already force `BuildLeft=false` a few lines above).

**Verification:** `internal/executor/leftjoin_hash_residual_dropped_row_test.go`
(`TestLeftJoinHashResidualFilterPreservesUnmatchedRow`) pins the minimal user-table
shape; confirmed non-vacuous via `git stash` on just this file (fails pre-fix with
"got 1 rows, want 2", passes post-fix). `internal/testport`'s
`TestPort_PgDumpConnectionSetup` DU-002 guard advances past the `invalid column
numbering` gap to the next (already-documented, milestone-scale) blocker. Full
`go test ./internal/executor/... ./internal/planner/...` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `TestPort_IsolationEvalPlanQual`/
`TestPort_IsolationEvalPlanQualTrigger` PASS (both exercise LEFT JOIN-heavy plpgsql
paths); `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0 failed
transactions, all three pgbench workloads).

## 6. Reference

- `internal/executor/operators_join_agg.go:20-29` — `joinOp` struct
- `internal/executor/operators_join_agg.go:40-75` — `Open()` (M0035 streaming)
- `internal/executor/operators_join_agg.go:87-152` — `runHashJoinStream` (M0035)
- `internal/executor/operators_join_agg.go:390-397` — `Next()` (index into o.rows)
- `internal/executor/operator.go:9-29` — `Operator` interface
- `analysis/tpch-streaming-hash-join-results.md` — Q2 o.rows bottleneck
