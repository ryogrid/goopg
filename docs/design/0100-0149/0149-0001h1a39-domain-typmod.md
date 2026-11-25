# M0149\-0001h1a39 — a domain's typmod applies to its columns and casts

## Problem

`CREATE DOMAIN d AS varchar(5)` recorded the typmod on `Domain.Base`, and it reached
`pg_type.typtypmod`, but nothing applied it. A table column of the domain was stored as
plain `varchar`, and a cast to the domain ignored the length, so `'123456'` was accepted
where PG 18.3 raises `value too long for type character varying(5)`.

## What PG does

`getBaseTypeAndTypmod` walks a domain to its base type and the typmod declared there.
Assignment raises on an over\-long value. An explicit cast to the domain converts to the
base type and then applies the length coercion, so it truncates or rounds.
`pg_attribute.atttypmod` of a domain column stays \-1, because the column's type is the domain.

## Design

- `catalog.InMemory.DomainBaseType` follows a chain of domains to the base type and its
  arguments.
- `resolveDomainColumnType` gives a column's storage type: the base type with the domain's
  typmod, plus the domain name for `DeclaredTypeName`. CREATE TABLE, ALTER TABLE ADD COLUMN
  and ALTER COLUMN TYPE all use it. The last two did not resolve domains before.
- `domainCastWithTypmod` turns a cast to such a domain into input to the base type followed
  by an explicit length cast, then runs the domain's `VALUE IN` checks. Target names that are
  built\-in types return before any catalog lookup.

## ALTER COLUMN TYPE

The rewrite truncated the heap before encoding the converted rows, so a value the new typmod
rejected failed after the rows were gone. Phase 1 now encodes each converted value, and a
rejected value fails with the table untouched. The statement also replaces the column's
`pg_attribute` row, which used to keep the old type.

## Not covered

- After a restart, columns of user\-defined types reload as `text` \(P0\-E25\), so a domain
  column stops enforcing its typmod until that is fixed.
- `timestamp(p)` columns and casts are not rounded \(M0149\-0001h1a45\).
- Array elements and CREATE TABLE AS columns ignore their typmod \(M0149\-0001h1a46\).
- Other domain differences are listed in M0149\-0001h1a47.
