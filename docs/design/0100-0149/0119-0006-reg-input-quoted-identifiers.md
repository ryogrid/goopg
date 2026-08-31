# M0119-0006 (72nd slice) — reg* INPUT honors double-quoted identifiers

Closes deferral ledger row 1341 (filed by the 71st slice).

## The gap

Every reg* name→OID **input** function (`regprocin`/`regprocedurein`/
`regclassin`/`regtypein`/`regrolein`/`regcollationin`) resolves a
double-quoted identifier the same way the SQL lexer does: a `"MyFunc"` keeps
its exact case and has its quotes stripped, an unquoted `myfunc` is downcased,
and a quoted segment may contain `.`/spaces. Upstream implements this once, in
`stringToQualifiedNameList` → `SplitIdentifierString`
(`postgres/src/backend/utils/adt/varlena.c:3581`), which every reg*in in
`regproc.c` feeds its raw text through before the catalog lookup.

goopg's reg* input paths instead ran the whole candidate through
`strings.ToLower` and then a *dumb first-dot* `splitQualifiedTable`, which:
- never strips the double quotes, so `'"MyFunc"'::regproc` reaches
  `Routines().LookupByName` with the literal name `"MyFunc"` (quotes and all)
  → `42883 function "\"MyFunc\"" does not exist`, where PG 18.3 resolves the
  routine;
- lowercases quoted segments (`'"MyFunc"'` → `"myfunc"`), mangling the case PG
  preserves;
- treats a `.` inside a quoted segment as a separator (`'"my.schema".fn`),
  where it is part of the identifier;
- false-flags a quoted role name containing `.` as "qualified" (regrolein's
  `strings.Contains(name, ".")`).

The OUTPUT side is already PG-faithful (69th/70th/71st slices quote and qualify
correctly); this slice is the input half of the same contract (Hard-won Rule #2
— the `::regproc` cast in expr.go and `regIdentifierInput` are sibling
renderers of `RegOut` and must agree).

## The fix

Port `SplitIdentifierString`'s per-segment rule into one shared parser used by
every reg* name→OID site:

```
splitRegQualifiedName(s) (schema, name string, ok bool)
```

- a `"…"` segment is unquoted with its case preserved; adjacent `""` collapses
  to a literal `"`;
- an unquoted segment is downcased;
- whitespace around segments is skipped;
- `.` separates segments but is not special inside a quoted segment;
- a syntax error (mismatched quotes, an empty unquoted segment, an empty
  string) returns `ok=false` → the caller raises `42602 invalid name syntax`,
  exactly upstream `stringToQualifiedNameList`'s `ereturn`;
- the first segment is the schema, the rest joined with `.` is the name
  (preserves `splitQualifiedTable`'s 2-level contract; a 3-part regclass name
  like `pg_catalog.pg_class` keeps resolving — measured `1259` on PG 18.3).

goopg's catalog lookups fold case (`key()`/`nameKey()`/`EqualFold`), so the
lookup half is untouched — quote-stripping alone makes quoted names resolve.
Because those lookups are case-insensitive, an **unquoted** `'myfunc'::regproc`
still matches a routine stored as `MyFunc` where PG raises 42883; that
leniency is the pre-existing goopg-wide name model and is out of scope (the
named defect is quoted identifiers failing, not unquoted ones being too lax).
The **collation** store is the one case-sensitive exception
(`CollationOIDByName` is documented PG-identifier semantics), and the parser's
downcasing of unquoted segments now makes `'C'::regcollation` **fail with 42704
exactly like PG 18.3** — only `'"C"'` resolves to 950. The old goopg input
resolved bare `C`; that leniency was a divergence and the accompanying tests
were updated to the PG-faithful shape.

### Call sites (all reg* name→OID input arms)

`internal/executor/reg_identifier.go` (`regIdentifierInput`):
- regclass arm — `splitRegQualifiedName`
- regproc/regprocedure arm — `regprocedureNamePart` (strip the `(…)` arg list,
  see below) then `splitRegQualifiedName`
- regrole arm — `splitRegIdentifiers`, require exactly one segment (the old
  `strings.Contains(name, ".")` false-flagged a quoted role like `"Alice.Bob"`)
- regcollation arm — `splitRegQualifiedName`
- regtype arm — `splitRegQualifiedName` before `TypeNameToOID`/
  `userTypeOIDForName`

`internal/executor/expr.go`:
- `::regproc`/`::regprocedure` cast string→OID arm (`:536`) — strips the arg
  list for regprocedure, then `splitRegQualifiedName`
- `::regclass` cast string→OID arm (`:824`)
- `regclass()` function-call cast arm (`:10249`)
- `pg_get_functiondef`'s text-name fallback (`:10147`)

`regprocedureNamePart` mirrors `parseNameAndArgTypes`' leading scan
(regproc.c:1899): everything before the first LEFT PAREN that is not inside
double quotes. Without it, `'"MyFunc"(integer)'::regprocedure` would hit the
parser's "expected separator" rule at the `(` and raise 42602 — a regression
for the one regprocedure shape that matters (quoted name + arg list). The arg
list itself is still NOT parsed (name-only resolution); see scope exclusions.

The old `splitQualifiedTable` stays for its non-reg* caller
(`operators_pg_get_publication_tables.go`).

## Measurement (throwaway PG 18.3, port 5599)

| input | PG 18.3 |
|---|---|
| `'"MyFunc71"'::regproc::oid` | 16452 (resolves) |
| `'"MyFunc71"(integer)'::regprocedure::oid` | 16452 |
| `'ragout71."Quoted Other"(integer)'::regprocedure::oid` | 16454 |
| `'"my.schema71".dotfn'::regproc::oid` | 16457 |
| `'"My Table71"'::regclass::oid` | 16458 |
| `'"My Coll71"'::regcollation::oid` | 16461 |
| `'"Alice71"'::regrole::oid` | 16462 |
| `'"Weird""Quote71"'::regproc::oid` | 16463 (quote-quote collapse) |
| `' ragout71 . other_func '::regproc::oid` | 16455 (whitespace tolerance) |
| `'"MyFunc71'::regproc` (mismatched quote) | `invalid name syntax` (42602) |
| `'ragout71..dotfn'::regproc` (empty segment) | `invalid name syntax` (42602) |
| `'myfunc71'::regproc` (unquoted ≠ mixed-case fn) | `function "myfunc71" does not exist` |

## Tests

New `internal/executor/reg_input_quoted_test.go` — pins the regproc input half
(the named defect) plus the family siblings:

1. **`TestRegProcInputResolvesQuotedIdentifier`** — `CREATE FUNCTION "MyFunc"`
   then `'"MyFunc"'::regproc::oid` = the routine's OID (the exact failing shape
   from row 1341; pre-fix 42883). Same for `::regprocedure` with `(integer)`.
2. **`TestRegInputQuotedSchemaAndCollapsedQuotes`** — a quoted schema
   containing a dot (`"my.schema"`), a schema-qualified quoted name
   (`ragout."Quoted Other"`), and a quote-quote collapse (`"Weird""Quote"`).
3. **`TestRegInputQuotedFamilySiblings`** — `'"My Table"'::regclass`,
   `'"My Coll"'::regcollation`, `'"Alice"'::regrole` (role registered
   lowercased, so the quoted spelling resolves through the case-insensitive
   role store), and a quoted `"int4eq"` builtin.
4. **`TestRegInputSyntaxErrors`** — mismatched quote and empty unquoted
   segment raise 42602 on both the cast path and the `regIdentifierInput`
   coercion path (INSERT/DEFAULT of a regproc column).
5. **`TestRegIdentifierInputQuotedCoercion`** — the `coerceRowForConstraintChecks`
   route (a `regproc` column DEFAULT `'"MyFunc"'`) resolves through the shared
   parser, not just the cast path.

## Gates

Package suites, `RALPH_PRECOMMIT_SCOPE=units`, `TestPort_RegressSuite`,
`scripts/tpch-spotcheck.sh` (Q12=2, Q13=35).

## Not in scope (recorded, not deferred)

- **Unquoted case-insensitivity** — goopg's catalog lookups fold case, so
  `'myfunc'::regproc` matches a `MyFunc` routine where PG raises 42883; the
  whole name model is case-insensitive by design (pre-existing, deliberate).
- **`regprocedurein` arg-list parsing** — `'my_func(integer)'` resolves by
  NAME only; the arg types are not split off and compared (goopg has no
  overload-matching on input). The `(…)` group is stripped so the name part
  parses, but the types themselves are ignored — a pre-existing gap, now
  bounded by the paren strip.
- **Missing `::regrole`/`::regcollation` CAST arms** — expr.go has no
  string→OID cast arm for regrole or regcollation, so `'alice'::regrole`
  silently no-ops (returns the string). Those two name→OID inputs resolve only
  through the `regIdentifierInput` coercion path (the tests assert there).
  Pre-existing; the quoted-identifier fix does not add the missing casts.
- **`regprocedurein` paren-requirement** — upstream raises 22P02
  "expected a left parenthesis" for a bare regprocedure name with no `(`;
  goopg still resolves a bare name (pre-existing leniency, unchanged).
- **3-part names** — a 3+ segment name is reduced to (first, rest-joined);
  upstream treats the first as a catalog/database name (`cross-database
  references are not implemented` for regclass), a divergence confined to a
  corner-of-corner input that PG 18.3 itself mostly errors on.
- **Mixed-case role names** — goopg's role store folds role names to lowercase
  on registration (ledger row 1340), so `'"Alice"'::regrole` resolves only
  because the store is case-insensitive; `regroleout` still cannot emit a
  case-preserved `"Alice"`.
