# M0097-0077 — ALTER TABLE ADD COLUMN … DEFAULT fast default (attmissingval)

Date: 2026-05-29
Status: Landed
Scope: `internal/catalog/catalog.go`, `internal/executor/codec.go`,
       `internal/executor/operators_ddl.go`
Related: M0097-0076 (DELETE … USING — same `returning` regress test, prior
         in-order blocker), M0106-0011 (relcache-inval on ADD COLUMN),
         M0097-0048 (inheritance children registration)

## Problem

The next in-order divergence on the `returning` regress test after
[[0097-0076]] is `ALTER TABLE … ADD COLUMN … DEFAULT <const>` backfill:

```sql
ALTER TABLE foo ADD COLUMN f4 int8 DEFAULT 99;
SELECT * FROM foo;   -- existing rows must show f4 = 99, not blank
```

`execAlterTableAddColumn` added the column to the catalog but recorded no
default, and the heap decoder (`DecodeRowIntoMctxPGTuple`) emitted
`NullDatum` for every column beyond the row's stored `natts`. Rows written
before the ALTER therefore surfaced NULL for the new column instead of the
declared DEFAULT — diverging from PostgreSQL, which records the constant
default as `pg_attribute.attmissingval` and fills it in at decode time
("fast default", no table rewrite — commit 16828d5c0273 in PG 11).

## Fix

Mirror PG's `attmissingval` "fast default" rather than rewriting the heap:

1. **Catalog** (`internal/catalog/catalog.go`): `Column` gains
   `MissingValue any`. It holds an `executor.Datum`, typed as `any` to
   avoid a catalog → executor import cycle (catalog is the lower layer).
   `nil` means "decode trailing missing columns as NULL" — the prior
   behaviour, preserved for plain ADD COLUMN and non-constant defaults.

2. **DDL** (`internal/executor/operators_ddl.go`):
   `execAlterTableAddColumn` evaluates the column's `DefaultExpr` once at
   ALTER time via the new `constDefaultDatum(expr, type)` and stores the
   resulting Datum on `Column.MissingValue` (only when the expression is a
   supported constant and not NULL — a NULL default needs no missing value
   since the decoder's fallback already yields NULL).

   `constDefaultDatum` handles the literal shapes regress / TPC-H actually
   exercise: integer / numeric / string / boolean / NULL literals, plus
   unary `-` over numeric literals (so `DEFAULT -1` survives), coerced to
   the column's declared type. Scientific notation and non-constant
   expressions return `(_, false)`, leaving `MissingValue` nil so the
   decoder falls back to NULL (correctness over completeness — a missing
   fast-default just costs a NULL, never a wrong value).

3. **Decoder** (`internal/executor/codec.go`):
   `DecodeRowIntoMctxPGTuple`'s `i >= storedNatts` branch now surfaces
   `c.MissingValue.(Datum)` when present, else `NullDatum`. This is the
   single read site that turns the stored attmissingval into row data.

### Inheritance recursion (bundled)

ADD COLUMN must propagate to inheritance/partition children
(PG's `ATAddColumn` recursion). `addColumnRecursive(tbl, col, …, isRoot)`
applies `Catalog.AddColumn` to `tbl` then recurses over
`InMemory.InheritanceChildren(tbl.OID)`:

- A duplicate-column ("already exists") error on the **root** (the named
  relation, `isRoot == true`) is a genuine user error → `42701`
  duplicate_column, matching PG and preserving prior behaviour.
- The same error on a **recursed child** is the column-merge case PG
  accepts silently: the child keeps its existing same-named column and the
  ADD is a no-op there.

The `isRoot` flag is load-bearing: an earlier draft tried to distinguish
root from child by `tbl.Name == act.Column.Name` (table name vs column
name — never matches), which silently swallowed the root-table 42701.
`TestDDLAlterTableAddColumnDuplicateErrors` guards against that regression.

## Verification

- `go build ./...` clean, `go vet ./internal/executor/` clean.
- `go test ./internal/executor/`:
  - `TestDecodeRowIntoMctxPGTupleUsesMissingValueForFastDefault` /
    `…NoMissingValueDecodesNullForTrailing` — decoder branch both ways.
  - `TestConstDefaultDatumLiteralCases` — literal evaluator across
    int/negative-int/string/bool/null/decimal shapes.
  - `TestDDLAlterTableAddColumnDefaultBackfillsExistingRows` — e2e: a row
    written pre-ALTER scans back the DEFAULT 99, and the column carries the
    expected `MissingValue` Datum.
  - `TestDDLAlterTableAddColumnDuplicateErrors` — duplicate column on the
    named relation still returns 42701.
  - Full package passes modulo the pre-existing `TestToastByteaRoundTrip`
    failure (reproduced on clean baseline `20198ce0` with this change
    stashed — unrelated to this work).
- `go test ./internal/parser/ ./internal/analyzer/ ./internal/planner/
  ./internal/catalog/` all pass.

## Remaining `returning` blockers

This clears the third item on the [[0097-0076]] remaining-blocker list.
Next in-order divergences:

- INHERITS row propagation through UPDATE FROM / DELETE USING.
- RETURNING OLD / RETURNING NEW alias references.

## Notes / limitations

- Only constant defaults become fast defaults. Volatile / expression
  defaults (`now()`, `nextval(...)`) still decode missing rows as NULL;
  matching them would require an actual rewrite (deferred — PG also rewrites
  for volatile defaults).
- The decoder reads `MissingValue` on the lower-layer `catalog.Column`; the
  encode side is unaffected (newly-written rows store the value inline), so
  this is decode-only and does not touch the storage format. Sibling-path
  class [[pattern_sibling_paths_must_agree]] does not apply — there is no
  matching encode twin to keep in sync.
