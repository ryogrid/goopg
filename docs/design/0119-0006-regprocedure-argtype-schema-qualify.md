---
status: accepted
date: 2026-08-14
supersedes: none
milestone: M0119-0006 (73rd slice)
---

# regprocedure arglist schema-qualifies non-visible arg types (format_type_be)

Closes deferral ledger row 1342 (the 71st slice's carry-forward). The
regprocedure arglist renders each input arg type through `pgArgTypeDisplayAlias`
only — a bare alias — where upstream `format_procedure_extended` passes each arg
type through `format_type_be`, which schema-qualifies a type whose namespace is
off the session's effective search_path (`schema.typename`). A routine whose
signature references an off-path user-defined arg type renders
`public.foo(integer)` where PG 18.3 emits `public.foo(public.mytype)`.

## 1. Problem

The 71st slice (`0119-0006-regprocedure-qualified-name.md`) made the regprocedure
output qualify ONLY the routine NAME (`quote_qualified_identifier(nspname,
proname)` when `FunctionIsVisible` is false) and appended the arglist unquoted.
That closed row 1338, but the arglist itself was left as the 71st slice's
pre-existing `formatProcedureArglist`: each input arg type name run through
`pgArgTypeDisplayAlias` (int4→integer etc.), never schema-qualified. That is
correct for a builtin arg type (`integer`, `text` — they live in pg_catalog,
which every search_path searches implicitly, so `format_type_be` never qualifies
them) but wrong for a USER-defined arg type whose namespace is off the path.

Measured against a throwaway PG 18.3 oracle (port 5599), DB `probe1342`,
`CREATE FUNCTION` battery in the `public` schema + a composite type + a table in
`offpath`/`onpath`:

| probe (default `search_path` = `"$user", public`) | goopg today | PG 18.3 |
|---|---|---|
| `oid::regprocedure` for `f_offarg(offpath.mytype)` | `f_offarg(mytype)` | `f_offarg(offpath.mytype)` |
| `f_onarg(onpath.mytype)` | `f_onarg(mytype)` | `f_onarg(onpath.mytype)` |
| `offpath.f_offboth(offpath.mytype)` | `offpath.f_offboth(mytype)` | `offpath.f_offboth(offpath.mytype)` |
| `f_offarr(offpath.mytype[])` | `f_offarr(mytype[])` | `f_offarr(offpath.mytype[])` |
| `f_offrow(offpath.ct)` (rowtype arg) | `f_offrow(ct)` | `f_offrow(offpath.ct)` |
| `f_builtin(integer)` (builtin arg) | `f_builtin(integer)` | `f_builtin(integer)` |
| `SET search_path = public, offpath`; `f_offarg` | `f_offarg(mytype)` | `f_offarg(mytype)` |

The routine NAME already qualifies (71st slice); only the ARGLIST diverges. The
search_path row shows the qualify decision is per-arg-type and session-dependent:
putting `offpath` on the path makes `offpath.mytype` visible → `format_type_be`
renders it bare, exactly like a builtin.

## 2. The upstream rule

`format_procedure_extended` (regproc.c:326) appends each input arg type via
`format_type_be`:

```c
for (i = 0; i < nargs; i++)
    appendStringInfoString(&buf, format_type_be(thisargtype));
```

`format_type_be` (format_type.c:343) = `format_type_extended(oid, -1, 0)`, which
does three things in order:

1. **A builtin OID in the special-case switch** → the bare SQL alias
   (`boolean`, `integer`, `bigint`, `real`, `double precision`,
   `character varying`, `timestamp without time zone`, …) — NEVER qualified,
   regardless of visibility. goopg's `pgArgTypeDisplayAlias` is the port of this
   switch.
2. **Any other type** → `nspname = NULL` when `TypeIsVisible(type_oid)` (the
   type's namespace is pg_catalog or on the search_path), else
   `get_namespace_name(typnamespace)`.
3. Render `quote_qualified_identifier(nspname, typname)`, appending `[]` for a
   true array.

So the qualify decision is **per arg type** (each arg's own OID → namespace →
`TypeIsVisible`), not a single session proxy. Two facts follow:

- **The schema is a property of the type**, fixed at `CREATE FUNCTION` when the
  arg type is resolved to an OID. goopg's user-type store has no namespace
  field, so the schema must be captured from the DDL's explicit qualification at
  CREATE time (see §3.1 for the bounded bare-name limitation).
- **Visibility is a session property**, re-checked at RENDER time (a later
  `SET search_path` changes the output). goopg already has the per-schema check:
  `regObjectSchemaVisible(ctx, schema)` (expr.go) — the same `TypeIsVisible`
  shape (pg_catalog implicit, else search-path membership) the reg* name paths
  use.

## 3. goopg implementation

Four parts.

### 3.1 Capture arg-type schemas at CREATE FUNCTION/PROCEDURE

New `Routine.ArgTypeSchemas []string`, parallel to `ArgTypes` (same length,
same order), populated in `execCreateFunction` (operators_ddl.go:11767) and
`execCreateProcedure` (:12504) by a shared helper:

```go
// argTypeSchema returns the namespace an argument type belongs to, for the
// regprocedure output path (deferral row 1342). Only an EXPLICITLY
// qualified name (ColumnType.Schema) yields a schema — goopg's user-type
// store has no namespace field, so a BARE type name cannot be mapped to its
// owner schema (bounded limitation, see §3.5). A pg_catalog-qualified name
// yields "pg_catalog" so the renderer's builtin-alias + never-qualify arms
// apply. A bare BUILTIN name keeps "" (equivalent rendering: the renderer
// treats "" and "pg_catalog" identically).
func argTypeSchema(t parser.ColumnType) string {
    return t.Schema // verbatim — the parser already case-folded unquoted
}
```

The parser's `ColumnType.Schema` (ast.go:1109, parseColumnType ddl.go:4600)
carries the explicit qualifier with case already folded for UNQUOTED
identifiers (`identText` returns the lowercased token) but PRESERVED for a
quoted `"OffPath".mytype`, so the helper must return it verbatim — never
re-lower. No lookup is needed for the qualified case; bare names → `""` (keeps
today's rendering). The array suffix lives in the arg NAME
(`routineArgTypeName` bakes `[]` into the string); the captured schema applies
to the ELEMENT (the §3.3 renderer splits and re-appends the suffix) and needs no
array handling at capture time.

### 3.2 Expose per-arg schemas to the renderers

`catalog.RegprocedureNameParts` (catalog.go:19877) currently returns the
pre-rendered `arglist string`. It instead returns a slice of resolved arg parts:

```go
// RegprocArg is one INPUT argument type of a pg_proc signature: the stored
// name (array suffix baked in, as Routine.ArgTypes[i].Name carries it) plus
// the namespace it was created in ("" when unknown). The qualify decision is
// the renderers' job, per-arg and session-dependent.
type RegprocArg struct{ Name, Schema string }

func RegprocedureNameParts(oid uint32, routines *Routines) (schema, name string, argTypes []RegprocArg, ok bool)
```

- Builtin path (`pgProcArgTypeNamesByOID`): every arg gets `Schema: "pg_catalog"`.
- User path: skip OUT-only args as today (`ArgModes[i] == "o"`), each remaining
  arg gets `Name: t.Name`, `Schema: ArgTypeSchemas[i]` (nil → `""`, defensive
  for a pre-73rd reloaded routine).

`RegprocedureName`/`RegprocedureNameAndSchema` keep their existing
`name(arglist)` contract via a bare builder:

```go
// formatProcedureArglist renders the UNQUALIFIED format_type_be display list
// (alias-only) — the shape RegprocedureName/AndSchema expose. The pg_faithful
// qualify-by-visibility renderer is executor/regprocedureArglist (§3.3).
func formatProcedureArglist(argTypes []RegprocArg) string
```

`pgArgTypeDisplayAlias` is exported as `catalog.ArgTypeDisplayAlias` (rename +
internal refs) so the executor renderer shares ONE alias table with the catalog
builder — no second alias source to drift.

### 3.3 The pg-faithful arglist renderer (executor side)

New private `regprocedureArglist` in reg_identifier.go, used by BOTH production
renderers so SELECT and COPY cannot drift (Hard-won Rule #2):

```go
// regprocedureArglist renders format_type_be for each INPUT arg type
// (regproc.c format_procedure_extended), schema-qualifying per-arg via the
// session's search_path (deferral row 1342). pg_catalog (builtin) and
// unknown-"" arg types render the bare SQL alias; a user type whose schema is
// NOT visible renders quote_qualified_identifier(schema, name) — the same
// quote rule the family's name path uses (regOutQualified). nil visible ⇒ all
// visible (bare), preserving the base RegOut callers.
func regprocedureArglist(argTypes []catalog.RegprocArg, visible func(schema string) bool) string {
    args := make([]string, len(argTypes))
    for i, a := range argTypes {
        base, isArray := splitArraySuffix(a.Name) // "mytype[]" → ("mytype", true)
        name := base
        if a.Schema == "" || a.Schema == "pg_catalog" {
            name = catalog.ArgTypeDisplayAlias(base)
        }
        if a.Schema != "" && a.Schema != "pg_catalog" && visible != nil && !visible(a.Schema) {
            name = quoteQualifiedIdentifier(a.Schema, name) // quotes the ELEMENT
        }
        if isArray {
            name += "[]"
        }
        args[i] = name
    }
    return strings.Join(args, ",")
}
```

Two deliberate details (reviewer-verified):

- **The array suffix is split off BEFORE quoting/aliasing and re-appended after.**
  The stored arg name carries `[]` baked in (`routineArgTypeName`,
  operators_call.go:671), so quoting the whole name would yield
  `offpath."mytype[]"` — `pgQuoteIdent` treats `[`/`]` as needing quotes —
  where PG emits `offpath.mytype[]`. Quoting the element and re-appending `[]`
  fixes that AND (as a small inherited-gap win) makes a bare builtin array
  alias correctly: `int[]` → `integer[]`, matching `format_type_be`'s
  array-element aliasing. The catalog's bare `formatProcedureArglist`
  (RegprocedureName/AndSchema) does NOT split — it renders the stored name
  unchanged, preserving its pre-existing behavior (builtin-array aliasing there
  is a separate pre-existing gap, §6).
- **A user type in a non-pg_catalog schema does NOT go through
  `ArgTypeDisplayAlias`** (a user composite named `int` must not alias to
  `integer`); the alias gating is a small correctness win over today's
  unconditional alias.

### 3.4 Thread the session visibility predicate to both renderers

`regObjectSchemaVisible` (expr.go:13800) is exported as
`executor.RegObjectSchemaVisible` (rename + internal refs at expr.go cast path
and copy.go). Then:

- **New `RegOutArgVisible`** (reg_identifier.go) — `RegOut`'s sibling with an
  extra `argVisible func(schema string) bool`:

  ```go
  func RegOut(typeName string, oid uint32, cat catalog.Catalog, qualify bool) string {
      return regOut(typeName, oid, cat, qualify, nil) // base callers: bare arglist
  }
  func RegOutArgVisible(typeName string, oid uint32, cat catalog.Catalog, qualify bool,
      argVisible func(schema string) bool) string {
      return regOut(typeName, oid, cat, qualify, argVisible)
  }
  ```

  RegOut keeps its signature (≈30 test callers unchanged); the production
  SELECT/COPY paths switch to `RegOutArgVisible`.

- **Variadic `visible ...func(schema string) bool`** added to the value-seam
  functions that carry `cat, qualify` down to RegOut, so the ~20 existing
  callers (tests + non-reg paths) need no edit while production supplies the
  predicate:

  - `EncodeCopyTextRow(dst, row, cols, dateStyle, dateOrder, timeZone, cat,
    qualify, visible ...func(schema string) bool)` (copy_text.go:35)
  - `EncodeCopyCsvRow(...)` (copy_csv.go — mirrors)
  - `datumToCopyText(t, d, dateStyle, dateOrder, timeZone, cat, qualify,
    visible ...func(schema string) bool)` (copy_text.go:271)
  - `appendTypedCellText(dst, d, typ, getSetting, visible ...func(schema string)
    bool)` (dispatch.go:3097)

  This preserves the 68th slice's deliberate design (value params, not a
  *Context, so the TEXT/CSV renderers share the narrow seam) — the closure is
  a value like `cat`/`qualify`.

- **Production sites compute the predicate where ctx is available** — ALL three
  `appendTypedCellText` callers and both COPY paths, so the simple-query,
  extended-protocol, and COPY wire paths cannot drift:
  - COPY: `CopyOutTo` (copy.go:60) already has `ctx`; add
    `visible := func(s string) bool { return regObjectSchemaVisible(ctx, s) }`
    beside the existing `qualify` computation (:84).
  - SELECT simple-query: dispatch.go:2825 has `ctx`; pass
    `func(s string) bool { return executor.RegObjectSchemaVisible(ctx, s) }`.
  - SELECT extended-protocol: dispatch_extended.go:470 has `ectx`
    (`*executor.Context`, :203); pass the same
    `func(s string) bool { return executor.RegObjectSchemaVisible(ectx, s) }`.
  - The `::regprocedure` cast (expr.go:604) already has `ctx`; switch to
    `regprocedureArglist(argTypes, func(s string) bool {
       return regObjectSchemaVisible(ctx, s) })`.

### 3.5 Persistence and limitations

- `ArgTypeSchemas` rides the existing Routine JSON round-trip (proargdefaults
  via `pgProcArgMetaJSON`/`DecodePGProcArgMeta`) automatically; a pre-73rd data
  dir's reloaded routines have the key absent → nil → `""` schemas → bare args,
  exactly today's behavior. Backward compatible.
- Signature matching is untouched: `ArgTypeSchemas` is consulted only by the
  regprocedure OUTPUT path; `Signature()` compares `ArgTypes[i].Name` only, so
  ALTER/DROP/COMMENT lookups and overload resolution are unaffected.
- **Bare-name arg types stay unqualified.** A `CREATE FUNCTION f2(mytype)` with
  `offpath` on the path still renders `f2(mytype)` (PG resolves `mytype` to an
  OID and renders `f2(offpath.mytype)`). This is a consequence of goopg's
  user-type store having no namespace field — the same gap that makes
  `CREATE TABLE`'s column-type resolution name-based. It is filed separately
  (§6) rather than widened here, because adding a namespace to
  enum/composite/domain/range types is a store-wide change that also
  unblocks the pg_proc proargtypes OID column and pg_dump type-qualification.
  The explicit-qualification case (the row-1342 repro, every probe above) is
  fully fixed. **Note (85th slice):** the §1 search_path row (line 42) held only
  because the probe put a TABLE in `offpath` — `searchPathSchemas` proved a user
  schema's existence via `LookupTable`, which cannot see an EMPTY schema. With
  `offpath` empty, `SET search_path = public, offpath` rendered
  `f_offarg(offpath.mytype)` (deferral row 1347). The 85th slice swapped the
  proxy to `Catalog.SchemaExists` (empty schemas registered at CREATE SCHEMA), so
  the empty-offpath case now renders bare too; pinned by
  `TestRegObjectSchemaVisibleSeesEmptySchema`.
- **Literal-Const vs column-cast qualify imprecision** (the 69th slice's
  public-proxy vs per-schema flag) is unchanged this slice: the NAME qualify
  still comes from the caller's proxy flag on the SELECT/COPY paths and the
  per-schema check on the cast path. The ARGLIST now uses the real per-arg
  visibility on both paths, so the two renderers agree on args even under a
  non-default search_path; the name-proxy divergence is the documented
  pre-existing gap (§6).

## 4. Gates

New unit tests:

- `TestRegOutRegprocedureQualifiesArgTypes` (reg_qualify_test.go) — a user
  routine whose `ArgTypeSchemas = ["offpath"]` renders `offpath.mytype` at
  `RegOutArgVisible` qualify=true with a per-schema `visible` (offpath not
  visible) and bare `mytype` with offpath visible; the ARRAY variant
  (`ArgTypes = ["mytype[]"]`, `ArgTypeSchemas = ["offpath"]`) renders
  `offpath.mytype[]` (element quoted, `[]` re-appended — reviewer BLOCKER 1),
  and a builtin array (`ArgTypes = ["int[]"]`, `ArgTypeSchemas =
  ["pg_catalog"]`) renders `integer[]`; builtin scalar `["pg_catalog"]` always
  bare; base `RegOut` (no predicate) stays bare.
- `TestRegprocedureCastArgTypesQualify` (regoperator_schema_qualify_test.go or
  a new file) — the `::regprocedure` cast sibling agrees with the column path
  (same fixture, same expected strings).
- `TestRegCopyAndSelectSiblingArgQualifyAgree` (reg_copy_sibling_test.go) — a
  raw `regprocedure`-typed column whose OID is an off-path-arg routine: COPY TO
  and SELECT wire render the same qualified arglist.
- `TestCreateFunctionCapturesArgTypeSchemas` (operators_ddl_test.go or the
  reg test file) — `CREATE FUNCTION f(offpath.mytype, pg_catalog.int4, int)`
  stores `ArgTypeSchemas = ["offpath", "pg_catalog", ""]` (via a live
  execCreateFunction, or the Routine registry after a real DDL run).
- `regproc_name_test.go` stays green: `RegprocedureName` still renders the
  bare alias-only arglist.

Expected strings are pinned to the §1 oracle table.

Gates: package suites (`internal/executor`, `internal/server`,
`internal/catalog`), pre-commit units, `TestPort_RegressSuite`,
`scripts/tpch-spotcheck.sh` (Q12=2, Q13=35).

## 5. Files

- `internal/catalog/routines.go` — `Routine.ArgTypeSchemas []string`
- `internal/catalog/catalog.go` — `RegprocedureNameParts` returns `[]RegprocArg`;
  `formatProcedureArglist([]RegprocArg)` (bare); `pgArgTypeDisplayAlias` → exported
  `ArgTypeDisplayAlias`; `RegprocArg` struct
- `internal/executor/operators_ddl.go` — `argTypeSchema` helper + populate
  `ArgTypeSchemas` in execCreateFunction/execCreateProcedure
- `internal/executor/reg_identifier.go` — `RegOutArgVisible`, private `regOut`,
  `regprocedureArglist`
- `internal/executor/expr.go` — export `regObjectSchemaVisible`→
  `RegObjectSchemaVisible`; `::regprocedure` cast sibling uses
  `regprocedureArglist`
- `internal/executor/copy.go`, `copy_text.go`, `copy_csv.go` — variadic
  `visible` threading
- `internal/server/dispatch.go`, `dispatch_extended.go` — `appendTypedCellText`
  variadic `visible` + predicate at both call sites
- tests: reg_qualify_test.go, regoperator_schema_qualify_test.go,
  reg_copy_sibling_test.go, + one CREATE-capture test

## 6. Deferred (new ledger rows) and notes

- **Bare user-defined arg types cannot be schema-resolved** — goopg's user-type
  store (enum/composite/domain/range) has no namespace field; a bare arg type
  name cannot be mapped to its owner schema at CREATE, so its regprocedure
  output stays bare where PG qualifies. Fixing this is a store-wide namespace
  field (also unblocks pg_proc.proargtypes OID resolution and pg_dump
  type-qualification) and is out of scope here.
- **Quoted user type NAMES lose case at CREATE** (`routineArgTypeName`
  lowercases), so `offpath."MyType"` stores `mytype` and renders `offpath.mytype`
  where PG emits `offpath."MyType"` — pre-existing (the same case-folding family
  as the mixed-case role-name row 1340), inherited, not a regression.
- **The catalog's bare `formatProcedureArglist` (RegprocedureName/AndSchema)
  does not alias builtin ARRAYS** (`int[]` stays `int[]`; the executor renderer
  now emits `integer[]`) — pre-existing gap, kept so the catalog display API's
  behavior is untouched.
- **`ArgTypeDisplayAlias` is not a faithful port of format_type_be's switch**:
  no `varbit → bit varying`, and builtins PG renders through the default path
  with keyword quoting (`char` → `"char"`) are unmodeled. Pre-existing.
- **The name-qualify proxy imprecision** (publicSchemaVisible vs per-schema) on
  the SELECT/COPY reg* name path is the documented 69th-slice gap, unchanged.
- regproc/regprocedure INPUT DB-scoping bug (regIdentifierInput passes no
  dbOid to LookupByName) — pre-existing, separate.
- **Existing sibling tests that call `appendTypedCellText`/`EncodeCopyTextRow`
  directly** (reg_copy_sibling_test.go:91-199, executor/reg_copy_test.go:89-144)
  will exercise the nil→bare fallback unless they pass the production predicate;
  the new `TestRegCopyAndSelectSiblingArgQualifyAgree` supersedes them, and the
  direct-call tests that have a ctx in scope pass the ctx-based predicate to keep
  guarding production.
