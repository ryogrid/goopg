# M0106-0008: pg_class/pg_attribute Heap Tuple Bootstrap — Handover

2026-05-17 session, branch `ralph-period-from-0511`.

## What This Is About

Vanilla PG standby bootstrapped from a goopg basebackup must start, stream
WAL, and serve read-only queries. PG's `RelationBuildDesc` (called by
`load_critical_index` during backend startup) reads **actual pg_class/pg_attribute
heap tuples** — it bypasses the relcache init file entirely. Without those
tuples, PG PANICs on "could not open critical system index".

## Achieved

### 1. pg_class heap tuples (`base/{1,5}/1259`)
- Encode full PG18 `FormData_pg_class` byte layout: 31 fixed + 3 varlena columns
- PG casts raw tuple bytes directly as `FormData_pg_class*` via `GETSTRUCT`, so
  every field must be at the correct struct offset
- Handles multi-page files (47 nailed relations × ~145 bytes ≈ 7 KB → 1 page)

### 2. pg_attribute heap tuples (`base/{1,5}/1249`)
- One row per column per nailed relation (~264 rows, ~4 pages)
- Uses `pgAttrEntriesForRel` which derives attributes from column definitions
  for pg_class/pg_attribute themselves, and from `rel.Attrs` for other relations

### 3. Init file fixes (relcache_init.go)
- `buildPgClassBlob`: corrected all field offsets to PG18 struct layout
  (relpersistence at 118, relkind at 119, relnatts at 120, added relallfrozen
  at 108, relreplident at 130, etc.)
- `buildPgAttributeBlob`: `attalign` now computed from type length instead of
  hardcoded `'i'` — critical for TupleDesc physical offset computation
- `indexKeyAttrs`: set `Len=4, TypeOID=26, NotNull=true` (was all-zero)
  — `attlen=0` crashes PG's `att_addlength_pointer`
- `pgClassAttrs()`: expanded from 14 to 34 columns matching PG18 struct

### 4. encodeValuePG additions (codec.go)
- Added `char`, `float4`, `float8`, `xid` type encoding for PG-native format
- Added `math` import, `physicalPGTypeAlign` handles `char`/`name`/`xid`

### 5. HEAP_HASVARWIDTH flag
- `hasVarWidthCol()` helper checks for "text" columns
- `writeMultiPageHeap` / `writeMultiPageHeapRows` set `HeapHasVarWidth` in
  infomask — without this, PG's `nocachegetattr` fast path runs on tuples
  containing varlena columns and asserts

## Current Blocker

**`TRAP: failed Assert("false"), File: "heaptuple.c", Line: 705`**

- Occurs in `nocachegetattr` slow path at `att_addlength_pointer`
- Debug logging shows: `i=31 attlen=-1 attnum=32 natts=34`
- PG is accessing attnum 32 (relacl, first varlena column, `attlen=-1`)
- The assertion is inside the `att_addlength_pointer` macro which does:
  ```
  (attlen > 0) ? off + attlen :
    (attlen == -1) ? off + VARSIZE_ANY(attptr) :
      AssertMacro(attlen == -2), off + strlen(attptr) + 1
  ```
- For `attlen=-1`, the second branch (`VARSIZE_ANY`) should be taken
- The fact that the assertion fires suggests either:
  - An unexpected `attlen` value reaching the third branch
  - A varlena header byte at the computed offset being invalid (not a valid
    1B or 4B header), causing `VARSIZE_ANY` or a related assertion to fire
  - The `off` value pointing past the tuple data

### Stack trace context
```
RelationInitIndexAccessInfo → SearchSysCache1(AMOID, 403)
→ table_open(pg_am) → relation_open → RelationIdGetRelation
→ extractRelOptions → nocachegetattr (ASSERT)
```

The catcache is trying to read `relacl` (attnum 32) from a cached pg_class
tuple. The tuple was stored in the catcache using the TupleDesc from the
init file (34 columns). The varlena data at offset 144 should be `0x01`
(1-byte varlena header for empty string).

### Hypothesis
PG's `VARSIZE_ANY` or `att_addlength_pointer` assertion fires because the
data byte at the computed offset for `relacl` is not a valid varlena header.
Possible causes: misalignment in the tuple data (encoding vs TupleDesc
disagree on physical offset), or wrong `off` value from preceding fixed-width
column computation.

## Key Technical Insights

1. **PG casts raw tuple bytes as FormData_pg_class**: The `GETSTRUCT` macro
   in `RelationBuildDesc` returns a pointer to the raw tuple data and casts
   it to `FormData_pg_class*`. This means the heap tuple MUST have the exact
   binary layout of the C struct — column order, sizes, and padding must
   match byte-for-byte. A TupleDesc-based approach (like our 14-column
   attempt) will NOT work.

2. **Init file TupleDesc must agree with heap tuple layout**: The init file's
   `FormData_pg_attribute` blobs define the TupleDesc PG uses to decode heap
   tuples in `nocachegetattr`. If `attalign` is wrong, PG computes wrong
   physical offsets and reads garbage.

3. **attlen=0 is a poison value**: PG's `att_addlength_pointer` macro has no
   branch for `attlen=0`. It must be >0 (fixed), -1 (varlena), or -2
   (cstring). Any other value triggers `Assert(false)`.

4. **HEAP_HASVARWIDTH controls fast vs slow path**: Without this flag, PG
   takes the fast path in `nocachegetattr` and tries to cache all columns as
   fixed-width, asserting when `j > attnum` fails at the last fixed column.

5. **Vanilla PG compatibility is absolute**: The PG source tree under
   `./postgres/` is READ-ONLY. All fixes must be in goopg. See
   `.ralph/AGENT.md` for the policy.

## Files Modified

| File | Changes |
|------|---------|
| `internal/initdb/initdb.go` | `bootstrapPgClassTuples`, `bootstrapPgAttributeTuples`, `pgClassColDefs` (34 cols), `pgClassRow` (34 values), `pgAttrColDefs`, `pgAttrEntriesForRel`, `pgAttributeRow`, `writeMultiPageHeap`, `writeMultiPageHeapRows`, `hasVarWidthCol`, type helpers |
| `internal/initdb/relcache_init.go` | `buildPgClassBlob` (PG18 offsets), `buildPgAttributeBlob` (dynamic attalign), `indexKeyAttrs` (Len/TypeOID), `pgClassAttrs` (34 cols), `pgAlignChar`, `pgTypeIsByVal` |
| `internal/executor/codec.go` | `encodeValuePG`: char/float4/float8/xid encoding; `physicalPGTypeAlign`: char/name/xid alignment; `math` import |
| `internal/initdb/initdb_test.go` | Multi-block file-size check for pg_class/pg_attribute |
| `docs/design/0106-0002-pg-class-tuple-bootstrap.md` | Design doc |
| `.ralph/fix_plan.md` | M0106-0008 task description |

## Next Steps (for coding agent)

1. **Fix the varlena assertion**: The most promising approach is to verify
   the actual byte at the varlena offset (should be `0x01`). Write a Go
   unit test that encodes a pg_class tuple and checks the byte at offset
   144 (where `relacl` starts in the fixed-size part). If correct, the
   issue is in alignment computation within PG's slow path — consider
   setting `relacl`/`reloptions`/`relpartbound` as NULL (requires null
   bitmap support) to bypass the slow path entirely.

2. **After the assertion is fixed**, the next blocker will likely be
   `SearchSysCache1(AMOID, 403)` returning NULL because pg_am heap has
   no tuples. We'll need to bootstrap pg_am (btree row OID 403) and
   possibly pg_opclass/pg_amop/pg_amproc tuples.

3. **Operational relcache/catcache maintenance** (per design doc): This
   is NOT deferred — DDL must maintain PG-compatible catalog state and
   regenerate the relcache init file during normal operation.

## Design Docs

- `docs/design/0106-0001-relcache-init-file-format.md` — init file format
- `docs/design/0106-0002-pg-class-tuple-bootstrap.md` — this task
- `docs/milestones/0106-pg-relcache-init-file-compat.md` — milestone
