# BETWEEN Operator (Milestone 0003)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-29                                             |
| Milestone  | 0003 — HammerDB TPC-H Workload                         |
| Refines    | [0003-0001-planner-overview.md](0003-0001-planner-overview.md) |
| Supersedes | —                                                      |

## Problem

TPC-H Q6 — the simplest revenue query — uses
`l_discount BETWEEN :1 - 0.01 AND :1 + 0.01`. Q14 / Q19 also use
BETWEEN over numeric and date ranges. Without it, those queries
parse-error before any executor work runs.

## Decisions

### Parser-side desugar

`expr [NOT] BETWEEN low AND high` rewrites at parse time:

- `expr BETWEEN low AND high` → `(expr >= low) AND (expr <= high)`
- `expr NOT BETWEEN low AND high` → `NOT ((expr >= low) AND (expr <= high))`

The result is a tree of existing `BinaryOp` (`>=`, `<=`, `AND`)
and `UnaryOp` (`NOT`) nodes — no analyzer / planner / executor
changes are needed. SQL three-valued logic flows through the same
Kleene helpers (`evalAnd`, `evalNot`).

### Precedence handling

`low` and `high` operands parse at `precAnd + 1` so the literal
`AND` keyword that separates them in `BETWEEN low AND high` isn't
consumed as a boolean conjunction inside `low`. That keeps
`x BETWEEN 1 AND 10 AND y > 5` parsing as
`(x BETWEEN 1 AND 10) AND (y > 5)` — top-level AND with the
BETWEEN tree as its left child — matching upstream's gram.y.

### Date codec coverage

Surfaced during BETWEEN smoke testing: encoding `KindTime` into a
column declared `date` failed with `kind 5 cannot encode as date`.
The codec's `encodeValue` and `decodeValue` switches added `date`
alongside `timestamp`/`timestamptz`, sharing the 8-byte
nanos-since-epoch on-disk shape. The midnight-UTC coercion happens
at literal-parse time (`date '...'` → `KindTime` at midnight), so
arithmetic and comparison flow through the same code paths as
timestamps. The wire-side Format() still emits the timestamp
shape (`YYYY-MM-DD HH:MM:SS.000000`) for date columns; a dedicated
KindDate carrier with a date-only formatter is deferred to the
type-system milestone.

## Verification

End-to-end against `goopg start -D <dir>` with upstream psql 18.3:

```sql
CREATE TABLE lineitem (
  l_orderkey int4, l_quantity int4,
  l_discount numeric, l_shipdate date);
INSERT INTO lineitem VALUES
  (1, 10, 0.05, date '1995-01-15'),
  (2, 25, 0.06, date '1995-06-20'),
  (3, 50, 0.04, date '1996-03-10'),
  (4, 5,  0.07, date '1994-12-01');

-- Q6 numeric BETWEEN
SELECT l_orderkey FROM lineitem
WHERE l_discount BETWEEN 0.05 AND 0.07;
-- 1, 2, 4

-- INT BETWEEN
SELECT l_orderkey FROM lineitem
WHERE l_quantity BETWEEN 10 AND 30;
-- 1, 2

-- NOT BETWEEN
SELECT l_orderkey FROM lineitem
WHERE l_quantity NOT BETWEEN 10 AND 30;
-- 3, 4

-- Date BETWEEN
SELECT l_orderkey FROM lineitem
WHERE l_shipdate BETWEEN date '1995-01-01' AND date '1995-12-31';
-- 1, 2

-- Combined with another conjunct (precedence sanity)
SELECT l_orderkey FROM lineitem
WHERE l_quantity BETWEEN 1 AND 100 AND l_discount > 0.05;
-- 2, 4
```

`TestParseBetweenDesugar`, `TestParseNotBetweenDesugar`, and
`TestParseBetweenWithTrailingAnd` pin the AST shape and the
critical precedence-handling case.

## Out of scope (deferred)

- A dedicated `BetweenExpr` node. Desugaring at parse time is
  cheaper and re-uses every downstream operator path; the AST
  shape stays standard-comparison.
- KindDate carrier with date-only Format() — see "Date codec
  coverage" above.

## Follow-up: `BETWEEN SYMMETRIC` / `ASYMMETRIC` (2026-07-04, M0122-0004)

Closed the gap noted above. `SYMMETRIC`/`ASYMMETRIC` are registered as
reserved keywords (`internal/parser/token.go`, category `KwCatReserved`
in `internal/parser/keywords.go`, matching upstream's kwlist.h
`RESERVED_KEYWORD` classification for both).

`p.acceptBetweenOrdering()` (`internal/parser/select.go`) consumes the
optional keyword right after `[NOT] BETWEEN`. `ASYMMETRIC` is a no-op
(it's the existing default ordering); `SYMMETRIC` flows a bool into
`parseBetweenTail`, which now desugars

- `expr BETWEEN SYMMETRIC low AND high` →
  `(expr>=low AND expr<=high) OR (expr>=high AND expr<=low)`

still entirely inside the parser — no `BetweenExpr` node, no
analyzer/planner/executor changes, same as the plain-BETWEEN desugar.
Tests: `TestParseBetweenSymmetricDesugar`,
`TestParseNotBetweenSymmetricDesugar`,
`TestParseBetweenAsymmetricDesugar` (`internal/parser/between_test.go`).
