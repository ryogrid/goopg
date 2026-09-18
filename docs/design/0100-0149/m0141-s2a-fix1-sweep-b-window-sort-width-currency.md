# M0141-S2a-fix1-sweep-b — narrow WINDOW's internal sort cost inputs

Status: **landed, measured no-movement** (2026-09-18). `Parent:
M0141-S2a-fix1-sweep`. `Kind: impl`.

## Problem (recon's own framing, `m0141-s2a-fix1-sweep.md`)

goopg's `windowOp` (`internal/executor/operators_window.go` `Open`) sorts
its input internally rather than taking pre-sorted input the way PG's
`nodeWindowAgg` does, so `costWindow` (`internal/optimizer/
windowsetoppaths.go:216-241`) folds PG's separate `create_sort_path` step
into itself instead of pricing it as a distinct path the way PG's
`create_one_window_path` (`postgres/src/backend/optimizer/plan/
planner.c:4620-4760`) does. PG prices that Sort from the already-narrow
`subpath->pathtarget->width`; goopg's `addWindowPaths`
(`windowsetoppaths.go:256-296`, before this change) fed `costWindow` `len(cols)`/
`nodeAvgVarBytes(cols)` from `cols := belowNode.Output()` — the FULL row of
the node one level below, never narrowed — and `sizeWindowRelFromNode`
(`windowsetoppaths.go:133-142`) made the same full-`Output()` choice for the
WINDOW rel's own published `NCols`/`AvgVarBytes`.

Unlike the ORDER BY Sort sweep-a fixed, no `InputTarget`-style stamp reads
into this cost-time call — `stampWindowInputTarget(windowNode, nil)`
(`planner.go:7232`, B-01c third cut) exists and derives a keep-set for each
`*WindowAgg`, but it is stamped **keys-only** (`above=nil`) at construction
time, strictly before `createWindowPaths` costs the chain, and it is
compute-only in the first place (payload stamp, no cost/plan consumer) — the
B-01c file header states this explicitly ("A future applying cut may narrow
the WindowAgg input from InputTarget; that cut is NOT this file"). This task
is that applying cut, for the cost-sizing sites only (no Project insertion,
no schema/plan change — same C1/no-behavior-change contract every prior
narrowing cut in this family used).

## Implementation

New file `internal/optimizer/window_sort_narrow.go`:

- `deriveWindowChainNarrowKeeps(chain, aboveNames, aboveKnown)` computes, for
  every node in the (bottom-up) window chain, the keep-set
  `addWindowPaths` should read at that level in place of
  `belowNode.Output()`'s full row: ascending positions into
  `chain[i].Child.Output()`. Computed **top-down** (`i := len(chain)-1` down
  to `0`) so a column three levels up still survives at level 0: at each
  step, `total = windowWindowInputNames(chain[i])` (the node's OWN
  PARTITION BY/ORDER BY/func-args/filter/frame-offset columns — reused
  directly from `window_input_target.go`, the same helper
  `stampWindowInputTarget`'s keys-only stamp already computes) unioned with
  `need` (everything needed by every level above `chain[i]`, seeded from
  `aboveNames` and updated to `total` after each level). A name outside a
  level's own input schema is never "unknown", only a
  `windowWindowInputNames` collector veto is (the same rule
  `deriveWindowInputKeep`'s own doc comment states) — an empty match
  declines the whole derivation (unrepresentable, same rule
  `deriveOrderedSortInputKeep` uses).
- `deriveWindowRelNarrowKeep(top, aboveNames, aboveKnown)` computes the
  WINDOW rel's own OUTPUT-side keep: positions into `top.Output()` (the
  rel's published row) named by `aboveNames` alone — no own-names union,
  since a rel's own width is exactly what is needed of it above the rel, not
  what it reads from below.
- `narrowWindowRelWidth(rel, top, keep)` refines the `NCols`/`AvgVarBytes`
  `sizeWindowRelFromNode` just published on `rel`, mirroring
  `narrowOrderedRelWidths` (`ordered_input_narrow.go`) exactly: `Rows`/
  `Width` are left as `sizeWindowRelFromNode` set them — `rel.Width`'s only
  consumer is `pgSortRelationBytesCostEnabled()` (default OFF), the same
  disposition sweep-a recorded, kept for scope parity rather than
  independently re-derived.

Wiring:

- `addWindowPaths` gained a trailing `chainKeep [][]int` parameter,
  index-aligned with `windows`; a new `narrowedWindowCols(cols, keep)`
  helper narrows `belowNode.Output()` before `len(cols)`/
  `nodeAvgVarBytes(cols)` feed `costWindow` (the per-level `inWidth` term,
  `nodeTupleWidth(belowNode)`, is left unnarrowed — same Width-stays-full
  scope decision as the rel case above).
- `createWindowPaths` gained trailing `chainKeep [][]int`/`relKeep []int`
  parameters; calls `narrowWindowRelWidth(winRel, top, relKeep)` right after
  `sizeWindowRelFromNode`, and threads `chainKeep` into `addWindowPaths`.
- `buildWindowStage` (`planner.go:7139-7279`) computes both keep-sets ONCE,
  right after the per-group construction loop finishes (all of `windowChain`,
  `combinedByKey` and `currentCtx` are already final at that point) and
  before `createWindowPaths` runs: a throwaway `&windowSurface{input:
  inputCtx, agg: agg, output: currentCtx, windowByKey: combinedByKey}` —
  byte-for-byte what the function's own `surface` return value will hold a
  few lines later, not a second/different resolution — feeds
  `finalSelectOutputNames(s, inputCtx, agg, <throwaway surface>, starPS,
  false)` (the exact sweep-a helper, `ordered_input_narrow.go`) to get
  `aboveNames`. `srfPending` is passed the literal `false`: at
  `buildWindowStage`'s call site (`planner.go:1878-1884`), the SELECT-list
  SRF detection that would set `selectSrfPending` is itself gated
  `if ps == nil && !needsWindowStage(s)` (`planner.go:1845`), so
  `selectSrfPending` is provably still nil — not merely unknown — for the
  entire duration `buildWindowStage` runs; `false` is the true value, not an
  approximation. `starPS` (the composite-star `(expr).*` `ProjectSet`,
  `ps` at the call site) is threaded in as a new trailing parameter on
  `buildWindowStage` so the same decline guard sweep-a's Sort site applies
  reaches this site too.

## Why this cut is provably inert today (not "should show no movement" —
## genuinely cannot)

Unlike sweep-a's ORDER BY Sort (which competes for `addPath`'s dominance
tournament against nothing today, but at least carries a `PlanCost` that
`EXPLAIN` could in principle display under some future second candidate),
the WINDOW rel's design doc (`docs/design/
planner-p4-window-setop-paths/DESIGN.md`, restated in this file's own
header) states TWO independent facts that make this narrowing currently
unobservable by construction, not merely by corpus luck:

1. **No second candidate exists to select between.** `addWindowPaths`
   always offers exactly one `PathWindow` chain per WINDOW rel (`windowsetoppaths.go`'s
   own header: "goopg's input is a single finished Node, so that loop has
   one iteration"); `setCheapest`/`getCheapestFractionalPath` have nothing
   to discriminate.
2. **`*WindowAgg` carries no `PlanCost`.** `EXPLAIN` recomputes the legacy
   display number for a `*WindowAgg` node exactly as before this change —
   the file header's own "PRICES ARE SELECTION-NEUTRAL AND
   DISPLAY-INVISIBLE" fact.

So the correct acceptance bar here is not "no query's plan changed" (sweep-a's
bar) but the stronger "no query's *digest values* changed at all" — verified
below — plus the standing row-count/plan-shape gates, exactly as filed.

## Measurement

Same private-clone/private-port procedure as sweep-a, never touching the
shared `:65432`/`:65433`/`:65437`/`:65438` clusters beyond read-only
`pg_basebackup`/`SELECT`/`EXPLAIN`. Baseline binary: detached `git worktree
add --detach /tmp/wt-sweepb-baseline HEAD` at `9852fda5f` (sweep-a's own
close commit). After binary: working tree with this task's diff.

- **TPC-H values** (`scripts/tpch-acceptance-arm.sh`, full 22-query digest,
  `PGSHAPED=0`, private port 5583, `GOOPG_ANALYZE_SEED=20260905`,
  `NO_BUILD=1` with the two pinned binaries): `tpch-acceptance-runner -diff`
  reported **23/24 labels MATCH**; the sole non-MATCH is `Q9`, which
  `BOTH-ERROR`s identically in both arms (`pq: canceling statement due to
  user request (57014)`, the query's own known 600s-timeout flakiness —
  `q9_costdriven_mhj_cannot_be_cost_forced`, also the exact shape the
  P0-E7 in-progress-2026-09-18c measurement hit on an unrelated A/B the same
  day). `VERDICT: FAIL` is the runner's own literal error-string diff on
  Q9's cancellation message, not a values regression — every row-count-bearing
  query MATCHED. No TPC-H query touches a window function in its base form,
  so this arm is confirmation-by-absence (no digest line could move even in
  principle) rather than the positive evidence TPC-DS SF0.25 provides below.
- **TPC-DS SF0.25** (`scripts/tpcds-sf025-regression.sh sweep`, run against
  the after-binary tree, "far more common [window functions] there than in
  TPC-H" per the task's own gate note): `PASS=96 MISMATCH=0 CKMISMATCH=0
  ERROR=0 TIMEOUT=0`, `PLAN-SHAPE: queries=99 same=99 changed=0`. No
  category rose.
- `go build ./...` clean; `go test ./internal/optimizer/...` PASS (full
  package, all `addWindowPaths`/`createWindowPaths` call sites — 2 in
  `windowsetoppaths_test.go` plus the 2 production sites — updated to the
  new trailing parameters).

## Disposition

No `Parent: M0141-S2a-fix1-sweep-b` follow-up filed — nothing worsened, and
per the "provably inert today" analysis above nothing COULD have worsened
under the current single-candidate/no-`PlanCost` WINDOW rel design. Kept as
a correct, PG-faithful cost-input fix for the day a second WINDOW candidate
exists (C-14 Incremental Sort's window counterpart, or S2b-3b's presorted
credit wiring — both already named as this file's own future consumers in
`windowsetoppaths.go`'s `costWindow` doc comment) — the same "correctness of
the cost INPUT is the goal, independent of whether today's corpus exercises
the difference" standard sweep-a and fix2r used.

`Movement: none.` `go test ./internal/optimizer/...` and
`scripts/tpch-spotcheck.sh` (Q12=2/Q13=34) pass; `scripts/
tpcds-sf025-regression.sh sweep` PASS with zero plan-shape change;
`make ea-ratchet` is N/A — a costing-only change touching no row-estimate/
selectivity path, same disposition as sweep-a/fix2r.
