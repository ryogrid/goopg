---
status: accepted
date: 2026-08-14
supersedes: none
milestone: M0119-0006 (70th slice)
---

# regcollation qualifies a user collation with its actual schema

Closes deferral ledger row 1339 (the 69th slice's carry-forward). The
regcollation arm of `executor.RegOut` schema-qualified a user collation with
the hardcoded qualifier `"public"`; upstream `regcollationout` qualifies with
the collation's ACTUAL namespace.

## 1. Problem

The 69th slice (`0119-0006-regout-schema-qualification.md`) gave the reg*out
family a shared `regOutQualified(schema, name, qualify)` rule and ran the
regclass/regproc/regrole arms' resolved names through it. The regcollation arm
was the one member that did NOT: for a user collation it returned
`quoteQualifiedIdentifier("public", n)` — correct only because goopg creates
user collations in the current schema, which is `public` for a default
session. A `CREATE COLLATION` in any non-public schema rendered
`public.<name>` where PG 18.3 renders `<actual schema>.<name>`.

## 2. The upstream rule

`regcollationout` (regproc.c:1085) resolves the OID via the COLLOID syscache
and renders `quote_qualified_identifier(nspname, collationname)` — always. The
qualifier is the collation's OWN namespace when `CollationIsVisible` is false:

```c
if (CollationIsVisible(collationid))
    nspname = NULL;                       /* visible → bare quoted name */
else
    nspname = get_namespace_name(collationform->collnamespace);
result = quote_qualified_identifier(nspname, collationname);
```

So the qualifier is `get_namespace_name(collnamespace)` — the actual schema —
not the schema the collation happened to be created in (which for a
schema-qualified `CREATE COLLATION other_schema.mycoll` is `other_schema`).

Measured against a throwaway PG 18.3 oracle (port 5599), `CREATE COLLATION
ragout70.mycoll` and `ragout70."My Other Coll"`:

| probe | PG 18.3 |
|---|---|
| `SET search_path=''`; `oid::regcollation` of `ragout70.mycoll` | `ragout70.mycoll` |
| `SET search_path=''`; `oid::regcollation` of `ragout70."My Other Coll"` | `ragout70."My Other Coll"` |
| `SET search_path=ragout70`; `oid::regcollation` of `ragout70.mycoll` | `mycoll` |

## 3. goopg implementation

One arm in `internal/executor/reg_identifier.go`. The regcollation arm now
iterates `ListUserCollations` UNCONDITIONALLY (the old code only entered the
loop when `qualify` was true, falling through to the bare name otherwise — the
new code reaches the same bare name through `regOutQualified`'s `!qualify`
arm, so qualify=false behavior is unchanged):

```go
for _, uc := range im.ListUserCollations() {
    if uc.OID == oid {
        return regOutQualified(im.SchemaNameForOID(uc.NamespaceOID), n, qualify)
    }
}
// builtin collation (pg_catalog, always visible): bare quoted name
return pgQuoteIdent(n)
```

`im.SchemaNameForOID(uc.NamespaceOID)` resolves the collation's `collnamespace`
to its name (the `get_namespace_name` port); `regOutQualified` then applies the
family's shared rule, which also covers the two edges the hardcoded literal
could not:

- a user collation created in `pg_catalog` (superuser, rare) forces
  `qualify = false` there — `CollationIsVisible` is always true for pg_catalog,
  so the bare quoted name is the upstream answer (the old code would have
  emitted `public.<name>`);
- an unresolvable `NamespaceOID` (impossible in practice — `CreateCollation`
  always resolves or public-fallbacks to a non-zero OID) defaults to `public`,
  degrading to the old behavior.

The `qualify` flag semantics are untouched: COPY computes
`!regObjectSchemaVisible(ctx, "public")`, SELECT `!publicSchemaVisible(
getSetting)`. The remaining proxy imprecision — qualify is "public visible",
not per-object visibility, so a collation in a schema that IS on the path while
public is not renders over-qualified — is the same pre-existing design the 69th
slice shipped for every family member and is out of scope for this row.

## 4. Gates

New unit test `TestRegCollationQualifiesWithActualSchema`
(`internal/executor/reg_qualify_test.go`): `CREATE COLLATION
other_schema.othercoll` (after `RegisterSchema("other_schema")`, what CREATE
SCHEMA's DDL operator calls) renders `other_schema.othercoll` at qualify=true,
`CREATE COLLATION quote_schema."My Other Coll"` renders
`quote_schema."My Other Coll"` at qualify=true, qualify=false keeps the bare
`othercoll`, and the public-schema `mycoll` still renders `public.mycoll`.
Extended `TestRegCopyAndSelectSiblingQualifyAgree`
(`internal/server/reg_copy_sibling_test.go`) with the non-public collation so
the SELECT and COPY paths cannot drift on the fix. All expected strings match
the PG 18.3 oracle table in §2.

Package suites (`internal/executor`, `internal/server`) PASS; pre-commit units
PASS; `TestPort_RegressSuite` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35).

## 5. Files

- `internal/executor/reg_identifier.go` — regcollation arm qualifies with
  `SchemaNameForOID(uc.NamespaceOID)`
- `internal/executor/reg_qualify_test.go` — `TestRegCollationQualifiesWithActualSchema`
- `internal/server/reg_copy_sibling_test.go` — sibling-paths pin
