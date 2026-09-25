# R113 DESIGN — opt-in PG relation-byte Sort-price comparison

## Question and boundary

R112 established that Goopg has no universal Datum size. It found one reached,
price-sensitive family with a direct PostgreSQL analogue: `costSortRun` uses
`hashsize.EntryBytes(ncols, avgVarBytes)` for external-sort input/output bytes,
while PG18.3 `cost_tuplesort` calls `relation_byte_size(tuples, width)`:

```text
tuples * (MAXALIGN(width) + MAXALIGN(SizeofHeapTupleHeader))
```

R113 asks only whether this PG planner representation moves Sort elections
toward live PG18.3. It is not a whole-planner Datum-size rewrite and it must
not represent Goopg executor memory use.

## Authorized implementation

Add an opt-in, default-off switch enabled only by
`GOOPG_PG_SORT_RELATION_BYTES_COST=1`. Switch-off must preserve the exact
current `costSortRun` result. Switch-on may use a private planner helper for
that function's `inputBytes` and `outputBytes` only, when emitted width is
positive:

```text
pgRelationByteSize(rows, width) =
    rows * (MAXALIGN(width) + MAXALIGN(SizeofHeapTupleHeader))
```

Use PG18.3's 8-byte alignment and 24-byte `HeapTupleHeaderData`. The opt-in
branch must also reproduce `cost_tuplesort`'s ordering exactly: calculate its
input bytes from the original input row count before the two-row floor; then
apply the floor and bound before calculating output bytes and choosing the
branch. The legacy switch-off path retains its current floor-first result.
Preserve bounded-sort ordering, merge-pass calculation, and I/O rates. Zero,
negative, or invalid width must fail closed to current Goopg entry bytes. The
switch must not change row estimates, comparison CPU, pathkeys, costs outside
`costSortRun`, or executor behaviour.

Do not change executor Sort buffering, `hashsize.Choose`, Hash Join,
HashAggregate, Memoize, Materialize/rescan, scans, index costing, or storage.
`hashsize.EntryBytes` remains the executor/memory currency and normal price
currency when the switch is off.

## Required width provenance

An added `width` argument must be the width of rows being sorted, not key count,
post-projection relation width, or `AvgVarBytes`.

| caller | required emitted width |
|---|---|
| `sortPathForBounded` (merge inputs and ORDERED paths) | `pathWidth(sub)`: narrowed `OutputWidth`, otherwise `sub.Rel.Width`. A Sort projects nothing. |
| `addPartialSortPaths` (worker and leader) | `ordered.Width`, from `tupleWidth(child.Output())`; both sort the same child row at different counts. |
| `costWindow` | explicit pre-window child-node width, threaded beside `inNcols`/`inAvgVarBytes`. |
| SetOp or future direct caller | its input node/path emitted width; a completeness test guards every direct call site. |

Focused tests must prove narrow `Path.OutputWidth` wins over `Rel.Width`, merge
and upper Sort use actual inputs, both partial arms use the same width, and
Window receives its child width. Unknown width can be tested only as fallback;
no reachable sized path may silently pass zero.

## Focused proof and trace

Before corpus work, tests must prove: switch-off byte-identical legacy cost for
floor/fitting/disk/bounded branches; switch-on PG bytes at 8-byte boundaries;
the 0/1-row input-before-floor versus output-after-floor ordering and bounded
boundary vectors; consistent input/output, spill/page/run/bounded decisions;
invalid-width fallback; and planner width changes neither executor allocation
nor non-Sort costs.

With shaped-DP trace enabled, add trace-only records for caller identity,
rows/bound, emitted width, Goopg and PG bytes, both branch decisions, and
selected price currency. Existing `DPPATH` evidence must distinguish selected
Sort paths from merely offered candidates. Trace must not affect decisions
except via the named switch.

## Measurement, gates, and stop rules

Use the committed R112 source/data baseline and live PG18.3 with pinned GUCs.
Run switch-off/off A/A structural control, then fixed-seed OFF/ON TPC-H and
TPC-DS SF0.25 plan captures. Record every changed query and selected-versus-
offered Sort trace. Run TPC-H digest and foreground TPC-DS SF0.25 values in
both modes; any value difference stops the experiment. Run fresh live-PG18.3
structural census in both modes and classify each movement.

Run focused optimizer/executor suites, `go vet`, `git diff --check`, and the
established plan/value gates. Keep every command/result in the report,
including no movement. The switch remains default-off; promotion requires a
separate scope.

This task depends on R112 DONE. Obtain agent review, correct if needed, then
`git commit -n` and push this design before any production edit. Stop rather
than broaden if width provenance cannot be demonstrated, executor behaviour
changes, live PG18.3 evidence is unavailable, or a non-Sort family is needed.
