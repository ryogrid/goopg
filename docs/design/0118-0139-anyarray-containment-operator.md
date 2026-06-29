# 0118-0139 — `anyarray` containment / overlap operators (`@>` `<@` `&&`) (M0118-0002, predicate-gin enabler)

Status: accepted
Date: 2026-06-29
Milestone: M0118 — Upstream Isolation Spec Suite Pass-Through
Spec target: `postgres/src/test/isolation/specs/predicate-gin.spec`
Kind: **Enabler — NOT a promotion** (`predicate-gin.spec` stays `failed`)

## Problem

With array-column storage in place (design [0118-0138](0118-0138-array-column-storage-roundtrip.md)),
`predicate-gin.spec` reached its first read step

```sql
step ra1 { select * from gin_tbl where p @> array[1] limit 1; }
```

and failed with `ERROR: operator @>: invalid box value`.

`@>` (and its siblings `<@` and `&&`) were implemented in `expr.go` only as the
**geometric box** operators (`box @> box`, design 0118-0023): the handler
unconditionally called `parseBoxText` on both operands. When the operands are
array literals (`{1}`, `{1,2,3}`) `parseBoxText` fails and raises
`invalid box value`.

In upstream PostgreSQL these operator symbols are overloaded by operand type:
the parser/planner resolves `anyarray @> anyarray` to
`arraycontains`/`arraycontained`/`arrayoverlap`
(`src/backend/utils/adt/arrayfuncs.c`), distinct from the geometric
`box_contain`/`box_contained`/`box_overlap`.

## Fix

goopg carries a user array value as a `KindString` datum holding the canonical
PG array text (`{1,2,3}`). At the `OpContainedBy`/`OpContains`/`OpOverlap` case
in `evalBinaryOp` (expr.go), **before** attempting box parsing, detect when both
operands are array literals (`isArrayLiteralText`: `{`…`}`) and route to
set-membership semantics:

- `a @> b` (`OpContains`)    — every element of `b` is present in `a`
- `a <@ b` (`OpContainedBy`) — every element of `a` is present in `b`
- `a && b` (`OpOverlap`)     — `a` and `b` share at least one element

Elements are split with the existing `parseTextArray` (handles quoting/escaping)
and compared by canonical text. This is exact for the scalar element types
goopg stores in array columns (int2/4/8, oid, float4/8, bool, text/varchar):
both operands arrive in PG's canonical output form, so `"1" == "1"`. An empty
`{}` is trivially contained (`'{}' <@ anything` is true), matching PG.

NULL operands are already short-circuited to `NULL` earlier in `evalBinaryOp`,
so the helpers only see non-NULL array text.

### Touched code

- `internal/executor/expr.go`
  - `evalBinaryOp` `OpContainedBy`/`OpContains`/`OpOverlap` case: array-operand
    fast path before box parsing.
  - new helpers `isArrayLiteralText`, `evalArraySetOp`, `arrayElemsSubset`.
- `internal/executor/codec_array_test.go` — `TestArraySetOps` covers @>/<@/&&
  for present/absent/empty/text-with-quotes cases.

## Result

`predicate-gin`'s first divergence advances from read step `ra1`
(`invalid box value`) to a genuine SSI granularity divergence: a **disjoint**
permutation such as `ra1 ro2 wo1 c1 wb2 c2` (read key 1, insert key 2) now
returns the correct rows but goopg over-aborts the writer with 40001. goopg has
no native GIN AM (`USING gin` is catalog-only), so `p @> array[K]` runs as a
seq scan that takes a **relation-grain** SIREAD, conflicting with every index
insert regardless of key. The spec stays `failed`.

## Next step (separate loop)

GIN key-grain SSI: reuse the grid-cell predicate-lock primitive from design
[0118-0137](0118-0137-predicate-gist-grid-cell-ssi.md) keyed on the GIN search
key (the array element value) instead of a spatial cell —
`ssiRecordGinKeyRead` on the scan path for each searched key, a
`ssiRecordGinIndexInsert` twin per inserted element, plus the `fastupdate = on`
case where the whole index is under one predicate lock (a single sentinel key).
See memory `[[goopg_gist_grid_cell_ssi]]` and `[[goopg_hash_index_ssi_bucket_locking]]`.

## Scope / blast radius

Zero outside array operands: the new branch fires only when **both** operands
are `{`…`}` array literals; box, range, and JSON containment paths are
byte-identical. Geometric `box @> box` continues through `parseBoxText`
unchanged (a box renders as `(x1,y1),(x2,y2)`, never `{`…`}`).
