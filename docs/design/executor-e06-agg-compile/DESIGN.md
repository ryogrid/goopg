# E-06 (EX4-03) — Agg transition fast path

Status: accepted. Implements TODO_ALL.md E-06. Design: take3 13 §6
(EX4-03). Third EX4 slice after E-05 (same slab, same fallback
contract, same gate family). `MIXED`/spill behaviour unchanged.

## 1. Objective

Compile builtin-agg `transfn` expressions: the per-row argument
evaluations in `applyAgg` (`call.Filter`, `call.Arg`, `call.Arg2`,
`call.ExtraArgs`) from `evalExprSlot` to `evalFastExpr`. Gate:
per-row transfn slice down; values + pin.

## 2. Sites (one commit)

- `applyAgg` (`operators_join_agg.go:2665+`): Filter (`:2670`),
  WithinGroup direct/extra/order keys (`:2714,2725,2736` — evaluated
  per row into `withinGroupElems`), Arg (`:2757`), Arg2 (`:2772`),
  ExtraArgs (`:2778`), the strict-sfunc NULL pre-checks (`:3010,3035`
  read the same expressions — same nodes, no second compilation),
  regr_/covar_/corr Arg2 (`:3266` — skip-row on error, builtin, hot),
  `string_agg`/`array_agg … ORDER BY` per-row sort keys
  (`evalAggOrderByKeys`: `:3021,3049,3077`).
- Per-site error-handling rule (binding): one node may be read at
  several sites with DIFFERENT error semantics (strict pre-check
  swallows; delimiter sites `_, _`/break; regr skips the row). The
  node is compiled once; EACH dispatch preserves its site's handling
  verbatim — the twin-parity corpus covers a failing Arg2 at every
  site, not just arg positions.
- OUT: group keys (`evalGroupExprs` — grouping, not transition;
  separate slice if ever), `UserAgg` sfunc machinery (opaque
  callbacks — whole-call decline, see §3), `finishAgg` (once per
  group, not per row; its DISTINCT sfunc loop evaluates no exprs),
  ordered-set sorts (`compareDatum` — comparisons, not evals),
  `:2182`/`:2607` passthrough (per-group first-row, errors swallowed
  to NULL — not transition).

## 3. Mechanism (third slab user — same discipline as E-05)

- New fields on `aggregateOp` (NOT on `joinOp` — different operator,
  different Open): `aggExprs exprTreeSlab`, per-call node lists
  (`argNodes/filterNodes/arg2Nodes []int32` indexed parallel to
  `plan.Aggs` + `extraNodes [][]int32` + order-key nodes),
  compiled once per `Open` from the plan's `AggregateCall` list
  (same-split discipline: nodes derive from the calls the loops
  evaluate). Mandate `buildExpr`, never `buildExprCtx` (the latter
  folds constants — `FuncCall` is whitelist-excluded today, but a
  caller change would silently alter volatile timing).
- Rebuild unconditionally per Open (E-05 discipline); nil plan/tests
  via length-compared ensure-guard (adequate because zero
  same-length-swap sites exist — `plan.Aggs` mutates only at build,
  pre-Open; DISTINCT dedup mutates runtime state, not plan nodes;
  each Partial worker owns its op).
- Wrapper logic UNTOUCHED: FILTER skip, strict-sfunc NULL skips,
  array_agg NULL-inclusion, WithinGroup once-only direct-arg capture,
  `hasValue` stamping. Pure dispatch swap at each site.
- UserAgg exclusion = WHOLE-CALL decline (user-aggs share `applyAgg`
  lines with builtins — Filter/Arg evaluate before the dispatch):
  Filter/Arg/Arg2/ExtraArgs/OrderBy/WithinGroup keys ALL stay
  interpreted for that call, via a per-call `noExpr` sentinel + a
  per-row sentinel branch. The live machinery this preserves is the
  `SharedStateSlot` leader-skip + `finalizeGroup` sync (not the
  allocated-but-unread `sharedUserStates`).
- Fallback structural (`ExprAdapter`).
- Sibling rule: `o.plan.Aggs` calls stay the source of truth.

## 4. Safety contract

- Twin-parity per builtin (extend the E-05 merge corpus pattern, not
  the shared list unless green): arg/filter/arg2/order-keys over the
  agg corpus (ints, numeric, text, NULLs, div-by-zero positions AND
  error text at every read site per F2, `FILTER (WHERE …)`
  true/false/NULL, ≥1 `DISTINCT` case per builtin — the compiled Arg
  flows into `datumKey` dedup). Explicitly OUT (documented, not
  tested): volatile `FuncCall` (value parity meaningless across
  evaluations — Adapter on both twins by construction),
  `CTIDExpr`/`MergeWholeRowRef`/outer-scope refs (no agg-arg
  position can produce them — they never reach the compiled nodes).
- `MIXED`/spill/grouping-set behavior unchanged — enforced as a
  NEGATIVE diff rule (no `groupingsets.go`-style executor lane
  exists; the executor has one unified set loop): no NEW BRANCH in
  the grouping-set loop, `openSorted`, `finalizeGroup`, `finishAgg`,
  `parallel_agg_*.go`, `spill.go`, or `join_batch.go` (line-touches
  that thread an index through a pre-existing call, like the two
  `applyAgg` call sites, are not branches).
- Alloc arm MANDATORY (per-row transition is THE hot loop for OLAP):
  per-row delta ≤ 0 vs interpreted (node lists prebuilt; no per-row
  slot alloc — `applyAgg` already receives `slot`).

## 5. Gate

- Twin-parity tests (new file, per-builtin matrix).
- Optimizer + executor suites + units scope green.
- R8 values-diff BOTH suites (TPC-H Q1/Q3/Q4/Q5/Q6/Q7/Q10 agg-heavy;
  TPC-DS sweep `CKMISMATCH=0`) + plan-shape pin (zero moves) +
  spotcheck + one parallel Q1-shaped values test with workers on
  (Partial workers share `applyAgg` — compiled-transfn × combine
  interplay on int/numeric lanes, `floatSpecial`, variance
  correction must execute under the new evaluator).
- Timing arm: per-row transfn slice measured on the final site list
  (microbenchmark on `applyAgg` hot loop or Q1/Q6-class shape
  before/after); alloc arm asserts ≤ 0.
