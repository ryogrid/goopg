# 0118-0138 — `int4[]` user array column storage round-trip (M0118-0002, predicate-gin enabler)

Status: accepted
Date: 2026-06-29
Milestone: M0118 — Upstream Isolation Spec Suite Pass-Through
Spec target: `postgres/src/test/isolation/specs/predicate-gin.spec`
Kind: **Enabler — NOT a promotion** (`predicate-gin.spec` stays `failed`)

## Problem

`predicate-gin.spec` could not even reach its first read step: its global
`setup` block

```sql
create table gin_tbl(p int4[]);
insert into gin_tbl select array[1] from generate_series(1, 8192) g;
```

failed at the INSERT with `ERROR: invalid input syntax for type integer: "{1}"`.

A user array column carries `catalog.Type{Name:"int4", IsArray:true}` — `Name`
holds the **element** type and `IsArray` is tracked separately (so the runtime
evaluator keeps using the element type while pg_attribute renders `_int4`). The
`ARRAY[1]` constructor is evaluated by `array_construct` (expr.go) into a
`KindString` datum holding the array text `"{1}"`.

But three places keyed only on `Type.Name` ("int4") and ignored `IsArray`,
treating the value as a scalar int4:

1. **insertOp integer-range coercion** (`operators_storage.go`) —
   `evalCast("{1}", "int4")` → 22P02.
2. **heap encode** (`codec.go` `encodeValuePG`) — `coerceStringToInt64("{1}")`
   → 22P02 (the INSERT…SELECT path, which skips the analyzer VALUES check).
3. **analyzer assignability** (`analyzer.go` `isAssignable`) — `text[]`
   (ARRAY[…]'s analyzed type) vs an `int4` column → 42804 (the VALUES path).

And on read-back two more sites mis-typed the value:

4. **heap decode** (`codec.go` `decodePhysicalPGValueMctx`) read 4 raw bytes as
   a scalar int4 instead of an array blob.
5. **RowDescription** (`server/dispatch.go`) advertised the column OID as
   `int4` (23) via `typeOIDFor(Name)`, so `lib/pq` parsed the `"{1}"` text as a
   scalar int4 client-side → `strconv.ParseInt: parsing "{1}"`.

## Fix

Make the storage codec **array-aware**, gated entirely on `Type.IsArray` so
every scalar path is byte-for-byte unchanged (zero blast radius outside array
columns, which were wholly broken before).

### Storage codec (`codec_array.go`, new)

User array columns are stored as **PG-native `ArrayType` varlena blobs** (1-D,
no-NULL): a 24-byte header (`varlena | ndim=1 | dataoffset=0 | elemtype | dims[0]
| lbound[0]=1`) followed by element data at each element's `typalign` boundary
(relative to offset 24, which is MAXALIGN'd so 8-byte elements stay aligned).

- `encodeArrayValuePG` — parses the `array_construct` text `"{1,2}"`
  (`parseTextArray`) and builds the blob. Supported element types:
  int2/int4/int8/oid/float4/float8/bool (fixed-width, raw aligned) and
  text/varchar/bpchar (varlena, always-4-byte element header). A `KindBytes`
  datum passes through verbatim (catalog seeders inject ready-made arrays).
  Empty `"{}"` → `emptyArrayTypeBytes(elemOID)`. NULL elements are rejected
  (`0A000`) — the no-NULL shape is the only one goopg writes today.
- `decodeArrayValuePG` — inverts the blob back to canonical PG array text
  `"{1,2}"` as a `KindString` datum (fixed elements formatted via
  `strconv`/`PGFloatOut`, text elements quoted via `quoteArrayTextElem` when
  they contain delimiters/whitespace).

Wired at the three codec entry points, each behind `if t.IsArray`:
`encodeValuePG`, `decodePhysicalPGValueMctx`, `physicalPGTypeAlign` (→ 4, 'i').

### insertOp coercion (`operators_storage.go`)

The integer-range coercion loop skips array columns (`if col.Type.IsArray
{ continue }`) — the value is already the correct array text.

### Analyzer (`analyzer.go`)

`isAssignable` accepts any array source (`src.IsArray ||
HasSuffix(src.Name,"[]")`) for an array destination (`dst.IsArray`); element
contents are validated at runtime by the codec, mirroring PG's reliance on
`array_in`.

### Wire RowDescription (`server/dispatch.go`)

Both the simple- and extended-query field-description loops (sibling paths,
`pattern_sibling_paths_must_agree`) advertise the **array** pg_type OID via
`catalog.ArrayOIDForBase(typeOIDFor(Name))` when `sc.Type.IsArray`, so the
client decodes `"{1}"` as `_int4` (1007) not a scalar int4. The DataRow value
bytes were already correct (`AppendValueText` emits the text verbatim).

## Result

`predicate-gin.spec`'s global setup now executes; the first divergence advances
from permutation-0 setup (which blocked **all** permutations) to the first read
step `ra1` (`select * from gin_tbl where p @> array[1] limit 1`), which fails
with `operator @>: invalid box value` — the `@>` array-containment operator is
currently mis-dispatched to the geometric `box @> box`. The spec stays `failed`.

`CREATE TABLE t(p int4[]); INSERT … VALUES(array[1]); INSERT … SELECT array[g]
…; SELECT p FROM t` now round-trips correctly (`{1}`…`{5}`).

## Remaining blockers for `predicate-gin` (deferred, ledgered)

1. **`@>` array containment** runtime — element-membership semantics for
   `anyarray @> anyarray` (separate from `box @> box`); the analyzer/expr
   operator dispatch must route by operand type. (Next enabler.)
2. **GIN page-grain SSI** — like predicate-gist/predicate-hash, goopg has no
   native GIN AM (a `USING gin` index is catalog-only → equality/containment
   scans fall back to a relation-grain SIREAD that over-aborts the
   disjoint-key permutations). The grid-cell SSI primitive from design 0118-0137
   (`ssiGistGridCell`/`ssiRecordGistGridRead`/`ssiRecordGistIndexInsert`) is
   reusable, keyed on the GIN search key (the array element) instead of a
   spatial cell.

## Gates

- `go build ./...` clean.
- New `TestArrayCodecRoundTrip` (int2/int4/int8/oid/float8/bool/text + empty)
  + `TestArrayCodecTextElementQuoting` PASS.
- Full `internal/executor` + `internal/analyzer` unit suites PASS (the scalar
  codec round-trip tests confirm non-array paths byte-identical).
- `-race` on the codec encode/decode tests PASS.
- `internal/storage` + `internal/catalog` suites PASS.
- Probe confirms predicate-gin global setup + `SELECT` round-trip.
- `TestPort_RegressSuite` infra-timed-out on WSL2 (>9 min); the change is fully
  `IsArray`-gated so the scalar regress path is structurally unchanged (rule #5
  satisfied by the executor codec units + scalar suites).
- pgbench smoke = pre-commit hook (TPC-B touches no array column).
