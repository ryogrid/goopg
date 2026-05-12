# Design: Enum and Domain Types (M0097-0017)

**Status**: accepted  
**Milestone**: M0097-0017 — Extended type parity  
**Target tests**: `enum`, `domain`

## Problem

The `enum` and `domain` regress tests currently fail at parse time because
goopg has no `CREATE TYPE … AS ENUM` or `CREATE DOMAIN` parser branch.
Every subsequent statement that references these types then produces errors
or wrong answers.

## Scope

This document covers:
1. `CREATE TYPE name AS ENUM (val1, val2, …)` — user-defined enum types
2. `ALTER TYPE name ADD VALUE [IF NOT EXISTS] val [BEFORE|AFTER ref]` — enum mutations
3. `DROP TYPE name [CASCADE|RESTRICT]` — type removal
4. `CREATE DOMAIN name [AS] base_type [constraints]` — domain alias types
5. `DROP DOMAIN name [CASCADE|RESTRICT]` — domain removal
6. `pg_enum` virtual catalog view
7. Column type resolution: enum/domain columns stored as base type
8. Cast support: `'val'::enumtype`, `val::domaintype`
9. Helper functions: `enum_first`, `enum_last`, `enum_range`, `pg_input_is_valid`

Out of scope (v0):
- Composite types (`CREATE TYPE … AS (col type, …)`) — needed for rowtypes.sql
- Range types (`CREATE TYPE … AS RANGE`) — separate milestone
- Full enum constraint enforcement beyond label validation
- Domain CHECK constraint evaluation (parsed, not enforced)
- ALTER TYPE RENAME VALUE, RENAME TO
- CAST catalog entries

## Design

### Storage model

**Enums**: stored as text at rest. The enum label is the canonical value.
Comparison uses the sort order (position in the original definition list).
The catalog tracks the ordered label list; the btree codec treats enum
columns as text.

**Domains**: transparent alias for the base type. A column declared as
`domainint4` stores and behaves identically to `int4`. The domain registry
maps name → base type so the column type resolver can substitute.

### Catalog additions

```go
// EnumType holds one user-defined enum type.
type EnumType struct {
    Name   string
    OID    uint32
    Values []string  // ordered, position = sortorder
}

// Domain holds one user-defined domain type.
type Domain struct {
    Name    string
    OID     uint32
    Base    Type    // resolved base type (recurse through domain chains)
    NotNull bool
}
```

`InMemory` gets two new maps: `enumTypes map[string]*EnumType` and
`domains map[string]*Domain`.

### pg_enum virtual view

Columns: `enumtypid text, enumsortorder int, enumlabel text`  
`enumtypid` stores the enum type name (not a numeric OID) so that
`'rainbow'::regtype` (which returns the name unchanged in v0) matches.

### Column type resolution

`execCreateTable` already resolves column types. After the existing type
switch, an additional fallback looks up the type name in `enumTypes` (→
text) and `domains` (→ recursively resolved base type).

### Cast evaluation

`evalTypedStringLit` gains a fallback at the end of the switch: if the
type name is a known enum, validate the label and return it as a text
datum. If the type name is a known domain, recurse with the base type.

`evalFuncCall` case `"regtype"` returns the argument as-is (already done).

### Planner routing

`CreateTypeStmt`, `AlterTypeStmt`, `DropTypeStmt`, `CreateDomainStmt`,
`DropDomainStmt` are routed through the DDL pass-through: `planner.DDL{Stmt: s}`.

## Files changed

| File | Change |
|------|--------|
| `internal/parser/ast.go` | CreateTypeStmt, AlterTypeStmt, DropTypeStmt, CreateDomainStmt, DropDomainStmt |
| `internal/parser/ddl.go` | parseCreateType, parseAlterType, parseDropType, parseCreateDomain, parseDropDomain |
| `internal/parser/parser.go` | Dispatch for CREATE TYPE, ALTER TYPE, DROP TYPE, DROP DOMAIN |
| `internal/catalog/catalog.go` | EnumType, Domain structs; enumTypes/domains maps; Register/Lookup/Drop methods; pg_enum virtual table |
| `internal/planner/planner.go` | Route new stmt types through DDL |
| `internal/executor/operators_ddl.go` | execCreateType, execAlterType, execDropType, execCreateDomain, execDropDomain |
| `internal/executor/expr.go` | enum cast fallback in evalTypedStringLit; enum_first, enum_last, enum_range stubs |
