# 0035-0001 — Streaming Hash Join Executor

**Status:** accepted
**Parent milestone:** M0035
**Date:** 2026-05-02

## 1. Objective

Modify `joinOp.Open()` and `runHashJoin`/`runHashJoinBuildLeft` so that only the
**build side** is drained into memory. The **probe side** streams one row at a
time through the hash table via `Operator.Next()`. This eliminates the probe-side
`drainRows` copy, cutting peak memory by approximately 50% at each hash join level.

## 2. Current Behaviour

`joinOp.Open()` (operators_join_agg.go:40):

```go
leftRows, err := drainRows(o.left)   // deep-copies ALL probe rows
rightRows, err := drainRows(o.right)  // deep-copies ALL build rows
runHashJoin(leftRows, rightRows, ...)
```

Both children are fully buffered via `drainRows` which deep-copies every Row:
```go
dup := make(Row, len(row))
copy(dup, row)
rows = append(rows, dup)
```

For Q2's bushy plan with 4 hash joins, the probe side of each join is the result
of the previous join — which has already been materialized into `o.rows` by the
child join's `runHashJoin`. Loading these into `leftRows`/`rightRows` via
`drainRows` creates a second copy.

## 3. Proposed Change

### 3.1 `joinOp.Open()` — drain build-side only

```go
func (o *joinOp) Open(ctx *Context) error {
    o.ctx = ctx
    if err := o.left.Open(ctx); err != nil {
        return err
    }
    if err := o.right.Open(ctx); err != nil {
        _ = o.left.Close()
        return err
    }
    leftWidth := len(o.left.Schema())
    rightWidth := len(o.right.Schema())

    if o.plan.Algo == planner.JoinAlgoHash {
        if o.plan.BuildLeft {
            buildRows, err := drainRows(o.left)
            return o.runHashJoinBuildLeftStream(buildRows, o.right, leftWidth, rightWidth)
        }
        buildRows, err := drainRows(o.right)
        return o.runHashJoinStream(o.left, buildRows, leftWidth, rightWidth)
    }
    // Merge and NestedLoop still need both sides buffered.
    leftRows, _ := drainRows(o.left)
    rightRows, _ := drainRows(o.right)
    ...
}
```

### 3.2 `runHashJoinStream` — probe left by streaming

```go
func (o *joinOp) runHashJoinStream(probeOp Operator, buildRows []Row, leftWidth, rightWidth int) error {
    // Build phase unchanged — hash the build side.
    rightHash := make(map[string][]Row, len(buildRows))
    leftPad := nullRow(leftWidth)
    for _, r := range buildRows {
        key, ok, err := o.evalHashKey(o.plan.RightKey, concatRows(leftPad, r))
        if !ok { continue }
        rightHash[key] = append(rightHash[key], r)
    }

    // Probe phase — stream from probeOp.
    rightZero := nullRow(rightWidth)
    for {
        l, err := probeOp.Next()
        if err == EOF { break }
        key, ok, err := o.evalHashKey(o.plan.LeftKey, concatRows(l, rightZero))
        matches := rightHash[key]
        if !ok { matches = nil }
        matched := false
        for _, r := range matches {
            matched = true
            o.rows = append(o.rows, concatRows(l, r))
        }
        if !matched && o.plan.Type == planner.JoinTypeLeft {
            o.rows = append(o.rows, concatRows(l, nullRow(rightWidth)))
        }
    }
    return nil
}
```

### 3.3 `runHashJoinBuildLeftStream` — symmetric

Build left, probe right by streaming `right.Next()`.

### 3.4 LEFT JOIN tracking

For LEFT JOIN, the probe side (left/preserved) must emit unmatched rows.
The streaming probe already tracks `matched` per-row, so unmatched rows
emit `concatRows(l, nullRight)`. This is identical to the current logic.

RIGHT/FULL join not supported in v0 hash join (planner only uses hash for
INNER and LEFT). No change needed.

### 3.5 `o.rows` lifecycle

When the probe side IS a `joinOp` (nested join), its `o.rows` holds the
entire child result. `probeOp.Next()` reads from `o.rows` sequentially.
After the parent join's `Open()` completes, the child's `o.rows` is NOT
closed — the parent's `o.rows` holds concatRows copies of child rows.
The child `o.rows` retains the originals, which is fine (owned by parent).

`Close()` still nils `o.rows` (M0031-0003 fix). The child operator's
`Close()` is called by the parent's `Close()`, freeing the child's
buffers.

## 4. Memory Impact

| Join | Before | After | Reduction |
|------|--------|-------|-----------|
| part ⋈ partsupp (200K/800K) | 180MB + 400MB = 580MB | 400MB (build only) | **31%** |
| result ⋈ supplier (800K/10K) | 1.1GB + 7MB | 7MB (build only) | **95%** |
| result ⋈ unnested (800K/200K) | 1.7GB + 40MB | 40MB (build only) | **93%** |

Overall: probe-side `drainRows` accounts for ~2.8 GB of the ~3.4 GB estimated
peak. Streaming eliminates this, reducing peak to ~600 MB (build sides only +
hash table overhead).

## 5. Implementation

### Files changed

| File | Change |
|------|--------|
| `internal/executor/operators_join_agg.go:40-75` | `joinOp.Open()` — drain build-side only for hash joins |
| `internal/executor/operators_join_agg.go:126-217` | Replace `runHashJoin`/`runHashJoinBuildLeft` with streaming variants |
| `internal/executor/operators_join_agg.go:653-667` | `drainRows` unchanged (still needed for build side, nested-loop, merge) |
| `internal/executor/operator.go` | No change — `Operator.Next()` interface unchanged |

### Tests

- `TestStreamingHashJoinINNER`: 2-table equijoin, verify results match pre-streaming.
- `TestStreamingHashJoinLEFT`: LEFT JOIN with unmatched rows on the left side.
- `TestStreamingHashJoinQ2Plan`: Build Q2's bushy plan shape, verify execution.
- Existing TPC-H test suite unchanged (22/22 queries).
