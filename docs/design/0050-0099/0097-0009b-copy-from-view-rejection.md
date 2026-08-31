# 0097-0009b — Reject table-form `COPY` against a plain view

Status: accepted
Milestone: M0097-0009 (copyselect regress porting)

## Problem

PostgreSQL checks the relation *kind* before doing anything else in a
table-form `COPY`. A plain view has no heap to stream rows out of or to
load rows into, so both directions fail immediately with
`ERRCODE_WRONG_OBJECT_TYPE` (SQLSTATE `42809`):

```
COPY v_test1 TO STDOUT;
ERROR:  cannot copy from view "v_test1"
HINT:   Try the COPY (SELECT ...) TO variant.
```

goopg's `planCopy` (`internal/planner/copy.go`) resolved the relation
via `cat.LookupTable` and proceeded to build a heap-scan `Copy` node
regardless of kind. `COPY <view> TO STDOUT` therefore did not error the
way PostgreSQL does, diverging from the `copyselect` regress expected
output (the two `cannot copy from view` errors and their two hints were
absent from goopg's output).

## Fix

Two coordinated changes (sibling paths — planner produces the error, the
wire layer must carry every field of it):

1. **Planner** (`internal/planner/copy.go`, `planCopy`): right after the
   relation is resolved, reject it when `tbl.View != nil && !tbl.IsMatView`.
   Materialised views are exempt — they store rows in the heap like a
   table, so `COPY matview TO` is legal. Direction-specific messages:
   - `CopyTo`   → `cannot copy from view %q`, hint
     `Try the COPY (SELECT ...) TO variant.`
   - `CopyFrom` → `cannot copy to view %q`, hint
     `To enable inserting into the view, provide an INSTEAD OF INSERT trigger.`

   The check sits *before* column-list resolution so a view reference
   fails on kind, not on a missing column (matches PostgreSQL ordering).

2. **Wire layer** (`internal/server/copy.go`, `dispatchCopyViaExecutor`):
   the plan-error path called `writeQueryError(w, code, msg)` and dropped
   any hint. Changed to pass `planErrorHintFields(err)...` (the same
   helper the simple-query and extended-query dispatch paths already use),
   so the planner's hint reaches the client. Without this the ERROR line
   matched but the two HINT lines stayed in the diff.

## Scope / limits

`copyselect` still fails as a whole — its remaining gaps are independent
larger features: `UNION`/set-operations inside `COPY (SELECT …)`,
`COPY (SELECT … INTO …)` rejection, and psql multi-command (`\;` +
`\.` STDIN) handling. This change closes only the view-rejection lines
(2 ERROR + 2 HINT) and removes goopg's spurious output for those two
statements.

## Tests

- `TestPlanCopyViewRejected` (`internal/planner/copy_test.go`) — both
  directions, asserts code `42809`, the exact message, and the exact
  hint.
- End-to-end verified via `GOOPG_REGRESS_DIFF_DIR` on `copyselect`: the
  `cannot copy from view` ERROR lines and their `Try the COPY (SELECT …)`
  hints no longer appear in the diff.

Sibling-path discipline per [[pattern_sibling_paths_must_agree]]: the
planner error and the wire-layer field propagation had drifted; both are
now updated together.
