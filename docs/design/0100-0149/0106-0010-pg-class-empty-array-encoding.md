# 0106-0010 — pg_class empty array (relacl/reloptions) encoding

**Status:** accepted
**Date:** 2026-05-17
**Milestone:** M0106 (sub-milestone M0106-0010, step 1)
**Upstream reference:** `postgres/src/include/utils/array.h` (`ArrayType`),
`postgres/src/backend/utils/adt/arrayfuncs.c` (`deconstruct_array`),
`postgres/src/backend/utils/adt/arrayutils.c` (`construct_empty_array`)

## Problem

After M0106-0009 cleared the `nocachegetattr` slow-path assertion, a
vanilla PG backend booted from a goopg-produced data directory
crashes one stage later. The next call site —
`SearchSysCache1(AMOID, 403)` reaching into a cached pg_class tuple —
runs `extractRelOptions` / aclitem-walking helpers that cast the raw
`relacl` (attnum 32) and `reloptions` (attnum 33) datums as
`ArrayType*` and then assert
`ARR_ELEMTYPE(array) == elmtype` at `arrayfuncs.c:3644`.

The goopg bootstrap was writing a text varlena holding the two ASCII
bytes `{}` into those slots (`encodeValuePG`'s default branch). PG
re-interprets the same 4 bytes starting at the column offset as
`vl_len_ | ndim`, and the bytes after that as
`dataoffset | elemtype`. The values are garbage, so the assertion
trips on the first nailed pg_class tuple PG opens after init.

## Why a text varlena is wrong here

`relacl` and `reloptions` are not text columns in PG. The catalog
declares them as `aclitem[]` (OID 1034) and `text[]` (OID 1009);
PG's relcache and catcache read them through `heap_getattr` and
treat the returned `Datum` as an `ArrayType*`. The `extractRelOptions`
path is unconditional during `RelationInitIndexAccessInfo`, so even
the simplest catalog open is enough to trigger the assertion.

The init-file `pgClassAttrs()` (built in M0106-0008) already declares
`atttypid=1034` for `relacl` and `atttypid=1009` for `reloptions`,
but `pgClassColDefs()` (the heap-tuple writer side) was declaring
both columns as `text`. The two halves of M0106-0008/0009 had to
agree before the round trip became sound.

## Decision

Encode the empty values as the **binary `ArrayType` blob** that
`construct_empty_array(elmtype)` produces, not as a text varlena.
Empty in this context means "no ACL entries"; this is the same
in-memory shape PG would build at runtime if asked for the empty
array of that element type. The encoding is 16 bytes:

| offset | size | field         | value                          |
|--------|------|---------------|--------------------------------|
| 0      | 4    | `vl_len_`     | LE `(16 << 2)` (uncompressed)  |
| 4      | 4    | `ndim`        | 0 (LE)                          |
| 8      | 4    | `dataoffset`  | 0 (LE)                          |
| 12     | 4    | `elemtype`    | 1033 / 25 / 26 / 21 (LE)        |

PG's `VARSIZE_ANY` matches the `xxxxxx00` low-2-bits pattern of byte 0
and reads the full 16-byte size. `ARR_NDIM=0` short-circuits
`deconstruct_array`'s element-count math, and `ARR_ELEMTYPE` returns
exactly the OID the caller expected, so the assertion passes.

`relpartbound` (`pg_node_tree`, OID 194) is **not** an array — PG
only reads it when `relispartition = true`, which is false for every
nailed relation. A 1-byte empty varlena is sufficient there.

### Alternatives considered

- **NULL via null bitmap.** PG itself stores `relacl = NULL` when
  there are no explicit grants, so a null-bitmap encoding would be
  the most upstream-faithful answer. We rejected it for this loop
  because goopg's heap-tuple writer does not yet thread a
  bitmap-aware `t_hoff` through `NewHeapTuple`, and adding that
  plumbing risks regressing M0106-0008's GETSTRUCT contract. The
  binary empty-array encoding is byte-equivalent to what PG would
  see after a `construct_empty_array` followed by a heap insert
  with `t_isnull[relacl] = false`, so it is functionally correct
  for the cache paths we care about.
- **Change init-file `pgClassAttrs()` to advertise `text`.** This
  would let the text varlena round-trip, but it lies to PG about
  the catalog's true schema and would defeat any future code that
  actually iterates `relacl` (e.g. ACL enforcement on a goopg
  backup that PG promotes).

## Implementation

### Encoder (`internal/executor/codec.go`)

- New `emptyArrayTypeBytes(elemType uint32) []byte` — 16-byte
  PG-native serialisation matching `construct_empty_array`.
- New `varlenaTextBytes(s string) []byte` — extracted from the
  default branch of `encodeValuePG` so the existing text path and
  the new `pg_node_tree` path share one implementation.
- `encodeValuePG` gains explicit cases:
  - `"aclitem[]"`, `"_aclitem"` → `emptyArrayTypeBytes(1033)`
  - `"text[]"`, `"_text"` → `emptyArrayTypeBytes(25)`
  - `"oid[]"`, `"_oid"` → `emptyArrayTypeBytes(26)` (forward-compat)
  - `"int2[]"`, `"_int2"` → `emptyArrayTypeBytes(21)` (forward-compat)
  - `"pg_node_tree"` → `varlenaTextBytes(d.StringValue())`
- `physicalPGTypeAlign` returns 4 for all of the above plus
  `"anyarray"`. `ArrayType`'s leading members are `int32`, so the
  column must land on a 4-byte boundary; on the goopg layout the
  144-byte fixed prefix is already 4-aligned, so no padding bytes
  are introduced.

### Catalog metadata (`internal/initdb/initdb.go`)

- `pgClassColDefs()` now declares the three trailing varlena
  columns with their PG types (`aclitem[]`, `text[]`,
  `pg_node_tree`) instead of `text`.
- `pgCatalogTypeOID`, `pgCatalogTypeLen`, `pgTypeAlignChar`,
  `pgTypeStorageChar` learn the new OIDs 194 / 1009 / 1034 / 2277
  so that the on-disk `pg_attribute` rows (atttypid / attlen /
  attalign / attstorage) agree with the init-file TupleDesc.
- `pgAttrEntriesForRel` derives `NotNull` from
  `pgCatalogTypeLen != -1` rather than `Type.Name != "text"`; the
  prior check would have flagged `aclitem[]` as `NOT NULL`, which
  is wrong both for PG semantics and for any future null-bitmap
  rollout of M0106-0010 step 2.
- `internal/initdb/relcache_init.go::pgClassAttrs` switches
  `relpartbound.TypeOID` from 25 (text) to 194 (pg_node_tree) so
  the init file's TupleDesc matches the on-disk heap-tuple schema.

## Risk and blast radius

- The change only affects pg_class / pg_attribute bootstrap and the
  PG-native (`encodeRowPG`) encoder. goopg-internal storage uses
  `encodeRow` (the v0 flag-byte format) and is untouched.
- The `physicalPGTypeAlign` default already returned 4 for all
  unknown types; the explicit cases make the intent obvious and
  guard against any future change to that default.
- The text→ArrayType swap is a one-way upgrade: existing data
  directories created before this commit will continue to read
  back via `decodeValuePG`'s text path (the legacy data is still
  there), but a PG backend reading those directories would still
  trip the original assertion — i.e. data created before
  M0106-0010 cannot serve a PG standby. That matches the design
  doc's "M0106 must be reapplied on every bootstrap" stance.

## Regression coverage

- `internal/executor/codec_empty_array_test.go`
  - `TestEmptyArrayTypeBytesShape` (4 elem types) pins the 16-byte
    `vl_len_ | ndim | dataoffset | elemtype` layout and the
    `xxxxxx00` low-2-bits invariant.
  - `TestEncodeValuePGAclItemArrayEmitsEmptyArrayType` (4 type-name
    aliases) verifies that the encoder ignores any caller-supplied
    Datum payload and always emits the binary empty array.
  - `TestPhysicalPGTypeAlignArrayTypes` pins the 4-byte alignment
    for every array / pg_node_tree alias.
- `internal/initdb/pg_class_empty_array_test.go`
  - `TestPgClassRelaclReloptionsEncodedAsBinaryArrayType` re-encodes
    a synthetic nailed relation through the same
    `pgClassColDefs` + `pgClassRow` + `executor.EncodeRowPG` path
    that `bootstrapPgClassTuples` uses and walks the row to confirm
    the relacl / reloptions slots carry a valid `ArrayType` with
    the correct element OID.

## Follow-ups (deferred to subsequent loops)

- **pg_am bootstrap (M0106-0010 step 2):** after this commit
  unblocks `extractRelOptions`, the next PG call site is
  `SearchSysCache1(AMOID, 403)`. That requires a btree
  `pg_am` heap tuple (OID 403, amname `btree`, amhandler 330,
  amtype `i`) plus likely `pg_opclass` / `pg_amop` / `pg_amproc`
  rows. Tracked in M0106-0010 step 2.
- **pg_attribute varlena columns.** `attacl` / `attoptions` /
  `attfdwoptions` / `attmissingval` are still encoded as text
  varlena in `pgAttributeRow`. PG appears to leave them alone
  during the initial relcache load, but a defensive port of the
  same treatment is filed for the next loop.
- **DDL maintenance (M0106-0011).** None of this is dynamic; a
  later CREATE TABLE must keep the same encoding rules.
