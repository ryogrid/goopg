---
status: accepted
date: 2026-08-14
supersedes: none
milestone: M0119-0006 (88th + 89th slices)
---

# regtype qualifies a user type with its actual schema and quotes the name

Closes deferral ledger row 1355 (the 84th slice's carry-forward). goopg's
regtype renderer — `userTypeNameForOID`/`RegtypeName` (`internal/executor/
expr.go`) — prefixed every user-defined type name with the hardcoded literal
`"public."` (only when `qualify=true`) and the enum/domain/composite/range
registries tracked no schema at all. Upstream `regtypeout` →
`format_type_be` → `format_type_extended` schema-qualifies a user type whose
namespace is off the session's search_path with its ACTUAL namespace, and
always `quote_identifier`s the name.

## 1. Problem

The 84th slice (`0119-0006-pgoutput-reg-names.md`) fixed the pgoutput reg*
renderer's off-path qualification for the regclass/regproc/regprocedure/
regcollation arms but DELIBERATELY left the regtype arm on a fixed
`regtypeQualify bool`. The reason it could not follow the other arms was a
catalog-representation gap: the four user-type registries —

- `EnumType` (catalog.go:3011), `LookupEnumByOID` (21349)
- `Domain` (catalog.go:3043), `LookupDomainByOID` (22770)
- `CompositeType` (catalog.go:3168), `LookupCompositeTypeByOID` (21768)
- `RangeType` (catalog.go:3204), `LookupRangeTypeByOID` (22385)

— each carry `Name`/`OID`/`ArrayOID`/`Owner`/`DBOid` but **no schema field**
(unlike `UserCollation`, which already had `NamespaceOID`). So
`userTypeNameForOID` could not know a user type's real schema, and instead
emitted `"public." + Name` when told to qualify. A `CREATE TYPE
myschema.mood` therefore rendered `public.mood` (or bare `mood`) where PG
renders `myschema.mood`, and a mixed-case name was never quoted.

## 2. The upstream rule

`regtypeout` (regproc.c:1247) calls `format_type_be(typid)`
(format_type.c:343) → `format_type_extended(type_oid, -1, 0)`. For a user
type the default handling (format_type.c:303-326) is:

```c
if ((flags & FORMAT_TYPE_FORCE_QUALIFY) == 0 && TypeIsVisible(type_oid))
    nspname = NULL;                       /* visible → bare quoted name */
else
    nspname = get_namespace_name_or_temp(typeform->typnamespace);
buf = quote_qualified_identifier(nspname, typname);
```

`TypeIsVisible` (namespace.c:1039) tests the type's ACTUAL `typnamespace`
against the session search_path. Two facts drive the goopg change:

1. Qualification is decided by the type's real `typnamespace`, not by a
   "public" proxy — `public` is just a schema like any other.
2. The name is ALWAYS quoted via `quote_qualified_identifier` (which with a
   NULL nspname returns just the quoted name); builtin/pg_catalog types are
   implicitly visible and never qualify.

## 3. goopg implementation

Two slices.

**Slice A (`f4d594d3`, catalog representation).** Added `NamespaceOID uint32`
to all four registries (mirroring `UserCollation.NamespaceOID`), populated at
every entry-creation site:

- DDL `execCreateType` (all four branches — RANGE/COMPOSITE-with-fields/ENUM/
  COMPOSITE-shell) and `execCreateDomain` set it right after the register call
  using the CREATE AGGREGATE schema-with-public-fallback pattern
  (`SchemaOID(s.Schema)`, fallback `SchemaOID("public")`); the schema name is
  already parsed into `s.Schema` but was previously dropped.
- Startup/WAL reload (`internal/initdb/catalog_heap_reload.go`):
  `reloadUserEnumsFromHeap`/`reloadUserDomainsFromHeap`/
  `reloadUserRangeTypesFromHeap` now capture pg_type's `typnamespace` (col 2)
  instead of dropping it.

`ALTER TYPE … ADD/DROP/ALTER COLUMN` needs no change: it re-registers through
`RegisterCompositeTypeWithFields`, which reuses the same `*CompositeType`
pointer (only `.Fields` is replaced), so `NamespaceOID` survives every ALTER.

**Slice B (`3a03e18e`, rendering).** Consumed the field:

- `Catalog` interface gained `SchemaNameForOID(oid uint32) string` (reverse of
  `SchemaOID`; `*InMemory` is the sole implementer).
- `userTypeNameForOID`/`RegtypeName` changed `qualify bool` →
  `qualify func(schema string) bool`. Each of the ten user-type arms
  (enum/domain/composite/range × scalar/array/multirange/multirange-array)
  captures `NamespaceOID`, resolves `schema := cat.SchemaNameForOID(...)`, and
  renders `regOutQualified(schema, name, qualify(schema))`, keeping the
  `"[]"`/`MultirangeName` suffix split OUTSIDE the quoting (the 73rd-slice
  split/re-append convention).
- New `regOutQualifySchema` defaults `"" → "public"` BEFORE evaluating the
  predicate, so a `NamespaceOID == 0` type (bare `RegisterEnum` in a test, or
  a type with no recorded schema) behaves exactly like a public type — and
  `regOutQualified` defaults `"" → "public"` too, keeping predicate and
  rendered schema consistent.
- `regOutShared` dropped the separate `regtypeQualify bool`; the regtype arm
  now uses the same `qualify` predicate as regclass/regcollation/regprocedure
  (`RegOut`/`RegOutArgVisible`/`RegOutRendererVisible` updated).
- The four direct `userTypeNameForOID` callers — the `::regtype` cast
  (expr.go:727/759, the concrete PG divergence), `format_type()` (11910), and
  `RegtypeName` (14163) — and the `RegtypeName` caller in PL/pgSQL RAISE
  (plpgsql_runtime.go:2991) all pass a per-schema closure
  `func(s) bool { return !RegObjectSchemaVisible(ctx, s) }` instead of the
  fixed `!RegObjectSchemaVisible(ctx, "public")`.

## 4. Gates

New `internal/executor/regtype_actual_schema_test.go`:
`TestRegtypeOffPathActualSchema` (an off-path enum/domain renders its ACTUAL
schema on `::regtype` under both the default and `search_path=''` paths — not
`public.mood`; on-path stays bare), `TestRegtypeMixedCaseQuoted`
(`myschema."MyType"`, and `"PubMood"` bare on-path), and
`TestRegtypeSiblingAgreementOffPath` (SELECT wire / COPY TO / `::regtype`
cast / bare `RegOut` agree under `search_path=''`). Existing
`pg_typeof_oid_test.go` and `user_type_oid_name_test.go` callers converted to
the predicate form (no expected-output churn).

`go build ./...` PASS; `go test ./internal/executor/ ./internal/server/
./internal/wal/ ./internal/catalog/ ./internal/plpgsql/` PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35).

## 5. Files

- `internal/catalog/catalog.go` — `NamespaceOID` on the four registries +
  `SchemaNameForOID` on the `Catalog` interface
- `internal/executor/operators_ddl.go` — `execCreateType`/`execCreateDomain`
  set `NamespaceOID`
- `internal/initdb/catalog_heap_reload.go` — reload captures `typnamespace`
- `internal/executor/expr.go` — `userTypeNameForOID`/`RegtypeName`/`regOutQualifySchema`
- `internal/executor/reg_identifier.go` — `regOutShared` drops `regtypeQualify`
- `internal/executor/plpgsql_runtime.go` — RAISE per-schema closure
- `internal/executor/regtype_actual_schema_test.go`,
  `internal/executor/user_type_namespace_oid_test.go` — tests

## 6. Residual (deferred)

The wire/COPY/`reg*`→text/array-element paths still pass a constant
`!publicSchemaVisible` / `!RegObjectSchemaVisible(ctx, "public")` predicate
(`internal/server/dispatch.go:3227`, `internal/executor/copy.go:90`,
`regCastQualify` expr.go:3381, `codec_array.go:335`). Under the DEFAULT
search_path an off-path non-public type therefore renders BARE on those paths
while the `::regtype` cast / `format_type()` / walsender / RAISE now render
`myschema.mood` — the same "per-object imprecision" the regclass/regcollation
arms already carry (row 1339), which the brief scoped out. Recorded as a
follow-up ledger row; closing it means threading a per-schema predicate
through those four call sites.
