# 0122-0004 — RANGE window frame mode (non-offset bounds)

status: accepted
milestone: M0122-0004 (SQL language / executor features — window frame follow-up)

## Problem

Explicit window frame clauses (`{ROWS|RANGE|GROUPS} frame_extent
[frame_exclusion]`) parse structurally, and `ROWS` and `GROUPS` modes
execute end-to-end (see `docs/design/0020-0001-window-parser-and-ast.md`'s
Follow-up chain). `RANGE` was the last unimplemented frame mode: the
analyzer rejected any `RANGE` clause with `0A000`
("RANGE window frame units are not supported in v0"), so the default
frame — which is itself `RANGE UNBOUNDED PRECEDING AND CURRENT ROW` —
could never be spelled explicitly, and no cumulative/peer-based RANGE
frame could be selected.

`RANGE` differs from `ROWS`/`GROUPS` only in how a *value offset* bound
(`RANGE BETWEEN n PRECEDING AND m FOLLOWING`) is interpreted: the frame
includes every row whose ORDER BY column value lies within
`current_value ± offset`, which requires a type-aware `+`/`-`/`<`
operator lookup on the single ORDER BY column (the operator differs for
int/numeric/date/timestamp/interval, and offsets on `date`/`timestamp`
are `interval`-typed). That value arithmetic is the genuinely hard part
and is deferred.

The **non-offset** bounds — `UNBOUNDED PRECEDING`, `UNBOUNDED
FOLLOWING`, and `CURRENT ROW` — need no value arithmetic at all. In
`RANGE` mode `CURRENT ROW` means "the current row together with **all
its ORDER BY peers**" (rows equal on the ORDER BY key), exactly the
peer-group semantics `GROUPS` mode and the default frame already
compute. So `RANGE` restricted to those bounds is identical to
`GROUPS`-mode's non-offset behavior and to the default frame.

## Change

Land `RANGE` for the non-offset bound kinds only; keep `RANGE` with a
value offset rejected (`0A000`, deferred).

- **Analyzer** (`internal/analyzer/analyzer.go`, `validateWindowFrame`):
  a new `case parser.FrameModeRange` accepts the clause unless a
  `StartKind`/`EndKind` is `FrameBoundOffsetPreceding`/`Following`, in
  which case it raises `0A000`
  ("RANGE with a value offset ... is not supported in v0"). Unlike
  `GROUPS`, non-offset `RANGE` does **not** require an ORDER BY clause
  (without one, all rows are peers, so `RANGE ... CURRENT ROW` spans the
  whole partition — matching PostgreSQL). The bound-ordering checks
  (42P20) that follow are mode-independent and already applied.

- **Planner** (`internal/planner/planner.go`, `resolveWindowFrame`):
  unchanged — it already copied `Frame.Mode` through verbatim; only the
  now-stale "RANGE is rejected by the analyzer" comments on
  `plan.WindowAgg.Frame` / `plan.WindowFrame.Mode` were corrected.

- **Executor** (`internal/executor/operators_window.go`): `frameBounds`
  now dispatches `FrameModeRange` to the existing `frameBoundsGroups`
  alongside `FrameModeGroups`. Because the analyzer guarantees a
  `RANGE` frame reaching the executor carries only UNBOUNDED/CURRENT ROW
  bounds, `frameBoundsGroups`'s peer-group index arithmetic produces the
  correct value-peer frame (CURRENT ROW → the current row's whole peer
  group). The two group-bounds gating conditions
  (`needsValueGroupBounds` for the value window functions, and the
  `groupBounds` precompute in `evalExplicitFrameAggFuncs`) were extended
  to include `FrameModeRange` so `peerGroupBounds` is available.

No new executor arithmetic was written — the change is a dispatch
addition plus two gate extensions, reusing the peer-group machinery that
already backs `GROUPS` and the default frame.

## Verification

Every expectation was cross-checked against a real PostgreSQL 18.3
instance (initdb + a throwaway server) before hardcoding. Seed
`(grp,val)` = `(1,10),(1,10),(1,20),(1,30),(1,30),(2,100),(2,200)`:

- `RANGE BETWEEN CURRENT ROW AND CURRENT ROW` (the whole peer group):
  grp=1 sum `{20,20,20,60,60}`, count `{2,2,1,2,2}` — genuinely diverges
  from the equivalent ROWS frame (`{10,10,20,30,30}`, count all 1).
- `RANGE BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING`: grp=1
  `{100,100,80,60,60}`.
- `RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW` (default frame,
  spelled explicitly): `{20,20,40,100,100}` — identical to `GROUPS
  UNBOUNDED PRECEDING`.
- `RANGE` without ORDER BY: whole partition for every row (`100…`).

Tests:
- `internal/analyzer/analyzer_test.go`:
  `TestAnalyzeWindowFrameRangeOffsetRejected` (RANGE+offset → 0A000),
  `TestAnalyzeWindowFrameRangeNonOffsetAccepted` (all non-offset shapes,
  including no-ORDER-BY, analyze clean).
- `internal/executor/window_compat_test.go`:
  `TestCompatWindowExplicitRangePeers` (CURRENT ROW peers, CURRENT
  ROW→UNBOUNDED FOLLOWING, no-ORDER-BY whole partition),
  `TestCompatWindowRangeUnboundedPrecedingCumulative`.

Gates: `go build ./...` clean; `go test ./internal/analyzer/...
./internal/planner/... ./internal/parser/... ./internal/executor/` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke via the
pre-commit hook.

## Deferred

`RANGE` with a value offset bound (`RANGE BETWEEN n PRECEDING AND m
FOLLOWING`) — needs a per-ORDER-BY-column type-aware `+`/`-`/`<`
operator lookup (`in_range` support functions in upstream:
`postgres/src/backend/utils/adt/*_range.c` /
`window frame in_range` in `nodeWindowAgg.c`'s
`update_frameheadpos`/`update_frametailpos`). Recorded in the deferral
ledger. This is now the **only** open window-frame item; every other
frame mode and window function is implemented.
