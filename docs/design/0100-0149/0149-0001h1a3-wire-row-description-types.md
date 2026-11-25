# M0149\-0001h1a3 — result column types in RowDescription

## Problem

`typeOIDFor` \(`internal/postmaster/dispatch.go`\) listed about thirty type
names and answered text \(25\) for everything else, so bytea, void, json, xid,
the network and geometric types and every user\-defined type reached the client
as text. The extended\-protocol paths also ignored `Type.IsArray`, so an array
column was reported as its element type there.

## What PG does

`SendRowDescriptionMessage` \(`printtup.c`\) sends the column's pg\_type OID, after
`getBaseTypeAndTypmod` has replaced a domain with its base type. An array of a
domain is a type of its own and is sent as such.

## Design

`wireTypeOID(cat, dbOid, t)` in `internal/postmaster/wire_type_oid.go` is the one
source of the field's OID. The simple\-query, FETCH, extended Execute and Describe
paths all call it.

1. Array spellings \(`IsArray`, `int4[]`, `_int4`\) are reduced to the element.
2. Built\-in names resolve through `typeOIDFor`, `catalog.TypeNameToOID`, then the
   captured pg\_catalog `format_type` names.
3. Other names resolve against the connection database's enums, domains,
   composites, ranges, multiranges and relation rowtypes.
4. The array OID comes from the element's array type.

The planner's `unknown` and the polymorphic `any*` pseudo\-types stay text: PG
resolves them before a result column exists.

Functions whose every pg\_proc overload returns void are typed void in the
planner's `exprType`, so `select pg_sleep(0)` reports void.

Binary result formats are still refused at Bind \(0A000\), so a corrected OID
never changes how a value is encoded.

## Verification

A protocol probe compared each column's reported type with PG 18.3 for about 60 table
column types and a set of expressions. Differences left are the planner's or the
catalog stubs' types, not this layer's; they are filed as M0149\-0001h1a38.

## Type length and typmod \(M0149\-0001h1a38\)

`wireFieldDescription` sends the OID, typlen and typmod together. An array reports
length \-1 and keeps its element's typmod; a domain reports its base type's values.

The typmod comes from `catalog.Type.Args`, so the planner's `exprType` has to follow
PG's `exprTypmod` \(`nodeFuncs.c`\):

- a length\-coercing cast has the cast's typmod \(`castTypmodArgs` decodes
  `encodeTypmod`; a bare interval precision is packed as `INTERVAL_TYPMOD(p, full range)`\);
- min, max, sum, string\_agg, NULLIF, unary operators and rounding functions have none;
- CASE, COALESCE, GREATEST and LEAST keep a typmod only when every input has it.

`typeWithoutTypmod` renames `char` with arguments to `bpchar`, because `char` with no
arguments means the one\-byte type.

This also reaches column creation from a query, so views and CREATE TABLE AS keep the
declared length, and EXPLAIN widths use it. Plan shapes did not change.

## Not covered

- The table OID and column number are still 0 \(M0149\-0001h1a40\).
- A domain's typmod is not stored, so it is neither reported nor enforced
  \(M0149\-0001h1a39\).
- The remaining result type and label differences are M0149\-0001h1a41.
