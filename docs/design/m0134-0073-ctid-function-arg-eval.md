# M0134-0073 — `tidrangescan.sql`: ctid lost in function-argument evaluation

Status: accepted · Date: 2026-08-22 · Milestone: M0134-0073

## The case

`tidrangescan.sql` is a `failed` regress row (CSV
`docs/test-port/postgres-oracle-target-inventory.csv:202`). Sized at HEAD it is
**590 diff lines / 7 hunks / 0 `^+ERROR` / 0 `^-ERROR`**, server completes the
file (no crash), deterministic across fresh-cluster runs — genuinely failing,
not stale.

The diff splits into four root-cause buckets:

| bucket | root cause | tier | ~lines |
|---|---|---|---|
| B | `ctid` lost in function-argument evaluation → NULL → `DELETE` deletes 0 rows | **CONTAINED** (correctness bug) | ~420 |
| A | no `TidRangeScan` plan path exists at all | REFACTOR (missing feature) | ~40 |
| C | outer `ctid` across subquery/LATERAL mis-binds to the inner rel's ctid | REFACTOR (de-correlation) | ~25 |
| D | SCROLL `FETCH FIRST`/`FETCH LAST` aliased to NEXT/PRIOR | CONTAINED (gated on B) | ~10 |

This doc records the **Bucket B** slice (the largest contained fix). A, C and D
are ledgered as deferrals with their resume points.

## Root cause: ctid lives in the slot, not the Row

goopg's executor carries the row's ctid as a side-channel on the scan slot
(`MaterializedSlot.hasCTID/ctidBlock/ctidOff`, `internal/executor/slot.go:60-65`
and the opnode `*Slot` twin), **not** as an element of the `Row []Datum`
(`internal/executor/datum.go:870`). Expression evaluation against a real scan
slot therefore resolves `CTIDExpr` fine — but function-argument evaluation drops
the slot, and with it the ctid.

The loss is at two choke points:

1. `evalExprSlot`'s `FuncCall` case — `internal/executor/expr.go:1186`:
   `return evalFuncCall(x, slotToRow(slot), ctx)`. `slotToRow`
   (`slot.go:210-239`) reduces a `*MaterializedSlot`/`*Slot` to its bare `Row`,
   discarding `hasCTID/ctidBlock/ctidOff`.
2. Function handlers re-evaluate their arguments via `evalExpr(x.Args[i], row,
   ctx)` (e.g. `evalSubstr` `expr.go:15378`, the inline `"length"` case
   `expr.go:11953`). `evalExpr` (`expr.go:335-341`) wraps the bare `row` in a
   `rowSlotView`, which has no ctid, so `evalExprSlot`'s `*CTIDExpr` case
   (`expr.go:480-495`) — whose `switch s := slot.(type)` only matches
   `*MaterializedSlot`/`*Slot` — falls through to `NullDatum`.

Consequence: any builtin whose argument references `ctid` silently yields NULL
(`substring(ctid::text FROM …)`, `length(ctid::text)`, `substr(ctid::text,…)`),
while slot-preserving contexts (`ctid::text = …`, `||`, `IS NULL`) work. The
test's `DELETE … WHERE substring(ctid::text FROM ',(\d+)\)')::integer > 2`
deletes **0 rows** where PG prunes every row but two.

PG oracle: system columns are resolved from the slot explicitly —
`ExecEvalSysVar` → `slot_getsysattr(slot, SelfItemPointerAttributeNumber)` — so
the slot is threaded through expression evaluation, never reduced to a bare
attribute list. `textregexsubstr` (`postgres/src/backend/utils/adt/regexp.c:583`)
is fine; the breakage is upstream of it, in goopg's arg-evaluation boundary.

## Fix decision (fork chosen: thread the slot through function-argument eval)

Three candidate forks were weighed (full analysis in
`tmp/ralph-handoffs/m0134-0073-tidrangescan-sizing/report.md`):

- **(a) give `rowSlotView` an optional ctid field** — does not work alone: the
  ctid is destroyed at the `Row` boundary (`slotToRow`) *before* the
  `rowSlotView` is built, so there is nothing to attach.
- **(b) carry ctid as a trailing resjunk Datum in the Row** — PG-faithful and
  the seed for Bucket C + the deferred lockrows resjunk-ctid work
  (`docs/design/0129-0003-resjunk-ctid-column-path.md`), but needs a new
  planner trigger (extend scan schemas whenever a `CTIDExpr` is referenced, not
  just for `FOR UPDATE` rowmarks) plus a self-description problem (a trailing
  datum is indistinguishable from a real last column without a schema).
  **Deferred — this is the long-term path for Bucket C**, not the minimal Bucket B
  fix.
- **(c) thread the real slot through `evalFuncCall` into argument eval** —
  chosen. This removes the loss at the source, matches PG's explicit-slot model,
  and is executor-only (no planner/schema change). The compiled/fast evaluator
  twin (`exprnode.go` `ExprAdapter`) delegates to `evalExprSlot`, so it funnels
  through the same `expr.go:1186` drop and one fix covers both evaluators.

A fourth option (an ambient `ctx.CurRowHasCTID` side-channel) was **rejected**:
it is order-fragile across subquery boundaries (an inner scan clobbers the
"current" ctid) and would silently return the wrong block/offset rather than an
error — an anti-PG-faithful shortcut.

## The slice

Thread `SlotView` (not `Row`) through the function-call dispatch tree:

1. `expr.go:1186` → `evalFuncCall(x, slot, ctx)` (drop the `slotToRow`).
2. `evalFuncCall` signature `(x, row Row, ctx)` → `(x, slot SlotView, ctx)`.
   Keep a local `row := slotToRow(slot)` where a handler needs whole-row/Row
   indexing; that path legitimately has no ctid and is unchanged.
3. Every function handler dispatched from `evalFuncCall`'s switch takes
   `slot SlotView` instead of `row Row`, and re-evaluates sub-expressions with
   `evalExprSlot(x.Args[i], slot, ctx)` instead of `evalExpr(x.Args[i], row,
   ctx)` — so a `CTIDExpr` argument resolves against the same slot the enclosing
   expression saw.
4. Test callers that pass a non-nil `Row` wrap it in `rowSlotView(row)` (nil
   already compiles as a nil `SlotView`).

This is a mechanical signature + call-site change confined to the function
handler family in `internal/executor`; it fixes both evaluators (compiled twin
delegates) and introduces no column-index/tupledesc shift.

## Acceptance

- The three `tidrangescan` row-count anchors (DELETE pruning every row but the
  two whose `ctid` co-ordinates exceed the predicate) now match PG
  (`tidrangescan.out` — row counts collapse to the pruned counts).
- `length(ctid::text)`, `substr(ctid::text,…)`, `substring(ctid::text FROM …)`
  return the ctid string (e.g. `(0,1)`) instead of NULL, live-verified against
  the PG 18.3 oracle.
- No regression: binary-op / `IS NULL` / cast paths on `ctid` stay byte-identical;
  full `internal/executor` (and `internal/parser`) PASS; tpch-spotcheck row
  counts unchanged (Q12=2, Q13=35).

## Deferrals (ledger rows)

- **Bucket A** — no TidRangeScan node: `create_tidscan_paths`
  (`optimizer/path/tidpath.c:498`) + `nodeTidrangescan.c`. Own milestone.
- **Bucket C** — outer-ctid across subquery/LATERAL: needs ctid as an
  addressable `Row` datum (fork b) + `replace_nestloop_params`
  (`createplan.c:5036`) / `process_subquery_nestloop_params`
  (`util/paramassign.c:527`).
- **Bucket D** — `FETCH FIRST/LAST`: `parseFetchCursor`
  (`parser.go:3086-3090`) aliases them to NEXT/PRIOR; PG `PortalRunFetch`
  (`pquery.c:1377`) re-anchors to row 1 / last row. Gated on B's data layout.
- **Latent siblings** — the other 9 `slotToRow(slot)` call sites in
  `evalExprSlot` (CaseExpr, Subquery, In, Exists, Extract, …) share the same
  drop and are left untouched this slice.
- **xmin/xmax** — resolved as 42703 at the planner (`planner.go:13858-13864`);
  independent, pre-existing gap.
