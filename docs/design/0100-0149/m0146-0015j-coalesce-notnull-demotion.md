# M0146-0015j — `coalesce(...) IS [NOT] NULL` no longer demotes outer joins

| field | value |
|---|---|
| Status | landed 2026-09-27 |
| Kind | bug (wrong rows — outer join demoted past a non-strict qual) |
| Parent | none (M0146 family; sibling of M0146-0015g) |
| Movement | TPC-DS/TPC-H: none measured (sweep plans 99/99, arm 24/24) |

## Defect

```sql
select count(*)
from tk d full join tk e on e.u1 = d.u2 and e.h = 0
where coalesce(d.u1, e.u1) is not null;
```

goopg planned `Hash Join` and returned **10**; PostgreSQL 18.3 keeps
`Hash Full Join` and returns **190**. The WHERE term is strict on
neither input — a null-extended row still has the other side's columns,
so `coalesce` can be non-NULL with either input NULL. goopg's
null-rejection analysis collected every ColumnRef under the
`IS NOT NULL` operand and marked both tables non-nullable, demoting
the join and dropping the surviving side's rows.

## Root cause

`collectNonNullableWalk`'s `IsNullExpr` arm
(internal/optimizer/reduce_outer_joins.go) ran
`collectColumnRefTableNames` over the operand — every column ref it
contains, whatever wraps it.

PG's `find_nonnullable_vars_walker`
(postgres/src/backend/optimizer/util/clauses.c:1714) walks the
NullTest operand at `top_level=false` and descends ONLY into
strict-preserving constructs: `Var`, strict `FuncExpr`/`OpExpr`
(`func_strict`/`proisstrict`), `ScalarArrayOpExpr`, BoolExpr AND/OR
(intersected), NOT, and transparent wrappers (RelabelType, CoerceViaIO,
ArrayCoerceExpr, ConvertRowtypeExpr, CollateExpr). `CoalesceExpr`,
`CaseExpr`, `MinMaxExpr`, `NullIfExpr` have **no walker case** — they
contribute nothing.

The `IS NULL` twin had the same over-collection:
`find_forced_null_var` (clauses.c:1978) accepts only a plain `Var`
operand, while goopg's `collectForcedNullWalk` collected all refs of a
compound operand — `coalesce(d.u1, e.u1) IS NULL` marked both sides
forcing, which could drive a wrong LEFT→ANTI conversion.

## Fix

One new walker, `strictOperandRefs`, returns the operand's column refs
that must be non-NULL for the operand to be non-NULL:

- `ColumnRef` → itself;
- `BinaryOp` → recurse both sides iff `isStrictOp` (existing
  comparison fast-path + proisstrict resolution);
- `UnaryOp` / `CastExpr` / `CollateExpr` → recurse the operand
  (unary `-`/`+`/`NOT`, casts, collations are strict in their input);
- `RowExpr` → recurse every element (`ROW(...) IS NOT NULL` requires
  all elements non-NULL);
- `FuncCall` → recurse args iff
  `catalog.LookupBuiltinProcByProname` + `IsStrictProc` say strict.
  The proname lookup has no entries for COALESCE/NULLIF/GREATEST/LEAST
  (they are parse-tree special forms, not pg_proc functions — matching
  PG where they are dedicated node types with no walker case), so they
  conservatively contribute nothing; UDFs likewise stay conservative.

Applied at both `IS NOT NULL` arms (table-name and column-key
walkers). The `IS NULL` arms now collect only when the operand is a
plain `ColumnRef`, matching `find_forced_null_var`'s Var-only rule.

## Verification

- Live probes (`analysis/m0146/m0146-0015j/`): filed shape 190=PG,
  join types match on every arm (Full kept, `coalesce(e.u1,0)` keeps
  Left, `abs(e.u1)` still demotes, `e.u1 IS NULL` still converts to
  Anti). Remaining EXPLAIN diffs are join-method choice (Merge vs
  Hash) and `COALESCE` render casing — costing/cosmetic.
- Unit pins in `internal/optimizer/reduce_outer_joins_test.go`:
  Full kept, Left kept under const-arg coalesce, strict-func demotion
  preserved, coalesce `IS NULL` no longer forces ANTI.
- Gates: units PASS; tpch-spotcheck PASS (Q12=2, Q13=33); acceptance
  arm 24/24 MATCH vs baseline digests; TPC-DS SF0.25 sweep 96 PASS /
  0 MISMATCH, plans 99/99 same; tpcds-fireset PASS (no fires, both
  corpora).

## Residuals

- Operand-level AND/OR intersection (PG intersects per-arm nonnullable
  sets for BoolExpr operands below top level) is not modelled —
  `f(x) IS NOT NULL`-style operands containing a BoolExpr contribute
  nothing, i.e. conservative under-marking: plan-shape divergence at
  most, never wrong demotion.
- `var IS UNKNOWN` (PG's BooleanTest IS_UNKNOWN forced-null arm) is
  not collected — same conservative direction.
- Schema-qualified or user-defined strict functions under
  `IS NOT NULL` miss the proname lookup → under-marking (safe).
