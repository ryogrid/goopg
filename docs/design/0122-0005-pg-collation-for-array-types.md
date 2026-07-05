# `pg_collation_for` array-type support (M0122-0005 follow-up)

Status: accepted
Date: 2026-07-06
Supersedes: none (follow-up to the `pg_collation_for` fold, 2026-07-05
deferral-ledger row / M0122-0005)

## Problem

`internal/planner/planner.go`'s `foldPgCollationFor` folds
`pg_collation_for(expr)` at plan time to PostgreSQL's real answer. The
2026-07-05 landing left gap (3) explicitly deferred: array types (e.g.
`text[]`) were not recognized as collatable, so
`pg_collation_for('{a,b}'::text[])` raised `42804` instead of PG's real
answer (`"default"`).

Verified against a scratch upstream PostgreSQL 18.3 instance
(`postgres/src/backend/utils/adt/misc.c`'s `pg_collation_for` /
`type_is_collatable`, which follows `pg_type.typcollation` — an array type's
`typcollation` is derived from its element type at `CREATE TYPE` time, not
left invalid):

```sql
SELECT pg_collation_for('{a,b}'::text[]);        -- "default"
SELECT pg_collation_for(ARRAY['a','b']::name[]); -- "C"
SELECT pg_collation_for(ARRAY[1,2]);             -- ERROR 42804 (int4[] not collatable)
```

## Design

goopg represents an array type two different ways depending on the code
path (see `catalog.Type.IsArray`'s doc comment): a real table column's
`catalog.Type` keeps `Name` as the bare element name with `IsArray: true`,
while a cast-expression target type (`internal/parser/select.go:2452`,
mirrored by several `planner.go` sites building `catalog.Type{Name: et.Name +
"[]"}`) instead appends a literal `"[]"` suffix directly onto `Name`.

`foldPgCollationFor`'s `exprType(arg).Name` lookup already produces the
correct *unsuffixed* element name for the `IsArray`-flagged representation
(no change needed there — `text[]` column arrays already matched the `text`
case before this change). The gap was specific to the suffixed-name
representation reached via an explicit `::type[]` cast, which fell straight
through every case in the base-type switch into the `42804` error branch.

Fix: strip a trailing `"[]"` suffix (looped, matching the existing
`castTargetLabel`/exprType-array-stripping precedent used elsewhere in
`planner.go`, e.g. line ~9150) from `baseName` before the collatable-type
switch, so an array's element type — not the literal `"elemtype[]"` string —
decides collatability. This is applied after the domain-over-base-type
substitution and before the switch, so `array-of-domain` isn't specifically
handled (out of scope; no test coverage for it either upstream or here).

## Verification

`internal/planner/pg_collation_for_test.go`'s `TestPgCollationForFolds`
gained three cases, all cross-checked against the scratch PostgreSQL 18.3
instance above:

- `'{a,b}'::text[]` → `"default"`
- `ARRAY['a','b']::name[]` → `"C"`
- `'{1,2}'::int4[]` → `42804`

Gates: `go build ./...` clean; `go test ./internal/planner/...
./internal/executor/...` PASS (no regressions); `scripts/tpch-spotcheck.sh`
pending in this loop.

## Deferred

Unchanged from the 2026-07-05 row's remaining gaps (1) and (2) — a real
per-expression collation-derivation pass (`parse_collate.c`'s
`assign_expr_collations`, needed for a column's declared `COLLATE` to survive
into `ColumnRef` without being restated inline) and the
`resolveExprAfterAggregate` fold gap — both out of scope for this bounded
follow-up.
