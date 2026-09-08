# EX1-01 Design — Scan deform bound from Build-time consumer walk

Item: `TODO_EXECUTOR.md` EX1-01 (gate: values-diff both suites + pin +
alloc arm). Status: design for review.

## 1. Problem and dependency position

Q6 decode is 39.9% of window CPU; survivors (2%) full-deform all 16
columns for ~4–5 referenced. Planner P4-01 (positional target lists) is
OPEN and stays the general fix's input — this slice does NOT wait for
it and does NOT build it: it computes a per-leaf deform bound at
executor `Build()` time by walking the already-materialized plan above
the leaf, in leaf-local `ColumnRef.Index` space (the same coordinate
space the take5 prefilter uses for its `MaxCols`).

## 2. Mechanism (top-down bound, threaded to the leaf)

Build is top-down (`buildNode` recurses parent→child; leaves receive
only their own plan node), so no leaf-up walk exists. The bound is
computed at each PARENT and threaded down — same direction as the
Filter-above-SeqScan prefilter precedent (which peeks at
`p.Child.(*optimizer.SeqScan)` and stamps the built child):

- `scanDeformBound(parentChain) int`: maintained as a "highest
  referenced child-output index" while descending. Coordinates are
  CHILD-OUTPUT space (`ColumnRef.Index` = 0-based into the input row,
  `plan.go:420-421`) — coincidence with leaf space holds only through
  pass-through nodes (`Filter`, `Sort`, `Limit`, `Distinct`,
  `LockRows`, `Result` — `Output()==Child.Output()`), which propagate
  the bound unchanged. Terminators (bound resets to full width
  downstream): `Join` (merges schemas; keys are side-local),
  `Aggregate` (reshapes to groups+aggs — its `GroupExprs` /
  `AggregateCall{Arg,Arg2,ExtraArgs,Filter,OrderBy}` /
  `Passthrough` arms are READ as consumers before terminating),
  any `Project` that is not exactly identity (`Targets ≡
  ColumnRef(i)→i`, checked syntactically), `WHOLEROW`/RowExpr/CTID/
  TableOid/FuncCall/subquery/outer-ref anywhere (whitelist direction:
  unknown = full deform). Leaf-local `IndexScan.Cond` /
  `BitmapHeapScan.Cond` are read as consumers at the leaf itself
  (SeqScan-only slice: recorded for the follow-up).
- Consequence, stated: the walk terminates at the first join, so
  join-heavy shapes (Q9) get `bound=ncols` — this slice moves Q6-class
  scans; Q9's width was recorded under EX0-05 and moves with the
  general P4-01 fix, not here.
- Threading: through `buildNode` AND the `buildRec` slab twin (both
  Build paths need it). Gather: the bound is captured into the worker
  `buildChild` closure (the closure closes over the partial plan AND
  the bound); workers that cannot prove the bound decline to full
  deform. NLI inner rescans: out of scope (SeqScan-only + no inner
  rescans in this slice).
- `seqScanOp` deforms `[0, bound)` instead of `[0, len(cols))` on the
  survivor path (prefilter `[0, MaxCols)` unchanged).
  `scanRow`/`schema` stay FULL-WIDTH (P4-01b lesson). SEQSCAN-ONLY:
  IndexScan/BitmapHeapScan are an explicit follow-up (separate sibling
  decode paths + outer-coordinate risk — not "trivial", not this
  commit).
- Defense in depth: under a debug flag (default off, zero release
  cost) the undeformed tail is poisoned at deform time and any read of
  a poisoned slot panics — a walk miss fails loudly in tests instead
  of returning stale Datums. The gate runs once with the flag on.

## 3. Why values cannot change

The bound only ever EXCLUDES columns no consumer reads: any read of an
excluded column is a walk bug, and the walk defaults to full deform on
every shape it does not positively understand. The deform primitive +
resume-offset contract + "partial row never escapes" rule are the
proven prefilter machinery. Gate is values-diff (`-digest` + `-diff`)
on both suites regardless.

## 4. Verification (gate — full strength, both suites values-diff)

- Unit: bound computation over synthetic chains (filter-only,
  filter+project-identity, project-reorder terminator, join
  terminator, aggregate arm reading, WHOLEROW decline) + tail-poison
  run (flag on) proving no test reads past the bound.
- TPC-H values-diff: `cmd/tpch-runner -diff` pre/post → `VERDICT:
  PASS` (09 §3 floor — NOT just spotcheck row counts; a walk miss is
  silent wrong data, so the TPC-H side cannot downgrade).
- TPC-DS SF0.5 sweep values gate (PASS=95 MISMATCH=0); plan-gate
  `changed=0`.
- Timing + alloc arms on Q6 (decode slice must drop; allocations must
  not rise) + Q9 narrowed width re-recorded (expect unchanged —
  bound=ncols past the first join — stated, not a failure).
