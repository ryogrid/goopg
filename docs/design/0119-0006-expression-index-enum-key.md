# 0119-0006f — Enum expression index keys: type-directed encoding

**Status:** accepted
**Milestone:** M0119-0006 (pg_amcheck server tier / expression-key encoding)
**Date:** 2026-08-10
**Predecessors:** `0119-0006-expression-index-bulk-build.md` (7th slice),
`0119-0006-expression-index-result-type.md` (9th slice),
`0119-0006-expression-index-float-key.md` (10th slice — the same defect shape for
float, and the slice whose "still deferred" list named this one)

## Problem

`encodeArbiterExprKey` is the single encoder behind all three expression-key
paths: the ON CONFLICT arbiter probe (`encodeArbiterKey`), the runtime
index-maintain path (`encodeExprIndexKey`) and the CREATE INDEX / REINDEX bulk
build (`encodeCompositeBTreeKeyWithExprs`). Since the 7th slice it dispatches on
the runtime Datum *kind*, because an expression key column has no catalog column
to consult (`catalog.Index.Columns[i] == ""`).

Kind dispatch is sound only where the kind determines the ordering the type
requires. Enum is the second place — after float — where it does not, and here
the failure is *not* a mixed-encoding accident that shows up only for exotic
values: it is wrong for **every** enum expression index.

An enum COLUMN key is `EncodeFloat8(enumsortorder)`. That is not an
implementation convenience — it is the type's ordering. Upstream `enum_ops`
compares by `enumsortorder` (`enum_cmp` → `enum_cmp_internal`,
`src/backend/utils/adt/enum.c`), i.e. the order the labels were DECLARED in, and
goopg reproduces it by converting a `KindString` label into `KindEnum` (which
carries the sort order) *before* calling `encodeBTreeKeyForColumn`. Every column
path does that conversion explicitly — `collectBTreeEntries`
(`operators_ddl.go`), the unique-probe path (`operators_storage.go`), the
index-scan probe (`operators_index.go`) — keyed on the column's catalog type
(M0097-0022).

An expression key column has no catalog column, so that conversion never ran.
The raw label reached the `KindString` arm and was written with
`EncodeVarchar`. Dumped from a real build of
`CREATE INDEX ON t ((CASE WHEN a > 0 THEN m ELSE m END))` over
`CREATE TYPE mood AS ENUM ('sad','ok','happy')`:

```
686170707900   "happy\0"   6 bytes
6f6b00         "ok\0"      3 bytes
73616400       "sad\0"     4 bytes
```

Two defects in one:

1. **Wrong order.** The index is in ALPHABETICAL label order
   (`happy < ok < sad`) where the type's order is the declared one
   (`sad < ok < happy`) — here the exact reverse. An ordered read of the index
   disagrees with the type it indexes, and amcheck's item-order check under
   `enum_ops` would call a physically healthy index corrupt.
2. **Mixed encodings, latent.** A datum that *did* arrive as `KindEnum` — the
   seq-scan path injects those for enum columns (`operators_storage.go`) —
   takes the `KindEnum` arm and writes 8 float bytes into the same index as the
   variable-width label bytes. That is the float bug's shape: two byte spaces
   that do not interleave, plus a per-row-varying width that desynchronizes the
   composite key walk.

## Fix

Type-directed, exactly as the float arm: when the key expression's static result
type names a user enum, every row goes through `EncodeFloat8(enumsortorder)`,
whatever kind its datum arrived as.

- `encodeArbiterExprKey` takes a `*Context` (new first parameter) so it can
  reach the catalog. All three call sites already had one.
- `exprKeyEnumType(ctx, keyExpr)` resolves `planner.ExprResultType` and asks
  `catalog.InMemory.LookupEnum` whether that type name is an enum. The planner
  already returns a user enum's type NAME for the shapes an enum key expression
  takes (a `ColumnRef` of enum type, a CAST to the enum, a CASE over them); it
  is the catalog, not the planner, that decides enum-ness.
- `enumSortOrderForKey(et, v)` maps the datum to a sort order: `KindEnum`
  carries it; a `KindString` label is looked up in the catalog entry — the same
  label→order step every column path performs.
- Resolution failure means "not known to be an enum" and leaves kind dispatch in
  charge, so nothing an earlier goopg indexed stops being indexed. A value that
  is not a label of the enum returns `nil` (row not indexable), consistent with
  every other kind the encoder has no arm for.

`exprKeyDecodeType` is unchanged and still declines enums (`ok=false` → the
opclass comparator declines the whole index): `EncodeFloat8(sort order)` cannot
be inverted back to a label without the enum catalog entry, and the surrogate
table's contract is a *decode*, not a re-resolution. Its doc comment already
described the enum key as `EncodeFloat8(sort order)`; that description is now
true rather than aspirational.

## Faithfulness

`enum_cmp_internal` orders by `enumsortorder`, so an ordered walk of the index
now reproduces declaration order — for the fixture above, `sad, ok, happy`. The
encoder used is the one the enum COLUMN path already uses, so a column key and
an expression key over the same enum are byte-identical (Hard-won Rule #2:
sibling paths move together).

## Gates

- `TestEncodeArbiterExprKeyEnumIsTypeDirected` — 8-byte keys in declaration
  order; `KindString` label and `KindEnum` datum produce IDENTICAL bytes; a
  non-label returns nil; built-in non-vacuity assertion (with no catalog the
  encoder still produces the old label bytes).
- `TestExpressionIndexBuildEnumKey` — end-to-end over the bulk build AND the
  post-build INSERT maintain path, scanning the physical tree and asserting the
  decoded sort orders equal the enum's, with `'ok'` landing BETWEEN the two
  built rows (under the old label encoding it sorted last).

Both confirmed non-vacuous by disabling the new arm: the unit test reports
`"sad" encoded to 4 bytes` and the E2E test reports 6/4-byte label keys.

The labels are deliberately chosen so declaration order and alphabetical order
are exact reverses, so no assertion here can pass under label order.

## Still open for M0119-0006

Posting-list duplicate coverage in the checkunique tier;
`box`/`int4range`/`int4[]`/`interval` key encodings (no encoder arm at all); the
whole-database (unscoped) pg_amcheck run. See the deferral ledger rows dated
2026-08-10.
