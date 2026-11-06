# M0148\-0001i — user\-defined aggregates split across Gather (COMBINEFUNC)

**Parent:** M0148\-0001 `aggregates.sql` regress coverage closure · **Status:**
slices 1\-2 landed 2026\-11\-03; closed (the view\-pull\-up residue of the
`v_pagg_test` hunk moved to M0148\-0001i1)

## Target

`aggregates.sql` (expected\-line ~2880) creates `balk(int4)`:
`SFUNC = int4_sum(int8, int4)`, `COMBINEFUNC = balkifnull(int8, int8)`,
`PARALLEL = SAFE`, `INITCOND = '0'`. It forces parallelism with
`parallel_workers = 4` and `parallel_setup_cost = 0`, then expects:

    Finalize Aggregate -> Gather (4) -> Partial Aggregate
      -> Parallel Index Only Scan using tenk1_hundred on tenk1

goopg planned `Aggregate -> Index Only Scan`.

## Findings

- **The split gate.** `optimizer.AggregateIsDecomposable` refused every user
  aggregate, and the executor's combine step (`combineAggRuntime`) had no
  rule to call a user COMBINEFUNC. The built\-in `sum(hundred)` already split
  into PG's shape.
- **Wrong serial results (S2).** In a serial plan, the finish path called
  `COMBINEFUNC(NULL, state)` (M0097\-0122), imitating the parallel `balk()`
  NULL. PG never calls the combine function outside the combine phase. Two
  measurements on PG 18.3:
  - `p_csum` (raising COMBINEFUNC) returns `6` serially;
  - `mysum8` (`int4_sum` + `int8pl`) returns `495000` serially and in
    parallel.
  goopg returned `NULL` for a strict SQL COMBINEFUNC, and the opposite
  expectation was pinned in `TestRaisingSFuncAbortsStatement/CombineFunc`.
- **Wrong serial results (S2).** The builtin SFUNC `int4_sum` was missing
  from `executeSFuncCall`'s table. The call fell through as "not found" and
  every row kept INITCOND, so `mysum8` returned `0`.
- **Parser.** `PARALLEL` and `SERIALFUNC` were parsed and discarded.

## Change

- **Parser and catalog.** `CreateAggregateStmt` and `catalog.UserAggregate`
  carry `Parallel` and `SerialFunc`. CREATE AGGREGATE also records
  `CombineFuncStrict`: from the routine registry for a user function, and
  true for a builtin (every pg\_proc.dat function usable as aggcombinefn is
  strict).
- **Split gate.** `AggregateIsDecomposable` admits a user aggregate that has
  a COMBINEFUNC and is PARALLEL SAFE (unspecified means unsafe, PG's
  default). With an internal state type it also needs a SERIALFUNC
  (planner.c can\_partial\_agg / clauses.c).
- **Combine phase.** `combineAggRuntime` now takes the call and the executor
  context, and gains a user arm, `combineUserAggState`, modelled on
  nodeAgg's combine phase. That phase substitutes aggcombinefn as the
  transition function and keeps agginitval as the initial state:
  - the combine state starts from INITCOND;
  - a worker with no rows contributes its initial state;
  - a strict COMBINEFUNC skips a NULL partial, adopts the first partial
    into a NULL initial state without a call, and leaves a NULL state NULL
    (execExprInterp.c `EEOP_AGG_PLAIN_TRANS_INIT_STRICT_*`);
  - a raising COMBINEFUNC aborts the statement.
- **Accumulator.** The shared\-accumulator route
  (`aggPartialAccum.merge`) combines the first partial into an INITCOND
  state rather than adopting it.
- **Row transport.** `serializeAggCall` / `deserializeAggCall` ship the user
  state Datum with `hasValue` and `userStateSet`.
- **Serial fixes.** The serial finish no longer calls the COMBINEFUNC, and
  `int2_sum` / `int4_sum` were added with numeric.c's non\-strict semantics.

## Verification

- Probe: `balk(hundred)` plans `Finalize Aggregate -> Gather (4) -> Partial
  Aggregate -> Parallel Index Only Scan`. A SQL\-SFUNC user sum returns
  495000 serially and in parallel.
- Regress runner, HEAD → change:
  - aggregates goes from 231 to 209 diff lines and from 10 to 9 hunks; the
    `balk` hunk matches;
  - create\_aggregate is byte\-identical.
- Pins:
  - `TestCombineUserAggStateStrictRules`
  - `TestUserAggStateTransportRoundTrip`
  - `TestSerialUserAggregateWithBuiltinSFuncAndCombineFunc` — mutation
    checks: restoring the serial combine call gives `NULL`, and removing
    `int4_sum` gives `0`
  - `TestSerialAggregateNeverCallsCombineFunc` — PG 18.3 returns 6
  - `TestAggregateIsDecomposableWhitelist` — user\-aggregate cases

## Slice 2 — `array_agg`/`string_agg` split; views planned under session settings

`v_pagg_test` (expected\-line ~1929) aggregates `string_agg`/`array_agg` per
`y` in a subquery, unnests the results, and re\-aggregates. PG plans
`Finalize HashAggregate -> Gather -> Partial HashAggregate -> Parallel Seq
Scan` for the inner aggregate. Two causes kept goopg serial:

- **`array_agg`/`string_agg` were refused** as "order\-dependent", on the
  claim that PG treats them as parallel\-restricted. That is wrong for PG 18:
  both have combine functions (`array_agg_combine`,
  `array_agg_array_combine`, `string_agg_combine`) and are PARALLEL SAFE.
  - Their unordered, non\-DISTINCT forms are now decomposable.
  - Combine follows PG. `string_agg` appends the second state *with* its
    leading delimiter, kept as `strFirstDelim` because PG's state keeps the
    first delimiter and skips it only in the final function. `array_agg`
    concatenates the elements and their NULL flags; for array inputs the
    dimensions must agree (2202E).
  - Both states travel through the row transport.
  - PG does not split `array_agg` over an anonymous RECORD argument
    (parse\-agg.c `agg_args_support_sendreceive`), and neither does goopg.
    goopg types a table's whole\-row reference as `record` too, so it also
    keeps `array_agg(p)` over a named composite whole, where PG splits it
    (ledger).
- **Views were planned under the default settings.** `planScanRangeVar`
  called `Plan(tbl.View, cat)`, so none of the session's planner GUCs,
  `parallel_setup_cost = 0` among them, reached a view's body. It now calls
  `PlanWithSettings(tbl.View, cat, ps)`, as PG plans a view after the
  rewriter expands it into a subquery.

Verification:

- The inner aggregate of `v_pagg_test` now plans PG's split.
- The aggregates single\-suite run goes from 209 to 208 diff lines (still 9
  hunks).
- create\_view, rules, select\_views, with, select\_parallel, matview,
  groupingsets and create\_aggregate are byte\-identical against HEAD.
- `TestPort_RegressSuite`, `TestPort_PgDump*` and `TestPort_Isolation*`
  pass; the fire\-set shows no changed plan on any corpus.
- Pins: `TestUnorderedStringArrayAggSplit` (split, multiset\-equal to
  serial) and `TestStringArrayAggCombineAndTransport` (delimiter, NULL
  flags, dimensions, round trip). The refusal pins now use the ORDER BY,
  DISTINCT and ordered\-set forms.

## Remaining (M0148\-0001i1)

- The rest of the `v_pagg_test` hunk comes from goopg planning a view as an
  opaque sub\-plan; PG pulls the view's subquery up into the outer query:
  - a redundant top `Sort` for `order by y`, which the outer GroupAggregate
    already provides;
  - no `Result` node between the Sort and the ProjectSet;
  - unqualified names (`Group Key: y`, not `pagg_test.y`);
  - the sort key printed as `t`, not the cast expression.
- **Pre\-existing:** an expression over a split aggregate's NullTest
  (`sum(hundred) IS NULL`) fails in the Finalize with
  `column ref … out of Slot range`, at HEAD for builtins too.
  `sum(hundred) + 1` works.
- Unknown builtin SFUNC / COMBINEFUNC names still fall through silently;
  `executeSFuncCall` is a hand\-written table.
- `Parallel`, `SerialFunc` and `CombineFuncStrict` are not persisted in the
  pg\_aggregate / pg\_proc rows (`writeAggregateCatalogRows`). After a
  restart a user aggregate stops splitting, the fail\-closed direction.
