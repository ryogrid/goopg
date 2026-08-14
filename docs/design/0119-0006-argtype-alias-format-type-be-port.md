---
status: accepted
date: 2026-08-14
supersedes: none
milestone: M0119-0006 (74th slice)
---

# regprocedure arglist's ArgTypeDisplayAlias becomes a faithful format_type_be port

Closes deferral rows 1345 + 1346 (the 73rd slice's carry-forwards). The
regprocedure arglist's alias table (`catalog.ArgTypeDisplayAlias`, promoted
from `pgArgTypeDisplayAlias` in the 73rd slice) renders every base type through
a bare string-switch that is NOT a complete port of `format_type_be`'s
special-case switch (format_type.c), and the catalog-side bare arglist builder
(`formatProcedureArglist`) does not split a baked-in `[]` array suffix before
aliasing — so the two sibling renderers diverge on a builtin array arg.

## 1. Problem (measured against a throwaway PG 18.3 oracle, port 5533)

Probe: `CREATE FUNCTION` battery in `public`, then `oid::regprocedure`:

| probe (default `search_path`) | goopg today | PG 18.3 | row |
|---|---|---|---|
| `CREATE FUNCTION f_varbit(varbit)` | `f_varbit(varbit)` | `f_varbit(bit varying)` | 1346 |
| `CREATE FUNCTION f_char("char")` | `f_char(char)` | `f_char("char")` | 1346 |
| `CREATE FUNCTION f_chararr("char"[])` | `f_chararr(char[])` | `f_chararr("char"[])` | 1346 |
| `CREATE FUNCTION f_intarr(int[])` | `f_intarr(integer[])` | `f_intarr(integer[])` | already right (executor) |
| catalog bare `RegprocedureName` of `int[]` arg | `f(int[])` | `f(integer[])` | 1345 |

`varbit` (VARBITOID) is a real `format_type_be` special-case the alias table
lacks; `char` (CHAROID) is a reserved keyword rendered through the default path
as `"char"`; and the catalog bare builder aliases the WHOLE stored name so an
array suffix defeats the alias (`int[]` → no switch match → `int[]`).

## 2. The upstream rule

`format_type_be` (format_type.c:343) = `format_type_extended(oid, -1, 0)`:

1. A builtin OID in the special-case switch → the bare SQL alias (`boolean`,
   `integer`, `bigint`, `real`, `double precision`, `character`,
   `character varying`, `bit varying`, `timestamp without time zone`, …).
2. Any other type → `nspname = NULL` when `TypeIsVisible`, else the namespace
   name; then `quote_qualified_identifier(nspname, typname)` — which
   **double-quotes the name if it is not a standard identifier or matches any
   lexer keyword**. `char` is such a keyword: `quote_identifier("char")` →
   `"char"`. (`bpchar`'s `character` is the switch case, NOT this path — a
   `character` output never gets quoted.)
3. A true array appends `[]` AFTER the (possibly quoted) element name — so
   `char[]` renders `"char"[]`, never `"char[]"`.

The only missing switch case in `ArgTypeDisplayAlias` is `varbit → bit
varying` (verified against the full switch: `bit`, `interval`, `json`,
`numeric` are identities; every other case is already present). The only
builtin that reaches the default keyword-quoting path is `char`.

## 3. Implementation

### 3.1 `catalog.ArgTypeDisplayAlias` (internal/catalog/catalog.go)

Add two cases (input names are goopg's internal typname spellings, matched
case-folded):

- `case "varbit": return "bit varying"` — the missing VARBITOID switch case.
- `case "char": return "\"char\""` — the default-path keyword-quoting
  (format_type.c's `quote_identifier("char")`); `char` is the single-byte type
  OID 18, distinct from `bpchar` (1042), which keeps mapping to `character`.

Both new arms are consumed identically by the two sibling renderers because
both call the shared `ArgTypeDisplayAlias` (the executor's `regprocedureArglist`
and the catalog bare `formatProcedureArglist`).

### 3.2 `catalog.formatProcedureArglist` (internal/catalog/catalog.go)

The bare builder currently passes `a.Name` (which carries the baked-in `[]`
array suffix, exactly as `Routine.ArgTypes[i].Name` stores it) straight to
`ArgTypeDisplayAlias`, so `int[]` finds no switch case. Split the suffix, alias
the ELEMENT, re-append — the same shape the executor's `regprocedureArglist`
already implements (`splitArraySuffix` in reg_identifier.go), so `int[]` →
`integer[]`, `char[]` → `"char"[]`. A small package-local helper (catalog cannot
import executor) mirrors `splitArraySuffix`.

### 3.3 Executor `regprocedureArglist` — NO change

It already splits the suffix and routes the element through
`catalog.ArgTypeDisplayAlias`, so the new `varbit`/`char` arms apply
automatically and the two renderers stay byte-identical (Hard-won Rule #2).

## 4. Measured result (after fix, same probe)

`f_varbit(bit varying)`, `f_char("char")`, `f_chararr("char"[])`,
`f_intarr(integer[])`, catalog `RegprocedureName` of an `int[]` arg
`f(integer[])` — all four now match PG 18.3.

## 5. New deferrals (measured this slice, out of scope)

- **Multi-word type names in CREATE FUNCTION arg capture**: `bit varying` /
  `character varying` / `double precision` store the LAST word (`varying`,
  `precision`) via the parser's collapsed ColumnType.Name; `timestamp with time
  zone` is a syntax error in CREATE FUNCTION args. Separately, the arglist
  carries only `Name` (dropping Args/OID), so a bare-`char` arg — the parser
  stamps it as bpchar-like, `Args=[1]`, matching PG's parser — is
  indistinguishable from OID-18 `"char"` and renders `"char"` where PG renders
  `character`. Parser/type-capture resolution gaps, separate from the renderer.
  (Ledger row 1349.)
- **regproc/regclass → text/varchar/name cast on a STRING-LITERAL source
  renders the raw OID**: `'f_varbit'::regproc::text` → `131072` (PG: `f_varbit`),
  `'pg_type'::regclass::text` → `1247` (PG: `pg_type`); regtype/regrole/
  regcollation and non-literal sources (a column / `oid::regproc`) are
  unaffected. (Ledger row 1350.)

## 6. Tests

- `TestArgTypeDisplayAliasFormatTypeBePort` (catalog) — pins `varbit` and `char`
  in the alias table.
- `TestRegprocedureName` (catalog, extended) — a user routine with an `int[]`
  arg renders `my_arr(integer[])` (row 1345); a `varbit`/`char` arg renders the
  new aliases.
- `TestRegOutRegprocedureArgTypesVarbitChar` (executor, reg_qualify_test.go) —
  the SELECT wire renderer emits `f_varbit(bit varying)`, `f_char("char")`,
  `f_chararr("char"[])`.
- `TestRegprocedureArglistCatalogAndExecutorAgree` (executor) — the catalog bare
  builder and the executor renderer produce identical arglists for the same
  `[]RegprocArg` (Hard-won Rule #2 sibling pin).

## 7. Gates

Package suites (internal/catalog, internal/executor, internal/server), pre-commit
units, `TestPort_RegressSuite`, `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35).
