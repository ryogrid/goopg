# M0134-0187 — `generated_stored.sql`: INSERT/generated-column ordering and arity fixes

Status: **contained fix shipped, case stays `failed`** (2026-09-01). Sized
live for the first time; three independent bugs in the shared VALUES-form
INSERT/generated-column path fixed, dropping 67 diff lines and 13
`^+ERROR` shapes. Full pass blocked by six further, independently-scoped
gaps (see below) — none REFACTOR-tier alone, but none contained enough to
land alongside the fixes this loop already shipped.

## What the file tests

`postgres/src/test/regress/sql/generated_stored.sql` (756 lines) exercises
PostgreSQL's `GENERATED ALWAYS AS (expr) STORED` column support end to end:
`information_schema` introspection, CREATE-TABLE-time validation of the
generation expression (self-reference, whole-row reference, invalid
column, non-immutable functions, DEFAULT/IDENTITY conflicts), INSERT/UPDATE
arity and DEFAULT-only enforcement, NOT NULL/CHECK/UNIQUE/FK/PARTITION
interaction, triggers observing the pre-generation row, `LIKE INCLUDING
GENERATED`, and `ALTER TABLE ... ALTER COLUMN ... SET EXPRESSION AS`.

## Sizing (this loop, 2026-09-01)

`scripts/pg-regress-runner.sh -v generated_stored`: **0/1 PASS**. Before any
fix: 1675 diff lines, 72 `^+ERROR` shapes. After the three fixes below:
**1608 diff lines, 59 `^+ERROR` shapes**.

## Fixes landed

### 1. Implicit INSERT column list must include generated columns

goopg's `resolveInsertTargetColumns` (analyzer) and both `colIndex`
builders in `internal/optimizer/planner.go` (`rewriteInsertDefaultMarkers`
and `planInsert`'s VALUES-form branch) excluded `GENERATED ALWAYS AS …
STORED` columns from the *implicit* (no explicit column list) target-column
set entirely — "they're computed by the executor" (M0096-0008). Real
PostgreSQL's `checkInsertTargets` (`postgres/src/backend/parser/
parse_target.c`) does **not** filter `attgenerated` columns out of the
default target list at all; the exclusion was simply wrong, and it broke
every statement in the file that addresses the generated column's own
position — most visibly `INSERT INTO gtest1 VALUES (2, DEFAULT)`, which PG
accepts (explicit DEFAULT for a generated column is legal) but goopg
rejected with a fabricated `INSERT row has 2 values, target expects 1`
(14 occurrences of this exact wrong error in the original diff).

Fix: the implicit column list now includes every column, generated or not,
in all three sites (kept in lockstep per the existing cross-file comments
tying them together). A generated column's cell may still only ever be
`DEFAULT` — enforced by fix #1b below — or simply absent (a VALUES row
shorter than the column count, still legal with no explicit column list).
`INSERT ... SELECT`'s *implicit*-list colIndex construction is deliberately
**left unchanged** (still excludes generated columns) — that form has no
`DEFAULT` spelling to legitimately target one with, the file has no test
exercising it, and extending the fix there would need its own validation
design; gated behind `s.Select == nil` in both `planInsert` colIndex sites.

### 1b. Reject a non-DEFAULT value in a generated column's cell

New check in `rewriteInsertDefaultMarkers`'s row-substitution loop: once a
cell's target ordinal (via the now-fixed `colIndex`) resolves to a
generated column, only a `*parser.DefaultMarker` may occupy it — anything
else (even a literal that happens to equal what the expression would
compute) raises PostgreSQL's own error, verbatim:

```
ERROR:  cannot insert a non-DEFAULT value into column "b"
DETAIL:  Column "b" is a generated column.
```

SQLSTATE `428C9` (`ERRCODE_GENERATED_ALWAYS`), matching
`postgres/src/backend/rewrite/rewriteHandler.c`'s
`rewriteTargetListIU`. Returned as a `*optimizer.PlanError` (which already
has a `Detail` field the wire layer renders) so `toPlanError` passes it
through unchanged.

**Known gap:** this check lives in `rewriteInsertDefaultMarkers`, which
resolves the INSERT target via a direct `cat.LookupTable(s.Target)` — it
does not follow the view->base-table chain `planInsert` resolves for an
updatable view target. `INSERT INTO <view> VALUES (...)` therefore does
**not** get this protection (ledgered below, bucket d).

### 2. `computeGeneratedColumns` must run before NOT NULL / CHECK enforcement

`internal/executor/operators_storage.go`'s INSERT path called
`computeGeneratedColumns` immediately before partition routing — well
*after* the NOT NULL and CHECK constraint checks. Upstream's order
(`nodeModifyTable.c`) is `ExecBRInsertTriggers` -> `ExecComputeStoredGenerated`
-> `ExecConstraints`. Because goopg checked constraints against the
pre-computation placeholder (`NullDatum`), any **NOT NULL-declared**
generated column raised a false violation even when its expression would
evaluate to a non-null result — e.g. `GENERATED ALWAYS AS (nullif(a, 0))
STORED NOT NULL` with `a = 1` should insert `b = 1` cleanly, but goopg
rejected it as a NOT NULL violation on the still-null placeholder.

Fix: moved the `computeGeneratedColumns(cols, row)` call to right after the
BEFORE INSERT trigger block (so triggers still see the pre-generation row,
matching upstream) and before the NOT NULL check. Partition routing already
ran after the old call site, so its relative order (generate-before-route,
needed since `PARTITION BY` may key on a generated column) is unchanged —
only the constraint checks moved to see the computed value.

### 3. `nullif`/`coalesce` in the generated-expression mini-evaluator

`evalGenFuncCall` (`internal/executor/operators_generated.go`) is a small
hand-written expression evaluator used for both generated-column
computation and DEFAULT-expression evaluation; its function whitelist had
no `nullif` or `coalesce` arm. Because the fallthrough for an unrecognised
function is `return NullDatum, nil` — success, not an error — this was
silently indistinguishable from a legitimately NULL result: fix #2 above
made the ordering bug visible as fix #3's actual root cause, since
`nullif(a, 0)` always evaluated to NULL regardless of `a`.

Fix: added `nullif`/`coalesce` arms mirroring `expr.go`'s
`evalExprSlot` reference implementation (`Format()`-based equality for
`nullif`, first-non-null scan for `coalesce`).

## Tests

- `internal/optimizer/planner_test.go`: replaced
  `TestPlanInsertDefaultValuesSkipsGeneratedColumns` (asserted the old,
  wrong exclusion) with `TestPlanInsertDefaultValuesIncludesGeneratedColumns`
  and added `TestPlanInsertValuesRejectsNonDefaultIntoGeneratedColumn`
  (covers the reject-a-value case, the accept-DEFAULT case, and the
  short-row-implicitly-omitted case).
- `internal/executor/generated_column_insert_test.go` (new):
  `TestInsertGeneratedColumnComputedBeforeNotNullCheck`,
  `TestInsertGeneratedColumnAcceptsDefaultRejectsValue`,
  `TestGeneratedColumnEvaluatesCoalesce`.

## Gates run

- `go build ./...` clean.
- `go test ./internal/optimizer/... ./internal/parser/... ./internal/parser/analyzer/... ./internal/catalog/... ./internal/executor/...` — all PASS.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — PASS.
- `scripts/tpch-spotcheck.sh` — PASS (Q12=2 rows, Q13=34 rows).
- `scripts/pg-regress-runner.sh -v generated_stored` — 0/1 PASS (expected;
  case stays out of the pass-required set), 1608 diff lines / 59 `^+ERROR`
  (down from 1675 / 72).
- Spot-checked `insert.sql` and `generated_virtual.sql` (siblings that also
  exercise INSERT arity / generated columns) still fail at 0% parity with
  no new divergence attributable to this loop's change (the one arity-
  message-wording mismatch visible in `insert.sql`'s diff is a pre-existing,
  unrelated plain-column case this loop did not touch).

## Remaining gaps (deferral ledger, M0134-0187 row, 2026-09-01)

Six further independently-verified buckets, none attempted this loop:

- **(a) `information_schema.columns` / `.column_column_usage` missing** —
  both raise `relation "..." does not exist`; the file's first real
  assertions (column introspection) fail outright.
- **(b) generated-column CREATE-TABLE-time validation entirely absent** —
  duplicate generation clause, self/mutual generated-column reference,
  whole-row-var self-reference, invalid column reference, non-immutable
  expression, DEFAULT+GENERATED conflict, IDENTITY+GENERATED conflict: PG
  raises one of 7 distinct errors for each `gtest_err_N` table; goopg
  creates all of them without complaint.
- **(c) whole-row `Var` evaluation inside a CHECK constraint** — `CHECK
  (gtest20c IS NOT NULL)` (a bare table-name reference used as a
  whole-row/composite value) errors "could not evaluate check constraint
  … column \"gtest20c\" does not exist" instead of resolving to the row's
  composite value.
- **(d) INSERT-through-VIEW bypasses the fix #1b protection** — see "Known
  gap" above; a view target's generated column silently accepts a real
  value, and the resulting bad rows cascade into spurious duplicate-key
  errors on later statements in the file.
- **(e) `LIKE INCLUDING GENERATED`** interacts badly with a source table
  that already had a column dropped (`gtest28a`/`gtest28b`) — likely a
  second symptom of the same cross-statement cascade as (c)/(d) rather than
  an independent root cause; not investigated in isolation.
- **(f) misc, not root-caused**: `permission denied for table gtest12`/
  `gtest11` (GRANT/REVOKE on a generated-column table), `statistics object
  "gtest31_2_stat" does not exist` (extended statistics DDL), `function
  gf1(integer) does not exist` (a domain/generated-column function
  interaction).

Resume points and upstream citations for each are in the ledger row itself.
Buckets (a) and (b) are each their own REFACTOR-tier subsystem addition
(a real `information_schema` view join, and a `validateGeneratedColumnExpr`
CREATE-TABLE hook covering 7 distinct checks); (c) needs whole-row Var
support in the CHECK-constraint evaluator (shared machinery with the
`num_nulls(gtest_err_2c)` case bucket in the same file); none fit in this
loop's budget alongside the three fixes already landed.

## Next case

Per `.ralph/fix_plan.md` M0134 ordering: **M0134-0188** (`xml.sql`, `not-tried`).
