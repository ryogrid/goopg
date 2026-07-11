# RANGE value-offset window frame bounds (M0122-0004 follow-up)

Status: implemented (2026-07-11)
Supersedes: none (follow-up to commit `794872f4`, "RANGE frame mode for
non-offset bounds"); builds on `docs/design/0020-0001-window-parser-and-ast.md`.

## Problem

`794872f4` landed RANGE frame mode for the non-offset bounds (UNBOUNDED
PRECEDING/FOLLOWING, CURRENT ROW), which are purely peer-based and share the
GROUPS-mode executor path. The last remaining window-frame gap was RANGE with a
**value offset** bound:

```sql
SELECT sum(x) OVER (ORDER BY x RANGE BETWEEN 1 PRECEDING AND 1 FOLLOWING) ...
SELECT count(*) OVER (ORDER BY ts RANGE BETWEEN '1 hour' PRECEDING
                                            AND '1 hour' FOLLOWING) ...
```

Here a row is in the frame when its ORDER BY column value falls within
`current_value ± offset`, so the frame boundary is computed by comparing the
ORDER BY value against `current_value ± offset` — PostgreSQL's `in_range`
support functions. The analyzer previously rejected every RANGE value offset
with `0A000`.

## PostgreSQL semantics (the oracle)

Reference: `postgres/src/backend/parser/parse_clause.c` (`transformFrameOffset`,
the exactly-one-ORDER-BY-column check) and
`postgres/src/backend/executor/nodeWindowAgg.c`
(`update_frameheadpos` / `update_frametailpos`, RANGE `START_OFFSET` /
`END_OFFSET` branches).

1. **Exactly one ORDER BY column** is required (`42P20`,
   `ERRCODE_WINDOWING_ERROR`): "RANGE with offset PRECEDING/FOLLOWING requires
   exactly one ORDER BY column".
2. The offset value is evaluated once per query; it must be **non-null**
   (`22004`) and **non-negative** (`22013`,
   "invalid preceding or following size in window function").
3. Frame head / tail are found by scanning rows in ORDER BY order and applying
   the `in_range(val, base, offset, sub, less)` predicate, where `base` is the
   current row's value and `val` is the candidate row's value:
   - `sum = sub ? base - offset : base + offset`
   - result = `less ? val <= sum : val >= sum`
   - **START** bound: `sub = START_OFFSET_PRECEDING`, `less = false`; if the
     sort is DESC, flip both (`sub = !sub; less = true`). Frame head = first row
     where the predicate is true.
   - **END** bound: `sub = END_OFFSET_PRECEDING`, `less = true`; if DESC flip
     both. Frame tail (exclusive) = first row where the predicate is false.
   - CURRENT ROW is exactly offset 0: `sum = base`, so START CURRENT ROW = first
     peer (`val >= base` asc), END CURRENT ROW = one past last peer
     (`val <= base` asc). This lets CURRENT ROW share the offset path with a
     nil offset (no arithmetic, `sum = base`).
4. **NULL handling** (derived from the null branches of the two scan loops):
   - **Non-null current row:** null-valued rows are never in the frame (both
     leading NULLS FIRST and trailing NULLS LAST rows are skipped).
   - **NULL current row:** the frame is exactly the contiguous NULL peer block,
     regardless of NULLS FIRST/LAST — unless a side is UNBOUNDED, which extends
     that side to the partition edge.

## goopg design

All within `internal/executor/operators_window.go` plus one analyzer guard.

### Analyzer (`internal/analyzer/analyzer.go`, `validateWindowFrame`)

Replace the blanket `0A000` RANGE-offset rejection with the PG guard: when a
RANGE frame has an offset bound, require `orderByLen == 1`, else `42P20`. All
other bound-combination checks already run below and are unchanged.

### Executor

- `windowOp` gains `frameStartOffDatum` / `frameEndOffDatum Datum` (the RANGE
  offset values, evaluated once in `evalWindowFuncs`, mirroring the existing
  int64 `frameStartOff` / `frameEndOff` for ROWS/GROUPS). A new
  `resolveRangeOffset` evaluates + null-checks (`22004`) +
  negative-checks (`22013`) the offset while keeping its Datum (numeric / float
  / interval, not just int).
- `frameBounds` gains a RANGE-with-value-offset branch that delegates to a new
  `frameBoundsRange(pStart, pEnd, i)`. `frameBounds` and its four call sites
  (`evalExplicitFrameAggFuncs`, `first_value`, `last_value`, `nth_value`) now
  return an `error` (the value comparison / arithmetic can fail on a type with
  no `in_range` equivalent — reported as `0A000`, mirroring PG's
  "not supported for column type").
- `frameBoundsRange` scans the partition for the frame head and tail, mirroring
  the two nodeWindowAgg.c loops, including the NULL branches. The boundary value
  `sum = base ± offset` reuses the existing, tested `evalBinary(OpAdd/OpSub, …)`
  — which already implements `numeric ± numeric`, `date/timestamp ± interval`,
  etc., i.e. exactly the `in_range` type coverage — and the `val </>= sum`
  comparison reuses `compareDatum`.

Complexity is O(n²) per partition (a head/tail scan per row), matching the
existing frame-aggregate loop; window partitions are already materialized.

## Deferred (ledger)

- `in_range` for a column/offset type pair that `evalBinary` does not support
  (e.g. an exotic user type) surfaces as `0A000` at execution time rather than
  as PG's parse-time "not supported for column type" — goopg has no per-type
  `in_range` catalog registry, so the check is arithmetic-driven and lazy.
- Negative-interval offset detection uses a component-sign heuristic
  (`months<0 || days<0 || micros<0`), not PG's exact `interval` comparison
  against zero.

## Tests

- `internal/analyzer/analyzer_test.go`: RANGE offset with two ORDER BY columns →
  `42P20`; with one column → accepted.
- `internal/executor/window_compat_test.go`: numeric and (where the type system
  allows) timestamp/interval RANGE frames, ASC and DESC, PRECEDING/FOLLOWING and
  mixed bounds, cross-checked byte-for-byte against live PostgreSQL 18.3.
