# Index-expression result-type resolution, and expression key columns in amcheck's opclass comparator (M0119-0006)

status: accepted
date: 2026-08-10
task: M0119-0006 (pg_amcheck server tier), ninth slice
related: [0119-0006a opclass comparator dispatch](0119-0006-opclass-comparator-dispatch-amcheck.md),
[0119-0006c expression key columns in the bulk build](0119-0006-expression-index-bulk-build.md)

## Problem

An *expression* index key column (`CREATE INDEX ON t ((lower(a)))`) is
represented in goopg by `catalog.Index.Columns[i] == ""` plus the parsed AST in
`ColExprs[i]`. There is no catalog column behind it, so every consumer that
needs the key's *type* had to decline expression columns wholesale:

- `btIndexOpClassComparator` (amcheck's B-tree verifier) returned `nil` — i.e.
  fell back to whole-key byte order — for **the entire index** as soon as any
  key column was an expression. A `pg_amproc` row repointed at a descending
  comparator therefore went unreported on such an index: exactly the corruption
  `005_opclass_damage.pl` exists to catch, invisible.
- `encodeArbiterExprKey` (the shared expression-key encoder) dispatches on the
  runtime Datum *kind* because no static type was available, an assumption the
  eighth slice could only document, not check.

The blocker in both cases is the same missing primitive: what type does this
index expression produce?

## What landed

### 1. `planner.ExprResultType(Expr) (catalog.Type, bool)`

`internal/planner/expr_result_type.go`. Resolves the static result type of a
*resolved* planner Expr — PG's `exprType()` (`nodes/nodeFuncs.c`).

The faithfulness decision that matters: function calls and operators are **not**
resolved from a hand-written name table but from the PG 18 catalog seed goopg
already ships. `catalog.LookupProcForNode(name, argOIDs)` → `ProcResultType` is
pg_proc's `prorettype`; `catalog.LookupOperatorForNode(spelling, l, r)` →
`OperatorEntry.ResultType` is pg_operator's `oprresult`. A resolution therefore
agrees with what a real backend's parse analysis records for the same overload —
including overloads goopg's own evaluator implements loosely. Where the planner's
`BinaryOp.ResultType` annotation disagrees with the catalog (it records the width
goopg's evaluator computes in — `int4 + int4` is annotated `int8`), the catalog
wins and the annotation is the fallback.

The other decision: **it declines rather than guesses.** `catalog.TypeNameToOID`
falls back to `text` (25) for every name it does not know — user-defined enums,
domains, composite types — so `exactTypeOID` rejects anything that lands on text
without literally spelling `text`. A key decoder handed a confidently wrong type
misreads key bytes; a decline just costs the caller its optimization.

This is deliberately NOT a strengthening of the existing unexported
`inferExprType`, which answers "what type should `lag()`/`lead()` report" and
defaults to text for everything unrecognized. The two answer different
questions and a text default is precisely what a key decoder must not receive,
so they must not be merged.

### 2. `exprKeyDecodeType` — the decode-side twin of `encodeArbiterExprKey`

`internal/executor/operators_upsert.go`, placed immediately below the encoder it
mirrors (Hard-won Rule #2: sibling paths move together).

The SQL result type is *not* what the key bytes were written under. The encoder
dispatches on Datum kind, so:

| expression SQL type | encoder arm | decode surrogate | routine-safe |
|---|---|---|---|
| int2 / int4 / int8 / oid | `EncodeInt8` (always 8 bytes) | `int8` | yes |
| bool | `EncodeInt8(0/1)` | `int8` | **no** (decodes as integer) |
| numeric | `EncodeNumericKey` | `numeric` | yes |
| date / time / timestamp(tz) | `EncodeTimestamp` (int64 micros) | `timestamp` | yes |
| text / varchar / char / name / uuid | `EncodeVarchar` | `text` | yes |
| bytea | `EncodeVarchar` | `text` | **no** (decodes as string) |
| float4 / float8 | *(no arm — row not indexed)* | — declines | — |
| enum | `EncodeFloat8(sort order)` | — declines (needs the enum catalog to invert) | — |

An int4-typed expression whose key is 8 bytes decoded as a 4-byte int4 column
would leave the composite walk mid-key and make a healthy index report
`item order invariant violated` — a false positive, the worst outcome for a
corruption checker. `allowRoutine=false` covers the narrower hazard: the
surrogate decodes correctly but to a Datum of a different kind than a user
comparator declared over the SQL type expects, so those columns still decode
(keeping the walk aligned) while ordering by bytes, which for both encodings is
the type's own order anyway.

### 3. The comparator's expression arm

`btIndexOpClassComparator` no longer bails on `name == ""`. It calls
`exprKeyColumnType(idx, i)`, which resolves the expression through the *same*
`planner.ResolveIndexPredicate` the build path uses (so the comparator can never
disagree with what was indexed), types it with `ExprResultType`, and maps it
with `exprKeyDecodeType`. A decline anywhere returns `nil` — the previous
behaviour, byte order — so this slice can only add detection, never remove it.

## Gates

- `internal/executor`: `TestBtIndexCheck_OpClassDamageDetectedExpression` — the
  005 shape on `((lower(t)) text_fickle_expr_ops)`: clean, then repoint
  `x_asc_cmp` → `x_desc_cmp`, then `item order invariant violated for index
  "fickleexpr"`.
- `TestBtIndexCheck_ExpressionKeyCleanUnderPlainOpClass` — a composite
  `((a * 2), i int4_fickle_expr_ops)`. This is the width gate: the integer
  expression column is 8 encoded bytes, the plain int4 column 4, and a wrong
  surrogate makes the *healthy* index report corrupt. Both halves asserted.
- Both confirmed **non-vacuous**: forcing `exprKeyColumnType` to decline makes
  the damaged indexes report clean and both tests fail.
- `TestExprKeyDecodeTypeRoundTrip` pins encoder↔surrogate byte-for-byte with a
  trailing sentinel (the decoder must consume exactly the encoded prefix),
  `TestExprKeyDecodeTypeDeclinesUninvertible` and
  `TestExprKeyDecodeTypeRoutineSafety` pin the deliberate declines.
- `internal/planner`: `TestExprResultTypeResolves` (23 expressions through the
  production `ResolveIndexPredicate` path), `TestExprResultTypeDeclinesUnknown`
  (the enum-argument text fallback must NOT be trusted),
  `TestExprResultTypeEnumColumnPassesThrough`.

## Deferred

- `encodeArbiterExprKey` still has no float4/float8 arm, so a float-valued index
  expression indexes no rows at all; and an enum-valued one encodes but cannot
  be inverted. Both now decline explicitly instead of silently mis-decoding.
- `ExprResultType` resolves a `CASE` to its first resolvable branch rather than
  PG's `select_common_type` over all branches.
- The resolved type is computed on demand, not stored on `catalog.Index`. There
  is no durability difference — `ColExprs` itself is in-memory only — and one
  fewer field to keep in sync; if a hot path ever needs it, memoize there.

See `.ralph/deferral_ledger.md` for the resume points.
