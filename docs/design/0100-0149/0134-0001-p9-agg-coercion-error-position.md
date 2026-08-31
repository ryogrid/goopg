# M0134-0001 P9 — a deferred coercion must still carry the literal's position

Status: accepted (landed 2026-08-17, slice S21)
Milestone: M0134-0001 (`aggregates.sql` regress-sql digestion)
Related: `0134-0001-p2-explain-format.md` (S11/S17/S18/S19),
`0134-0001-p8-ordered-set-agg-collation.md` (S20)

## The divergence

```sql
select rank('fred') within group (order by x) from generate_series(1,5) x;
```

`postgres/src/test/regress/expected/aggregates.out:2563-2571` (diff hunk #12 of
26, `@@ -2283,12 +2216,10 @@`):

```
ERROR:  invalid input syntax for type integer: "fred"
LINE 1: select rank('fred') within group (order by x) from generate_...
                    ^
```

goopg emitted the `ERROR:` line alone — no `LINE 1:`, no caret.

The decisive observation came from the hunk itself: the *sibling* query two
lines below (`rank('adam'::text collate "C") within group (order by x collate
"POSIX")`, a 42P21 collation mismatch) **already** rendered its `LINE 1:`/`^`
correctly and was unchanged by the diff. So the position-rendering plumbing was
not the defect; exactly one error's position was never set.

## Root cause

`buildAggregateCall` (`internal/optimizer/planner.go:8091`) handles a
hypothetical-set aggregate whose direct argument is a `text`/`unknown` literal
against a numeric ORDER BY column by deferring the coercion to runtime
(`:8374-8379`):

```go
argExpr = &CastExpr{Operand: argExpr, TargetType: orderT}
```

`CastExpr` (`internal/optimizer/plan.go:525-534`) carries an **unexported**
`pos int` with a `Pos()` accessor. The composite literal above never set it, so
it defaulted to 0 — and `ExecError.Pos == 0` is goopg's established convention
for "suppress `LINE 1`" (`internal/executor/operators_ddl.go:3207,10043`,
enforced by `operators_ddl_system_column_test.go:34`).

Everything downstream was already correct and already exercised:
`CastExpr.Pos()` → `evalCast(…, pos, …)` (`internal/executor/expr.go:514-526`)
→ the string→int branches' `if ee, ok := err.(*ExecError); ok { ee.Pos = pos }`
(`:3536-3543` int4, `:3560-3573` int8) → the renderer. The chain was simply fed
a zero. The fix sets `pos: argE.Pos()` from the original *pre-wrap* direct-arg
expression, matching the sibling `PlanError` constructed twelve lines below at
`:8386`, which already did exactly this.

## The PG oracle — and why goopg reaches the same output by a different road

PG never has this problem because it does not defer the coercion at all.
`coerce_type` (`postgres/src/backend/parser/parse_coerce.c:157`) folds a string
literal to a `Const` at **parse-analysis** time by invoking the target type's
input function immediately, and takes two deliberate steps to preserve position:

```c
/* :294-298 */
/* We use the original literal's location regardless of the position
 * of the coercion. ... */
newcon->location = con->location;
/* :300-304 */
/* Set up to point at the constant's text if the input routine throws
 * an error. */
setup_parser_errposition_callback(&pcbstate, pstate, con->location);
```

goopg evaluates the cast at run time instead. That is a genuine mechanism
difference, not a faithful port — but goopg's runtime path has its own position
convention that produces byte-identical user-visible output, so S21 fed that
convention rather than restructuring the coercion timing. The observable
consequence of the remaining difference is ledgered (see below).

## Verification of the one real risk

The scoping pass could not confirm that the wire-protocol layer computes the
caret column identically for a runtime `ExecError.Pos` and a parse-time
`PlanError.Pos`; had it not, the fix would have produced a caret in the wrong
column and the brief required an escalation rather than an offset fudge. It was
checked empirically against `postgres/local_install/bin/psql` on a throwaway
capped goopg server: **byte-identical to PG for both errors**, so the two origins
do share one basis.

## Twin search — positively negative

This milestone has burned four slices on unpaired twins (S11's two EXPLAIN
walkers, S17's `describePlan`/`describePlanVerbose`, S18's two key emitters,
S20's two target-list resolvers), so the absence of a twin is recorded as a
result, not an omission:

- **Evaluation**: one site cares about `CastExpr.Pos()` for error rendering —
  `internal/executor/expr.go:514` inside `evalExprSlot`, shared by general
  expressions *and* the hypothetical-set direct arg (`operators_join_agg.go`
  calls `evalExprSlot` at `:2588` and `:2631`). There is no separate
  aggregate-argument evaluator. The two other `*optimizer.CastExpr` cases in
  `expr.go` (`:8050` typmod inference, `:13980` `hashPartTypeName`) never call
  `evalCast` and render no position.
- **Construction**: one site (`planner.go:8376`); `buildAggregateCall`'s two
  callers (`:6773` SELECT list, `:6826` HAVING) share it.

## Measurement

`aggregates` **943 → 930 lines, 26 → 25 hunks** — the scoping prediction hit
exactly; hunk #12 closed entirely with no adjacent hunk merging. Sentinel
`functional_deps` 56 unchanged (the only run-to-run deterministic sentinel, per
S18).

Guards: `internal/executor/hypothetical_set_agg_errpos_test.go` —
`TestHypotheticalSetAggRuntimeCastErrorCarriesPosition` (FAIL-pre/PASS-post,
verified by temporary revert) and
`TestHypotheticalSetAggCollationMismatchCarriesPosition`, which pins the sibling
42P21 `*optimizer.PlanError` so the pair cannot drift apart.

## Deferred

goopg coerces at run time where PG coerces at parse-analysis time, so a query
shape that never evaluates the direct argument (zero input rows, `WHERE false`)
should error in PG and stay silent in goopg. No corpus witness — every
`aggregates.sql` occurrence has rows. See the deferral ledger row dated
2026-08-17.
