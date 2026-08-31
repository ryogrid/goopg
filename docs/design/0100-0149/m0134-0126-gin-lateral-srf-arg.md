# M0134-0126 — `gin.sql` sizing + user-defined-FROM-function LATERAL arg fix

Status: PARKED (`failed`), one engine-wide fix shipped, case does not flip green.

## Sizing

`scripts/pg-regress-runner.sh gin` at HEAD (before this loop): 0% parity,
218 diff lines against `postgres/src/test/regress/expected/gin.out` (183-line
source). Four independent root causes:

1. **No GIN physical-index plan integration.** `EXPLAIN (costs off) SELECT
   count(*) FROM gin_test_tbl WHERE i @> array[1, 999]` expects `Bitmap Heap
   Scan` / `Bitmap Index Scan` on the GIN index; goopg plans a `Seq Scan`
   with a `Filter`. A `USING gin` index is catalog-only in goopg today — no
   planner path ever considers it for `@>`/`<@`/`&&` quals. REFACTOR-tier,
   its own subsystem (parallel to the GiST catalog-only gap documented in
   `goopg_gist_grid_cell_ssi` memory / M0097 area).
2. **`gin_clean_pending_list(regclass)` unimplemented** — the fastupdate
   pending-list flush function used four times in the test (`pg_proc` has no
   seed row and the executor has no dispatch case). REFACTOR-tier: requires
   the GIN fastupdate pending-list buffer mechanism, which doesn't exist.
3. **`gin_fuzzy_search_limit` GUC unregistered** — `SET
   gin_fuzzy_search_limit = 1000` raises `unrecognized configuration
   parameter`. Small and independently landable (even a no-op int GUC would
   silence the 2 error lines), not claimed this loop — lower priority than
   the LATERAL fix below and orthogonal to it.
4. **plpgsql `RETURN QUERY EXECUTE '...' || arg` dynamic-SQL parsing gap** —
   newly EXPOSED by the fix below (previously masked because the whole
   `WITH ... LATERAL explain_query_json(...)` block failed earlier at plan
   time with "column query does not exist"). `explain_query_json`'s body is
   `RETURN QUERY EXECUTE 'EXPLAIN (ANALYZE, FORMAT json) ' || query_sql`; the
   plpgsql parser cannot handle a `RETURN QUERY EXECUTE <expr>` whose
   argument is a `||`-concatenation rather than a bare string/identifier.
   Root cause not investigated further this loop (out of the LATERAL fix's
   scope); flagged as the next resume point for this file.

Net diff count is unchanged at 218 lines because the shipped fix (below) just
moves the failure point from "column does not exist" (bucket 4's masking
symptom) to "RETURN QUERY: syntax error" (bucket 4's real cause) within the
same test block — a wash on line count but real progress: the LATERAL
correlation bug is gone and confirmed fixed independently (see Executor
verification below), and it was general enough to be worth shipping on its
own regardless of whether it moves `gin.sql`'s score.

## Shipped fix: user-defined FROM-clause function LATERAL arg resolution

**Not gin-specific.** `gin.sql`'s
```sql
lateral explain_query_json($$select * from t_gin_test_tbl where $$ || query) js
```
merely exposed a general engine bug: **any** user-defined function invoked in
the FROM clause (implicit-LATERAL comma-join or explicit `LATERAL`) with an
argument correlating against an earlier FROM item raised `column "..." does
not exist` at plan time — for both SETOF and non-SETOF routines, qualified or
unqualified column reference, VALUES-list or base-table source. Minimal repro:

```sql
create table t(query text);
select js from t, lateral my_scalar_func(query) js;   -- was: column "query" does not exist
select js from t, my_setof_func(t.query) js;           -- was: column "query" does not exist
```

### Root cause (plan-time)

`planTableFuncRangeVar` (`internal/optimizer/planner.go`) dispatches known
builtin FROM-clause SRFs (`pg_get_publication_tables`, `verify_heapam`,
`pg_get_sequence_data`, `pg_options_to_table`, `unnest`,
`regexp_split_to_table`, `generate_subscripts`, `generate_series`, …) through
individual `planX` helpers, each of which builds an arg-resolution context
that chains `lateralCtx` (lateral siblings already in scope) with
`planParent` (outer-query correlation) — see e.g. the `pg_options_to_table`
comment "Mirrors generate_series." The **fallback branch for user-defined
routines** (`cat.Routines().LookupByName`, reached when the function name
doesn't match any of the builtin dispatches) instead built
`ctx := &resolveContext{}` — completely empty, ignoring `lateralCtx` and
`planParent` both. Any correlated arg expression therefore resolved against
zero visible columns and failed to parse-bind.

Fix: build the arg-resolution context identically to the sibling builtin
paths (`generate_series`, `pg_options_to_table`) — chain `lateralCtx` with
`planParent`, falling back to a fresh `cat`-scoped context with `planParent`
as parent when `lateralCtx` itself has no parent set yet.

### Root cause (run-time) — the naive fix would still be broken

`nodeReferencesOuter` (the function that decides whether the wrapping `Join`
must be flagged `Lateral` so the executor drives the SRF per outer row via
`BindLateralOuter`, versus planning a plain cross join) had no case for
`*ScalarFuncScan` or `*UserSrfScan` — it fell through to the generic
`planHasOuterRef` walk, which only recognizes `*OuterColumnRef` (real
subquery-correlation Vars), not the plain `*ColumnRef` a lateral-sibling
resolution produces (same convention `*PgGetPublicationTables`/
`*PgGetSequenceData` already needed their own case for). Added matching
cases for both node types.

Even with the Join now correctly flagged `Lateral`, `scalarFuncScanOp` and
`userSrfScanOp` (`internal/executor/operators_scalar_func_scan.go`,
`operators_user_srf_scan.go`) had no `BindLateralOuter` method at all — they
always evaluated their function/args against a `nil` slot
(`evalExpr(o.plan.Func, nil, o.ctx)` / `evalExprSlot(argExpr, nil, ctx)`).
Verified live: a plan-only fix reproduces as `column ref query/0 on nil
slot` at runtime instead of a plan-time error — strictly worse (a query that
now "plans" but crashes on execution). Added `outerSlot SlotView` +
`BindLateralOuter` to both operators (mirroring `pgGetSequenceDataOp`,
`internal/executor/operators_pg_get_sequence_data.go`), wired the stored
`outerSlot` into the existing eval calls, and reset `scalarFuncScanOp.done`
in `Open()` (previously only zero-valued at construction — needed once the
op gets re-`Open`ed per outer row under a lateral join).

### Files changed

- `internal/optimizer/planner.go`: `planTableFuncRangeVar`'s user-routine
  branch arg-context construction; `nodeReferencesOuter` gains
  `*ScalarFuncScan`/`*UserSrfScan` cases.
- `internal/executor/operators_scalar_func_scan.go`: `outerSlot` field +
  `BindLateralOuter`; `Open()` resets `done`; `Next()` evaluates via
  `evalExprSlot(o.plan.Func, o.outerSlot, o.ctx)` instead of
  `evalExpr(o.plan.Func, nil, o.ctx)`.
- `internal/executor/operators_user_srf_scan.go`: `outerSlot` field +
  `BindLateralOuter`; `Open()` evaluates args via
  `evalExprSlot(argExpr, o.outerSlot, ctx)` instead of `nil`.

### Verification

- `TestPlanUserFuncLateralArgResolvesAgainstLeftFromItem`
  (`internal/optimizer/planner_test.go`, setof + scalar subtests): plans
  `SELECT js FROM t_probe, LATERAL myfunc(t_probe.query) js` for both a
  SETOF and non-SETOF user routine, asserts it plans without error and the
  wrapping `Join` is flagged `Lateral`.
- `TestScalarFuncScanFrom_LateralArgResolvesAgainstLeftFromItem`
  (`internal/executor/scalar_func_scan_from_test.go`): end-to-end, both node
  shapes, asserts the correct per-row values come back (not just that
  planning succeeds) — this is the test that would have caught the
  `BindLateralOuter`-missing runtime gap a plan-only fix would have left
  behind.
- `scripts/tpch-spotcheck.sh`: PASS (Q12=2, Q13=35).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: PASS (all
  packages, including `internal/optimizer` and `internal/executor`).

## Resume points (next M0134-0126 loop, or standalone follow-ups)

1. **`gin_fuzzy_search_limit` GUC** — smallest remaining `gin.sql` item;
   register as an integer GUC (PG default 0, `postgresql.conf.sample`) even
   without wiring real fuzzy-search behavior — silences 2 of the 218 diff
   lines with near-zero risk.
2. **plpgsql `RETURN QUERY EXECUTE <concat-expr>`** — newly unmasked bucket
   4 above; needs its own repro outside `gin.sql` to confirm scope (is it
   specific to `||` concatenation, or any non-literal EXECUTE target in
   `RETURN QUERY`?).
3. **GIN physical-index planning + `gin_clean_pending_list`** — the two
   REFACTOR-tier buckets (1 and 2 above); together they're the majority of
   the remaining 218 lines. Candidate for a dedicated GIN-index milestone
   parallel to the GiST catalog-only-index gap.
