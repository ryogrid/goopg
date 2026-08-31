# 0119-0006 — bare user-type arg names resolve their owner schema at CREATE (M0119-0006)

Status: accepted
Closes deferral-ledger row 1343.

## Defect

A BARE user-defined arg type in `CREATE FUNCTION`/`CREATE PROCEDURE` stored an
empty schema in `Routine.ArgTypeSchemas[i]`, so its regprocedure arglist rendered
bare — `g(myenum)` — where PG 18.3's `format_type_be` resolves the type's
namespace and schema-qualifies it: `g(offpath.myenum)`.

`CREATE FUNCTION g(mytype)` for a type in a non-public schema is the repro; the
type name carries no `ColumnType.Schema`, and `argTypeSchema` (operators_call.go)
returned `t.Schema` verbatim → `""`.

## Root cause

The 73rd slice (row 1342) captured only an *explicit* `ColumnType.Schema`. Row
1343's original premise — "the user-type store has no namespace field" — is
stale: the 88th slice (`f4d594d3`, row 1355) already added `NamespaceOID` to the
enum/domain/composite/range registries, populated at CREATE TYPE/DOMAIN and at
startup reload. The only missing piece was resolving a bare NAME to that
namespace at routine-creation time.

## Oracle

PG resolves each routine arg type's pg_type tuple at creation via
`typenameTypeId` / `LookupTypeNameExtended` (`parse_type.c:291`, `:73`), then
`format_type_be` (format_type.c:315/318/322) schema-qualifies via
`TypeIsVisible` → `get_namespace_name_or_temp(typeform->typnamespace)` →
`quote_qualified_identifier`.

## Change

`argTypeSchema(t parser.ColumnType, cat catalog.Catalog, dbOid uint32) string`
(operators_call.go):

- `t.Schema != ""` → returned verbatim (never re-lowered; quoted qualifiers are
  case-preserved) — unchanged behavior.
- bare name → strip a trailing `[]` (`splitArraySuffix`), then probe the
  user-type name-keyed lookups in the `userTypeOIDForName` order — `LookupEnum`
  → `LookupDomain` → `LookupCompositeType` → `LookupRangeType` →
  `LookupRangeTypeByMultirangeName` — and return
  `cat.SchemaNameForOID(hit.NamespaceOID)` on the first hit, else `""` (a bare
  builtin hits no registry).

Both capture call sites (execCreateFunction / execCreateProcedure,
operators_ddl.go) pass `o.ctx.Catalog` + the RAW `o.ctx.CurrentDatabaseOid`.
The user-type registries are keyed by the exact value `RegisterEnum`/… received
(raw `CurrentDatabaseOid`), *unlike* the routine registry which normalizes via
`NamespaceDBOid` — a normalized probe would miss a type registered in the same
live session (raw 0) or on a real postgres connection (raw 5).

The renderer (`regprocedureArglist`, reg_identifier.go) needs no change: it
already schema-qualifies a populated `Schema` via `quoteQualifiedIdentifier` /
`pgQuoteIdent` under the session visibility predicate.

## Tests

`TestCreateFunctionCapturesBareArgTypeSchema`
(regoperator_schema_qualify_test.go): all four user-type kinds in a non-public
schema; bare builtin (`integer`) stays bare; non-visible vs visible search_path
rendering; array element form (`myenum[]`); and the PROCEDURE sibling capture
(Hard-won Rule #2). The 73rd slice's explicit-schema regression test stays
green.

## Gates

package suites (executor/catalog), pre-commit units, `scripts/tpch-spotcheck.sh`
(Q12=2, Q13=35), `TestPort_RegressSuite` (45 PASS / 0 FAIL) — all PASS.

## Deferrals (new rows)

- Type-registry dbOid keying mismatch: live DDL keys types on raw
  `CurrentDatabaseOid`, startup reload keys on `cat.DBOID()` — a bare-type arg
  in a non-default database after a restart may miss the probe.
- `LookupRangeTypeByMultirangeName` has no dbOid parameter (scans all DBs).
- Composite types still do not reload at startup (pre-existing).
