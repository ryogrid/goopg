# M0134-0015 — `join.sql`: legacy inheritance star-suffix parse, and FROM-clause scalar functions

Status: accepted (slices C and D landed; case PARKED)
Task: `.ralph/fix_plan.md` M0134-0015
Case: `postgres/src/test/regress/sql/join.sql`

## 1. Sizing at HEAD

`scripts/pg-regress-runner.sh --verbose join` at HEAD: **FAIL**, 20920 diff
lines / 146 `^+ERROR` lines / 140 hunks.

The raw line count massively overstates the number of distinct defects. A large
fraction of it is **plan-shape divergence** (Hash vs Merge Join, Seq vs
Index/Bitmap Scan) in `EXPLAIN` output — the already-tracked cost-model gap
(`docs/design/cost-model/`), explicitly out of scope for M0134. The real
divergences bucket into six independent root causes:

| bucket | cause | tier |
|---|---|---|
| A | unparenthesized/chained nested join trees with multiple trailing `ON` clauses; paren-wrapped joins whose left arm is a parenthesized derived table (`internal/parser/select.go:1300-1359` lacks a real recursive `table_ref`/`joined_table` grammar) | REFACTOR |
| B | `JOIN ... USING (cols) AS alias` (PG 17 join-using alias) unparsed; `JoinExpr` (`internal/parser/ast.go:698`) has no alias field, and the binder must expose the merged columns under it | small, real design cost; 9 hits |
| C | non-`SETOF` (scalar) user functions rejected as FROM-clause sources | CONTAINED |
| D | `ALTER TABLE tbl* RENAME COLUMN ...` legacy star suffix mis-parses | TRIVIAL, high leverage |
| E | btree v0 has no composite/`RECORD` key comparator (`btree v0 only supports int4 / numeric keys`), cascading into an aborted transaction block | ledger |
| F | `value ... out of int4 range for index key` where PG treats an out-of-range bigint probe as "no match" rather than an error | unpinned; do not fix blind |

## 2. Slice D — the legacy inheritance star suffix (LANDED)

### Symptom

```
ERROR:  syntax error at or near "expected ADD or DROP (got *)"
LINE 1: ALTER TABLE b_star* RENAME COLUMN b TO bb;
```

This lives in the **shared `create_misc.sql` setup**, not in `join.sql` itself —
PG's `parallel_schedule` records "join depends on create_misc". Because the
rename silently never happened, `join.sql:394-425`
(`select aa, bb, unique1, unique1 from tenk1 right join b_star on aa = unique1`)
failed with `column "aa" does not exist`. It is a genuine goopg engine bug, not
a runner artifact, and it corrupts the setup for every regress file sharing it.

### Root cause

`internal/parser/ddl.go:8829` tested the trailing `*` with

```go
if p.cur().Kind == TokenOperator && p.cur().Value == "*" {
```

but the lexer emits `*` as `TokenSymbol` (`internal/parser/lexer.go:391,424`) —
it is never `TokenOperator`. The check could therefore never fire, and the `*`
was left in the stream to be rejected by the ALTER subcommand dispatcher.

Every other star-suffix site in the parser already uses `TokenSymbol`
(`ddl.go:5827`, `parser.go:1855,3484`, `select.go:1121,3039,4134,4285`);
`ddl.go:8829` was the sole outlier — verified exhaustively by grepping for
`TokenOperator` compared against `"*"` across `internal/parser/`.

### Fix and PG semantics

One line: `TokenOperator` → `TokenSymbol`.

The suffix is correctly consumed as a **no-op marker**. In PG 18.3 a trailing
`*` on an ALTER TABLE relation name means "and descendants"
(`postgres/src/backend/parser/gram.y`, `relation_expr`; `interpretInhOption`),
which is already the default when `ONLY` is absent. goopg matches: `stmt.Only`
defaults to false and every executor gate reads `s.Only`, so recursion into
descendants already happens. No behavior is dropped, hence no deferral row.

### Tests

- `internal/parser/alter_test.go` — `TestParseAlterTableInheritanceStarSuffix`
  (RENAME COLUMN and ADD COLUMN forms; the second proves the suffix is consumed
  generally rather than only ahead of RENAME).
- `internal/executor/operators_ddl_inherit_guards_test.go` —
  `TestAlterTableInheritGuardsRenameColumnStarSuffix`, executing the literal
  `ALTER TABLE b_star* RENAME COLUMN b TO bb` acceptance SQL.

Both FAIL pre-fix (`syntax error at or near "expected ADD or DROP (got *)"`)
and PASS post-fix.

## 3. Slice C — non-SETOF routines as FROM-clause sources (LANDED)

### Symptom

```
ERROR:  table-valued function "f_immutable_int4" not supported
ERROR:  table-valued function "mki8" not supported
```

14 direct hits in the contiguous block `join.sql:1511-1581`, e.g.

```sql
select unique1 from tenk1, (select * from f_immutable_int4(1) x) x where x = unique1;
select * from mki8(1,2);
```

where `f_immutable_int4` is a plain `returns int` routine — **not** `RETURNS
SETOF`.

### Root cause

`planTableFuncRangeVar` (`internal/optimizer/planner.go:4479`) resolves a
FROM-clause function by looking up candidate routines and gating on a single
condition, `if r.ReturnsSet` (`:4583`). A matching non-set routine simply fails
the `if`, the loop body is skipped, and control falls out to the terminal
`0A000 table-valued function %q not supported` at `:4679-4680`.

The set-ness gate is a goopg artifact. PG applies **the same machinery**
regardless of `proretset`: `transformRangeFunction`
(`postgres/src/backend/parser/parse_clause.c:463`) builds
funcnames/funcexprs/coldeflists for every function in the FROM list and calls
`buildNSItemFromLists`; the result type comes from `get_expr_result_type` /
`get_func_result_type` (`postgres/src/backend/utils/fmgr/funcapi.c:299,410`) in
both cases. There is no per-set-ness naming or typing rule to reproduce.

Execution differs in exactly one respect:
`ExecMakeTableFunctionResult` (`postgres/src/backend/executor/execSRF.c:101`)
branches on `setexpr->funcReturnsSet` (`:174`) — a non-set function is called
**once** and yields **exactly one row**. Critically, the `no_function_result:`
block (`~:386-410`) manufactures **one row of all-NULLs** when a non-set
function produced no result, so a non-SETOF function in FROM **never yields zero
rows even when it returns NULL** — unlike a SETOF function returning zero rows.

### Fix

Both members of a sibling pair had to move, and the brief said so up front:

1. `internal/optimizer/planner.go` — the existing three-way column-schema block
   (OUT-parameter routine / composite-or-table return type / plain scalar
   return, previously `:4593-4667`) was **factored** into a new
   `userRoutineColumnSchema` helper called from BOTH the `r.ReturnsSet` arm
   (→ `*UserSrfScan`) and the new `!r.ReturnsSet` arm (→ `*ScalarFuncScan`
   carrying the full schema). Copy-pasting it would have created a second
   divergent copy of "how do we name columns for routine r" — the recurring
   `pattern_sibling_paths_must_agree` failure. `WITH ORDINALITY` wrapping was
   verified generic and now covers both arms.
2. `internal/executor/operators_scalar_func_scan.go` — `scalarFuncScanOp.Next()`
   returned `Row{val}` unconditionally, with no composite branch. It now carries
   the `len(sch) > 1 && d.Kind == KindString` → `decomposeCompositeText` branch
   ported from its sibling `userSrfScanOp.Next()`
   (`operators_user_srf_scan.go:55-69`, helper `:73-91`).

No new evaluator was needed: `ScalarFuncScan`'s executor op already implements
PG's "exactly one call, exactly one row" contract, and `evalFuncCall` →
`executeStoredRoutine` (`internal/executor/plpgsql_runtime.go:313`) is the same
scalar-call path that already served `WHERE x = f_immutable_int4(1)`. Only the
FROM-clause *routing* was missing. No parser changes: every error site echoed
unmangled SQL, proving the statements already parsed.

The sibling-drift risk was demonstrated empirically rather than argued: stashing
only the executor file while keeping the planner change makes
`TestScalarFuncScanFrom_CompositeExpands` fail with
`XX000: column ref q2/1 out of MaterializedSlot range 1` — i.e. planning
multi-column while executing one crammed composite-text datum.

### Tests

`internal/executor/scalar_func_scan_from_test.go` — 6 tests: scalar return via
subquery form; direct FROM item with alias; **composite return expanding to
multiple columns** (the test that pins the executor sibling); NULL result still
yielding one all-NULL row (PG's `no_function_result` semantics); no-alias column
naming; and non-regression of the existing SETOF path and the `parse_ident` /
`pg_input_error_info` builtin callers of `planScalarFuncScan`.

## 4. Result and why the case stays `failed`

| metric | HEAD | after |
|---|---|---|
| `^+ERROR` lines | 146 | **131** (-15) |
| diff lines | 20920 | 20946 (+26) |

The `^+ERROR` count is the meaningful metric. The diff-line count *rose* because
statements that previously aborted with an error now execute and emit real
result rows and `EXPLAIN` output — which then differ in plan shape, the
out-of-scope cost-model gap. Do not read +26 lines as a regression; read
-15 errors as the progress. `table-valued function ... not supported` went
14 → 0, and `ALTER TABLE b_star* RENAME COLUMN b TO bb` now succeeds in the
shared setup (the `column "bb" does not exist` signature is fully gone).

**M0134-0015 is PARKED, not closed.** The CSV row stays `failed`,
`pass_required` stays `no`, and `make regen-testport` is NOT run. Bucket A (a
real recursive `table_ref`/`joined_table` grammar) is REFACTOR-tier and is the
dominant remaining contributor; buckets B, E, F and the discoveries below are
ledgered.

## 5. Ledgered non-goals (see `.ralph/deferral_ledger.md`, 2026-08-20)

- **`ALTER TABLE <root>* RENAME COLUMN` does not propagate to inheritance
  children.** Surfaced only after slice D made the statement parse:
  `create_misc.sql:215`'s `ALTER TABLE a_star* RENAME COLUMN a TO aa` now
  succeeds, yet `join.sql:~394` still reports `column "aa" does not exist` on
  descendant `b_star` (2 hits). The parser fix is table-identity-agnostic, so
  this is a distinct executor-side inheritance-recursion gap. A textbook
  instance of the M0134 pattern: fixing one defect unmasks the next.
- Bucket A — nested/chained join trees with multiple trailing `ON` clauses
  (grammar refactor).
- Bucket B — `JOIN ... USING (cols) AS alias`.
- Bucket E — btree v0 lacks a composite/`RECORD` key comparator.
- Bucket F — out-of-int4-range index probe errors where PG returns no match.
- `ROWS FROM(f1(), f2())` mixing set and non-set members — PG lock-steps members
  and NULL-pads exhausted ones (`execSRF.c`); `planRowsFrom` has no non-set arm.
- `LATERAL` correlation to outer FROM-item columns for function scans — a
  **pre-existing** gap shared by the already-shipped SETOF path (`:4584` uses a
  bare `resolveContext{}` with no parent) and `planScalarFuncScan` (no
  `lateralCtx` parameter). Not introduced here and not needed by join.sql's
  literal-argument cases, but it does NOT work after this slice.
- Column-ref resolution against a composite-returning table function:
  `SELECT q1 FROM mki8(1,2)` raises `42703` while `SELECT *` sees the same
  columns. Reproduces identically on the pre-existing SETOF path, so it predates
  this slice.
- `decomposeCompositeText` never type-directs its decode — composite `int8`
  columns come back as `KindString` datums.
- `drop function f_immutable_int4(int)` → "does not exist" despite a successful
  `CREATE FUNCTION` and successful arg resolution: a separate DROP FUNCTION
  signature-matching gap that does not self-resolve with this slice.
