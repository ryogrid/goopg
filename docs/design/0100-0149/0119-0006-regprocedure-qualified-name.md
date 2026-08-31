---
status: accepted
date: 2026-08-14
supersedes: none
milestone: M0119-0006 (71st slice)
---

# regprocedure qualifies only the routine NAME (format_procedure)

Closes deferral ledger row 1338 (the 69th slice's carry-forward). The
regprocedure arm of `executor.RegOut` returned the BARE signature
(`my_udf()` via `catalog.RegprocedureName`) where upstream `regprocedureout` →
`format_procedure` (regproc.c) schema-qualifies the routine NAME when the
routine is off the session's effective search_path, and quote_identifiers the
name in BOTH arms.

## 1. Problem

The 69th slice (`0119-0006-regout-schema-qualification.md`) gave the reg*out
family a shared `regOutQualified(schema, name, qualify)` rule and ran the
regclass/regproc/regrole/regcollation arms through it. The regprocedure arm was
deliberately left alone: its display form is `name(argtype1,argtype2)` —
`format_procedure`, not a bare name — and the 69th slice kept the parens
unquoted on purpose (running the whole signature through `pgQuoteIdent` would
wrongly quote `int4out(integer)`). The result: a `regprocedure` column / COPY TO
render of an OFF-path routine emitted the bare signature where PG 18.3 emits
`public.my_udf()`.

The `qualify` flag was already plumbed into the arm's caller (COPY computes
`!regObjectSchemaVisible(ctx, "public")`, SELECT
`!publicSchemaVisible(getSetting)`), so the fix is local to the arm.

## 2. The upstream rule

PG 18.3 moved `format_procedure` into regproc.c as `format_procedure_extended`
(regproc.c:326), which replaces the older `format_procedure_internal`
(ruleutils.c) this deferral's resume point cited:

```c
if ((flags & FORMAT_PROC_FORCE_QUALIFY) == 0 &&
    FunctionIsVisible(procedure_oid))
    nspname = NULL;                            /* visible → bare name */
else
    nspname = get_namespace_name(procform->pronamespace);

appendStringInfo(&buf, "%s(",
                 quote_qualified_identifier(nspname, proname));
for (i = 0; i < nargs; i++)
    ... appendStringInfoString(&buf, format_type_be(thisargtype)); ...
appendStringInfoChar(&buf, ')');
```

Key facts:

- **The name is ALWAYS quote_identifier'd** — `quote_qualified_identifier`
  with a NULL qualifier reduces to `quote_identifier(proname)`. So a visible
  mixed-case routine renders `"MyFunc"(integer)`, not `MyFunc(integer)`.
- **Schema qualification applies ONLY to the name**: the qualifier is
  `quote_qualified_identifier(nspname, proname)` and the arglist is appended
  unquoted after `(`.
- **The arglist is `format_type_be`** (unqualified display aliases) — the
  `pgArgTypeDisplayAlias` list goopg already builds.
- **`FunctionIsVisible`** = the routine's schema is pg_catalog (implicitly
  searched) OR on the effective search_path.

Measured against a throwaway PG 18.3 oracle (port 5599), OIDs resolved from a
`CREATE FUNCTION` battery (`public.udf71(int4,text)`, `public."MyFunc71"(int4)`,
`ragout71.other_func()`, `ragout71."Quoted Other"(int4)`, `public.zero71()`):

| probe | default `search_path` (`"$user", public`) | `search_path=''` | `search_path=ragout71` |
|---|---|---|---|
| `public.udf71` | `udf71(integer,text)` | `public.udf71(integer,text)` | `public.udf71(integer,text)` |
| `public."MyFunc71"` | `"MyFunc71"(integer)` | `public."MyFunc71"(integer)` | `public."MyFunc71"(integer)` |
| `ragout71.other_func` | `ragout71.other_func()` | `ragout71.other_func()` | `other_func()` |
| `ragout71."Quoted Other"` | `ragout71."Quoted Other"(integer)` | `ragout71."Quoted Other"(integer)` | `"Quoted Other"(integer)` |
| `public.zero71` | `zero71()` | `public.zero71()` | — |
| builtin 43 `int4out` | `int4out(integer)` | `int4out(integer)` | — |

## 3. goopg implementation

Three sites:

1. **`catalog.RegprocedureNameParts`** (new, catalog.go) — resolves a pg_proc
   OID to the `(schema, name, arglist)` halves, refactored out of
   `RegprocedureNameAndSchema`. The NAME is returned raw (unquoted,
   unqualified); `format_procedure`'s quoting/qualification is the renderers'
   job since the same parts serve both arms. `RegprocedureName`/
   `RegprocedureNameAndSchema` keep their existing `name(arglist)` output for
   any external callers.

2. **`executor.RegOut` regprocedure arm** (reg_identifier.go) — the shared
   SELECT-wire/COPY-TO renderer now routes a resolved routine through the
   family's `regOutQualified(schema, name, qualify)` and appends the unquoted
   arglist:

   ```go
   if schema, name, arglist, ok := catalog.RegprocedureNameParts(oid, routines); ok {
       return regOutQualified(schema, name, qualify) + "(" + arglist + ")"
   }
   ```

   `regOutQualified` already encodes the upstream rule: `pg_catalog` forces
   `qualify = false` (implicitly visible), an empty schema defaults to
   `public`, then bare `quote_identifier(name)` or `quote_qualified_identifier
   (schema, name)`.

3. **`expr.go` `::regprocedure` cast path** (sibling, Hard-won Rule #2) —
   previously rendered `schema + "." + sig` off-path (whole-signature prefix,
   no name quoting) and bare `sig` on-path. It now uses the same
   `regOutQualified(schema, name, qualify) + "(" + arglist + ")"` form, with
   `qualify = !regObjectSchemaVisible(ctx, schema)` (the cast path's per-object
   visibility check — strictly more precise than the COPY/SELECT proxy flag).
   This fixes the on-path mixed-case case too (`"MyFunc"(integer)` not
   `MyFunc(integer)`) and keeps the two renderers byte-identical.

The `qualify` flag semantics are untouched. The per-object proxy imprecision
(the COPY/SELECT flag is "public visible", not per-object visibility, so a
routine in a non-path non-public schema under a public-inclusive search_path
renders bare on the column path where the cast path — per-object — qualifies)
is the same pre-existing design the 69th/70th slices shipped for every family
member and is out of scope for this row.

## 4. Gates

New unit tests:

- `TestRegOutRegprocedureQualifiesNameOnly` (`internal/executor/
  reg_qualify_test.go`) — user routine in public at qualify=true/false,
  arglist rendering, mixed-case quoted name (`"MyFunc"(integer)` /
  `public."MyFunc"(integer)`), non-public-schema routine
  (`other_schema.other_func()`), builtin never qualified
  (`int4out(integer)` both flags).
- `TestRegprocedureCastQuotesRoutineName` (`internal/executor/
  regoperator_schema_qualify_test.go`) — the sibling cast path: `"MyFunc"` at
  default path → `"MyFunc"(integer)`, empty path → `public."MyFunc"(integer)`;
  a non-public-schema routine qualifies with its schema on BOTH paths.
- `TestRegCopyAndSelectSiblingQualifyAgree` (`internal/server/
  reg_copy_sibling_test.go`) — extended with a user routine so the SELECT and
  COPY paths cannot drift on the fix.

All expected strings match the PG 18.3 oracle table in §2.

Package suites (`internal/executor`, `internal/server`, `internal/catalog`)
PASS; pre-commit units PASS; `TestPort_RegressSuite` PASS (242.3 s);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35).

## 5. Files

- `internal/catalog/catalog.go` — `RegprocedureNameParts` (refactor of
  `RegprocedureNameAndSchema`); `formatProcedureArglist`
- `internal/executor/reg_identifier.go` — RegOut regprocedure arm qualifies
  only the name via `regOutQualified`
- `internal/executor/expr.go` — `::regprocedure` cast path sibling
- `internal/executor/reg_qualify_test.go`, `internal/executor/
  regoperator_schema_qualify_test.go` — new tests
- `internal/server/reg_copy_sibling_test.go` — sibling-paths pin
