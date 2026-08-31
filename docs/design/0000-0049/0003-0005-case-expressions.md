# CASE Expressions (Milestone 0003)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-28                                             |
| Milestone  | 0003 — HammerDB TPC-H Workload                         |
| Refines    | [root-0010-parser.md](../../root/root-0010-parser.md), [root-0011-planner.md](../../root/root-0011-planner.md), [root-0012-executor.md](../../root/root-0012-executor.md) |
| Supersedes | —                                                      |

## Problem

TPC-H queries use `CASE` heavily — Q1 packs five `CASE`
expressions into the SUM aggregates that compute returnflag /
linestatus group rollups, Q12 uses CASE for late-shipment
classification, Q14 uses CASE for promo-vs-non-promo revenue
fractions. Without CASE, none of those queries can run.

The parser had no `CASE` support at all (`KwCase` not even in
the keyword table) and would error at the leading `CASE` token.

## Upstream reference

- `postgres/src/backend/parser/gram.y` — `case_expr` /
  `case_arg` / `when_clause`.
- `postgres/src/backend/executor/execExpr.c` —
  `ExecInitCaseExpr` / `EEOP_CASE_TESTVAL`.

## Decisions

### Two forms, single AST node

Both upstream forms share one `parser.CaseExpr` carrying:

- `Operand` (nullable) — set for the simple form (`CASE expr
  WHEN val …`), nil for the searched form (`CASE WHEN cond …`).
- `Whens []CaseWhen` — at least one (mirrors upstream's
  grammar; bare `CASE END` is a syntax error).
- `Else` (nullable) — `ELSE` clause; absent → fallback yields
  NULL.

The planner mirrors with `planner.CaseExpr` /
`planner.CaseWhen`, recursively resolved through
`resolveCaseExpr`. Both `resolveExpr` and the after-aggregate
`resolveExprAfterAggregate` translation sites delegate to the
same helper.

### Type unification: same-or-compatible, else unknown

The analyzer's `analyzeCaseExpr` walks each WHEN/THEN/ELSE
branch and unifies the result types with `sameOrCompatible`:

- Same SQL type name → that type.
- Both numeric-like → arbitrary numeric (the type system
  isn't ready to pick the wider one; `unknown` falls out of
  the `isAssignable` checks).
- Both string-like → string.
- Otherwise → `unknown`.

Searched-form WHEN clauses must be boolean-like (errors
42804 otherwise). Simple-form WHEN clauses can be any type —
the executor's `compareEq` does the comparison and treats a
type mismatch as "not equal" rather than an error, matching
upstream's coercion-friendly stance.

### Executor: first match wins, NULL is "not matched"

`evalCaseExpr` walks Whens in order. The first match (boolean
true for searched form, equal-to-Operand for simple form)
returns the THEN; otherwise the ELSE; otherwise NULL. NULL
WHEN evaluates as "not matched" — never NULL-true. This
mirrors `postgres/src/backend/executor/execExpr.c`'s
`EEOP_CASE_WHEN_STEP`.

`compareEq` is the simple-form comparator. It handles the
type-pair combinations CASE branches typically mix:

- int/int, bool/bool, string/string, time/time → strict equality
- int/string and string/int → format-and-compare (lets
  HammerDB's `'1'` literal compare to a NUMERIC-stored
  integer)
- Anything else → false (no error)

NULL on either side → NULL (per upstream three-valued logic).

## Verification

End-to-end smoke against `goopg start -D <dir>` with upstream
psql 18.3:

```
CREATE TABLE lineitem (l_returnflag CHAR(1), …);
INSERT INTO lineitem VALUES ('A', …), ('R', …), ('N', …);

-- searched form
SELECT l_returnflag,
       CASE WHEN l_returnflag = 'A' THEN 'aged'
            WHEN l_returnflag = 'R' THEN 'returned'
            ELSE 'normal' END
FROM lineitem;
-- A → aged, R → returned, N → normal

-- simple form
SELECT CASE l_returnflag WHEN 'A' THEN 1 WHEN 'R' THEN 2 ELSE 0 END
FROM lineitem;

-- ELSE omitted → NULL fallback
SELECT CASE WHEN l_returnflag = 'A' THEN 'aged' END FROM lineitem;
-- A → aged, R → NULL, N → NULL
```

## Out of scope (deferred to subsequent loops)

- Real type unification across CASE branches (numeric width
  promotion, text/varchar coalescing). Waits on the type
  system milestone.
- COALESCE / NULLIF as SQL-standard sugar over CASE. They
  share the parser surface but parse to dedicated
  `CoalesceExpr` / `NullIfExpr` nodes upstream; v0 hasn't
  needed them yet.

## Cross-references

- TPC-H Q1 / Q12 / Q14 definitions:
  `HammerDB/scripts/tcl/postgres/tproch/`.
- Parser AST: [root-0010-parser.md](../../root/root-0010-parser.md).
- Executor evaluator: [root-0012-executor.md](../../root/root-0012-executor.md).
