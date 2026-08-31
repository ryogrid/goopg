# M0134-0129 — `indirect_toast.sql`: UPDATE SET-clause TOAST self-reference corruption

**Status:** PARKED (`failed`), major contained fix shipped.
**Oracle:** `postgres/src/test/regress/sql/indirect_toast.sql` /
`postgres/src/test/regress/expected/indirect_toast.out`.

## Sizing

`scripts/pg-regress-runner.sh indirect_toast`: 0% parity, 184 diff lines
before this loop's fix, **25 diff lines after** (86% reduction). The
remaining 25-line diff is entirely the `CREATE FUNCTION make_tuple_indirect
... LANGUAGE C STRICT AS :'regresslib'` call and the trigger built on top of
it (`update_using_indirect`) — the file's whole purpose is exercising PG's
"indirect" (in-memory pointer) TOAST datum representation via that C helper.
goopg has no dynamic-extension-loading capability (standing gap, item 3 of
`.ralph/working_set.md`'s cross-file blocker list, recurring across
M0134-0106/-0116/-0120 and create_operator/create_type-adjacent files); this
file joins that list. Everything else in the file is now byte-identical to
the PG oracle.

## What was actually wrong (the discovery, not just the symptom)

The failing diff's visible symptom was cosmetic-looking —
`substring(indtoasttest::text, 1, 200)` printed the literal string
`?datum kind=8?` (goopg's `Datum.AppendValueText` fallback for an
unresolved `KindToastPointer`, `internal/executor/datum.go:777`) instead of
the real column value. But this was **not a display bug**: re-running the
same `SELECT` after `VACUUM FREEZE` (a fresh scan, no residual in-memory
state) still showed the same garbage forever. The garbage had been
**durably written to disk** as the column's actual new value.

Root cause: `UPDATE ... SET f1 = '-'||f1||'-'` (a SET-clause expression
that concatenates a TOASTed column with itself) evaluated the RHS
expression against the row `scanMatching` (`internal/executor/
operators_storage.go`) hands to its callback — and that row is **not
detoasted**. Any oversized `text`/`bytea`/… column decodes as a raw 12-byte
`KindToastPointer` datum (`internal/executor/toast.go`'s
`needsDetoast`/`DetoastRow`); the plain `SELECT` path (`seqScanOp.Next()`)
already detoasts before returning rows, but the UPDATE/DELETE scan path
(`scanMatching`) never did. So `'-' || f1 || '-'` stringified the
*pointer*, not the value, via `AppendValueText`'s `?datum kind=N?`
fallback — and that short garbage string, being small, fit inline and was
written back as `f1`'s real new value. `f2` (untouched by the SET clause)
survived because the unmodified-column passthrough copies the raw
(still-valid) TOAST pointer unchanged — the *same* code shape that corrupts
f1 is exactly what keeps f2 correct, which is why the corruption looked
column-specific at first.

Fixing the naive way — detoasting the whole row up front — created a
second-order regression: the "unchanged" columns would then also carry
their full detoasted values into `newRow`, defeating PG's actual behaviour
of leaving an untouched out-of-line datum's TOAST pointer alone (this file
is *named* `indirect_toast.sql` specifically to test that invariant), and
inflating `newRow` past the in-place HOT-update path's line-pointer limit
(`tryApplyHOTUpdate` encodes directly with no re-toast step at all, unlike
the general `writeHeapRowReturning` path) — `ERROR: tuple too large for
line pointer len=800052`. Both of the below fixes were required together.

## Fix (four call sites, `internal/executor/operators_storage.go`)

1. **`scanMatching`'s WHERE-clause predicate evaluation**: build a
   lazily-detoasted view of the visible tuple for `evalExprSlot(pred, ...)`
   only; the row handed to the `fn` callback (and hence to
   `updateOp`/`deleteOp`) stays raw, so unmodified out-of-line columns keep
   passing their original pointer through.
2. **`updateOp`'s non-inherit-child SET-expression loop**: same
   lazily-detoasted view, built once per row on first SET-expression that
   needs it (`DetoastRow(o.ctx, scanRel, captureCols, row)`), used only for
   `evalExpr(o.plan.Set[setIdx], ...)`; the `else` (unset-column)
   passthrough branch still copies `row[i]` raw.
3. **`updateOp`'s inheritance-child branch**: same idea, but detoast must
   happen *before* `remapChildRowToParent` — the row's TOAST pointers are
   scoped to the child table's own TOAST relation (`ToastRelFor(scanRel)`),
   not the parent's.
4. **`updateOp.appendUpdateRetRowWithFrom`** (the RETURNING projection):
   detoast `newRow` before evaluating `o.plan.Returning` expressions. This
   is the piece that fixed the *first* UPDATE in the file (`SET cnt = cnt +
   1 RETURNING ...`, no SET-clause touching f1/f2 at all) — untouched
   columns deliberately keep their raw pointer per fix (2)/(3) above, so
   RETURNING — a plain read — must resolve it itself, exactly like a
   `SELECT` would via `seqScanOp`'s existing detoast.
5. **`tryApplyHOTUpdate`**: added the same `ToastLargeColumnsIfNeeded` call
   `writeHeapRowReturning` already had, immediately before
   `EncodeRowPGCtx`. Without this, fix (2)'s now-correct (large, real)
   SET-expression result hits the in-place HOT path with no re-toast step
   and blows the line-pointer size limit — this bug was latent before (a
   HOT update never needed to re-toast a *correct* value because the
   pre-fix garbage string was always tiny) and is exposed, not introduced,
   by fixing (2).

## Tests

`TestToastUpdateSetSelfReferenceDetoasts`
(`internal/executor/toast_update_selfref_test.go`): 300 KB / 500 KB
TOASTed `f1`/`f2` columns, `UPDATE ... SET cnt = cnt + 1, f1 = '-'||f1||'-'
RETURNING f1, f2`, asserts both the RETURNING projection and a subsequent
fresh `SELECT` see the real concatenated value for `f1` and the untouched
real value for `f2`.

## Deferred (ledger row filed same date)

- Same TOAST-self-reference class of bug is only fixed for the plain
  (non-inherit, non-`FROM`, non-index-probe) `updateOp` scan path. The
  sibling call sites — `updateOp.updateWithFrom` (UPDATE ... FROM),
  `updateOp.updateViaIndex` (single-key index probe), and `deleteOp`'s
  various scan callbacks — have their own, differently-shaped row-handling
  code and were **not** audited/fixed this loop. Resume point: search
  `operators_storage.go` for `evalExpr(o.plan.Set[` and the `deleteOp`
  scanMatching callbacks; apply the same lazily-detoasted-eval-row pattern.
- `make_tuple_indirect`/LANGUAGE C dynamic extension loading — unchanged,
  standing blocker (see sizing section above).

## Oracle references

- `postgres/src/backend/access/heap/heaptoast.c` — `toast_flatten_tuple`,
  the C function this test's `make_tuple_indirect` regress helper wraps.
- `postgres/src/backend/executor/nodeModifyTable.c` —
  `ExecUpdate`/`ExecProject` evaluate the target list (SET clause) against
  `ExecFetchSlotHeapTuple`, which is always a fully-deformed (detoasted for
  by-value access) slot in PG; goopg's `KindToastPointer` deferred-detoast
  representation has no direct PG analogue, so this class of bug is
  goopg-internal-representation-specific, not a port of any upstream defect.
