# M0122-0015c — PREPARE resolves its declared parameter types

Status: landed 2026-10-12.
Parent: M0122-0015. Found by the `005_pg_dump_filterfile.pl` port.

## Problem

PostgreSQL's PREPARE resolves each declared parameter type through
`typenameTypeId` (commands/prepare.c `PrepareQuery` → parse\_type.c
`LookupTypeName`). That lookup:

- resolves a schema qualifier, raising 3F000 when the schema does not exist;
- finds built-in types in `pg_catalog`;
- finds user types (enum, domain, composite, range, multirange) and table row
  types in their schema.

goopg instead checked the declared text against a hard-coded list of
unqualified built-in names (`isValidSQLTypeName`, postmaster/dispatch.go).
Two consequences:

- `PREPARE x(pg_catalog.oid)`, `PREPARE x(mood)` (an enum) and
  `PREPARE x(public.t)` (a row type) all raised 42704.
- pg\_dump runs `PREPARE dumpFunc(pg_catalog.oid)` before dumping the first
  function, so pg\_dump and pg\_dumpall of any database holding a user
  function exited 1.

## Design

`executor.ResolvePrepareParamType(ctx, spelled, isBuiltin)` is
`typenameTypeId` for one declared type. It runs these steps:

1. **Split the qualifier.** A leading `schema.` is split off: the first `.`
   outside double quotes and before any typmod or array suffix. The schema
   is unquoted or lower-cased, as ColId would deliver it.
2. **Check the schema.** It must exist in the connection's database, else
   3F000 `schema "x" does not exist`. This comes first, as in
   LookupTypeName.
3. **Built-in types.** A built-in matches only when the name is unqualified
   or `pg_catalog`-qualified. The caller's built-in table (the old allowlist)
   makes that decision after the qualifier is gone, so typmods, arrays and
   multi-word names are handled as before.
4. **User and row types.** User types (`userTypeOIDForName`, the registry
   `::regtype` uses) and table row types (`LookupTable` in the qualifier's
   schema, `public` when unqualified) match under any schema except
   `pg_catalog`.
5. **No match.** Anything else raises 42704 `type "<as written>" does not
   exist`.

The function returns the spelling with the qualifier resolved away. That
string is what the prepared statement stores: EXECUTE's argument coercion
(`CoerceParamToDeclaredType`, `normPrepParamType`) and the parameter-type
inference read it as a bare type name. Storing `pg_catalog.oid` would only
move the failure from PREPARE to EXECUTE.

## Verification

- Probe: 14 PREPARE / EXECUTE forms (qualified built-ins, typmod, array,
  `"char"`, enum bare and qualified, row type, and the five error forms).
  Values, SQLSTATEs and messages match PG 18.3. The only difference is the
  missing caret, covered in the next section.
- `pg_dump` of a database with sql and plpgsql functions exits 0 and emits
  both functions.
- Test: `TestResolvePrepareParamType`.
- Regress A/B: `prepare`, `plancache`, `prepared_xacts`, `domain` and `enum`
  are identical. `plpgsql` shows only its known overload flap, confirmed by
  rerunning HEAD.

## Residuals (ledgered)

- **No caret.** PG draws a `LINE` caret at the type name on these errors.
  goopg's `PrepareStmt.ParamTypes` is a list of strings without positions.
  The grammar's `prep_type_list` would have to carry `fn_type`'s position.
- **Schema-blind user types.** User types are resolved by name across
  schemas. goopg's type registries have no per-schema namespace (the same
  limitation `::regtype` documents).
- **Row-type parameters are only accepted.** A row-type parameter passes
  PREPARE, but EXECUTE's coercion to a composite is not exercised.
