# M0106-0010 Step 3cq — pg_type heap canonical typalign (root-cause + plan)

## Status

Diagnostic / root-cause loop. No PG-canonical pg_type rewrite yet.

This loop:

1. Reproduced the next PG-standby boot FATAL after Step 3cp
   (pg_user_mapping) with `TestE2E_FailoverGoopgToPG/async`.
2. Increased standby `log_min_messages` to `debug3` +
   `log_error_verbosity = verbose` to surface the FATAL's location.
3. Pinned the invariant that goopg's pg_attribute heap *is*
   well-formed at byte 83 (attalign) with a new regression test.
4. Established the actual blocker (pg_type heap is goopg-v0 encoded,
   not PG18 canonical) and the plan for Step 3cq proper.

## Symptom

```
[BACKEND-PID] DEBUG: InitPostgres        (postinit.c:723)
[BACKEND-PID] FATAL: XX000: invalid attalign value:   (location: tupdesc.c:105
                                                       populate_compact_attribute_internal)
```

Every user-backend connection FATALs immediately after `InitPostgres`
begins. The `%c` argument prints empty because `attalign` is `'\0'`.

Earlier in the log there is also a one-shot
`FATAL: 58P01: could not open file "base/5/2672": ...` (PID 1,
mdopenfork) — that is a separate, secondary issue tracked as the
next step in the sequence; not in scope for 3cq.

## Investigation summary

### Where the FATAL is raised

`tupdesc.c:105` is in `populate_compact_attribute_internal()`:

```c
switch (src->attalign)
{
    case TYPALIGN_INT:    /* 'i' */
    case TYPALIGN_CHAR:   /* 'c' */
    case TYPALIGN_DOUBLE: /* 'd' */
    case TYPALIGN_SHORT:  /* 's' */
        ...
    default:
        elog(ERROR, "invalid attalign value: %c", src->attalign);
}
```

### What `src` is

PG18's `TupleDescInitEntry` (`tupdesc.c:842`) sets
`att->attalign = typeForm->typalign;` from a SysCache lookup of
pg_type by OID (line 902), then calls `populate_compact_attribute`
on the freshly-written `att`. If `typeForm->typalign` is `'\0'`,
the dst tupdesc inherits `'\0'`, and the next call to
`populate_compact_attribute_internal` FATALs.

### Why init-file caching doesn't save us

PG18 `StartupXLOG` (xlog.c:5633) unconditionally calls
`RelationCacheInitFileRemove()` at the start of WAL recovery.
This wipes `global/pg_internal.init` and every
`base/<dboid>/pg_internal.init` regardless of clean-shutdown state.
So the init-file copies that `copyInitFiles()` placed under
`base/1/`, `base/5/`, `global/` are gone before any client backend
runs `InitPostgres`. Every backend therefore *has* to rebuild
relcache + tupledesc + typecache from heap. Prior Step 3X loops
worked because they died earlier on a missing nailed OID; once
3cp finished the nailed-rel sweep, the next failure mode surfaces:
pg_type lookup returns garbage typalign.

### What goopg's pg_type heap currently contains

`bootstrapSystemCatalogs` (`initdb.go:3431`) writes pg_type via
`extendWithRows(mgr, TypeRelationId, pgTypeData)` where
`pgTypeData[i] = catalog.EncodePGTypeRow(...)`. The v0 layout
emitted by `EncodePGTypeRow` (`internal/catalog/codec.go:179`)
contains only the seven fields the goopg planner needs
(`OID, TypName, TypNamespace, TypLen, TypByVal, TypType, TypCategory`)
and uses goopg's own field ordering — *not* the PG18
`FormData_pg_type` struct layout. When PG18 casts a goopg-v0
heap-tuple data buffer as `Form_pg_type *`, the byte that PG18
expects at `offsetof(FormData_pg_type, typalign)` is unrelated
goopg-v0 payload — often `'\0'`.

### What goopg's pg_attribute heap *does* contain (correctly)

`bootstrapPgAttributeTuples` (`initdb.go:1033`) overwrites the
file `base/1/1249` (and `base/5/1249`) with PG-native rows produced
by `pgAttributeRow` + `executor.EncodeRowPG`. New test
`TestAllPgAttributeHeapRowsHaveValidAttalignByte`
(`internal/initdb/pg_attribute_attalign_offset_test.go`) confirms
that for every nailed rel + attribute, byte 83 (attalign offset in
`FormData_pg_attribute`) is one of `'c'/'s'/'i'/'d'`. So the heap
copy of attalign is correct *in pg_attribute*; the breakage is
upstream of that — PG18 calls `TupleDescInitEntry` which *overrides*
attalign from pg_type, and that lookup is the corrupt one.

## Fix plan (Step 3cq)

Rewrite the pg_type heap pages so PG18 sees a canonical
`FormData_pg_type` layout, at least for the type OIDs that any
nailed-rel attribute references. The needed type set is finite and
already enumerated by `pgTypeAlignChar` (`initdb.go:3369`):

| OID | name           | typlen | typbyval | typalign |
|-----|----------------|--------|----------|----------|
| 16  | bool           | 1      | t        | 'c'      |
| 17  | bytea          | -1     | f        | 'i'      |
| 18  | char           | 1      | t        | 'c'      |
| 19  | name           | 64     | f        | 'c'      |
| 20  | int8           | 8      | t        | 'd'      |
| 21  | int2           | 2      | t        | 's'      |
| 22  | int2vector     | -1     | f        | 'i'      |
| 23  | int4           | 4      | t        | 'i'      |
| 24  | regproc        | 4      | t        | 'i'      |
| 25  | text           | -1     | f        | 'i'      |
| 26  | oid            | 4      | t        | 'i'      |
| 27  | tid            | 6      | f        | 's'      |
| 28  | xid            | 4      | t        | 'i'      |
| 30  | oidvector      | -1     | f        | 'i'      |
| 194 | pg_node_tree   | -1     | f        | 'i'      |
| 269 | text[]/_text   | -1     | f        | 'i'      |
| 700 | float4         | 4      | t        | 'i'      |
| 701 | float8         | 8      | t        | 'd'      |
| 1009| text[] alias   | -1     | f        | 'i'      |
| 1028| oid[]          | -1     | f        | 'i'      |
| 1034| aclitem[]      | -1     | f        | 'i'      |
| 1184| timestamptz    | 8      | t        | 'd'      |
| 1185| timestamptz[]  | -1     | f        | 'd'      |
| 3220| pg_lsn         | 8      | t        | 'd'      |
| 5017| pg_mcv_list    | -1     | f        | 'i'      |
| ... | ...            | ...    | ...      | ...      |

Approach:

1. Add `pgTypeColDefs()` mirroring PG18's `FormData_pg_type`
   columns (oid, typname, typnamespace, typowner, typlen, typbyval,
   typtype, typcategory, typispreferred, typisdefined, typdelim,
   typrelid, typsubscript, typelem, typarray, typinput, typoutput,
   typreceive, typsend, typmodin, typmodout, typanalyze, typalign,
   typstorage, typnotnull, typbasetype, typtypmod, typndims,
   typcollation, plus CATALOG_VARLEN `typdefaultbin`, `typdefault`,
   `typacl`). All fixed-part offsets must match PG18 exactly so
   `Form_pg_type *` cast lands typalign at the right byte.
2. Add `pgTypeInitialEntries()` — one row per OID the nailed-rel
   attribute set references. Fill typlen, typbyval, typalign,
   typstorage authoritatively from `postgres/src/include/catalog/pg_type.dat`.
3. Add `bootstrapPgTypeTuples(dataDir)` that uses
   `writeMultiPageHeapRows(dataDir, "1247", pgTypeColDefs(), rows)`
   to overwrite `base/1/1247` and `base/5/1247` with PG-canonical
   heap pages — same idempotent overwrite pattern as
   `bootstrapPgAttributeTuples`.
4. Wire the new bootstrap into `Init()` after the existing
   `bootstrapSystemCatalogs` (or replace the pg_type portion of it).
5. Index seeding: pg_type's nailed indexes (2703 oid, 2704
   typname/nsp) already get populated through the existing
   nailed-rel sweep; only verify the heap TIDs in
   `pgTypeTIDs` flow into 2703/2704 populated-btree builders.

Regression coverage:

- New: `TestPgTypeInitialEntriesCoverNailedAttrTypeOIDs` — every
  TypeOID present in any nailedAttr must be in `pgTypeInitialEntries()`.
- New: `TestBootstrapPgTypeTuplesWritesCanonicalTypalign` — load the
  emitted heap page, walk tuples, assert byte at
  `offsetof(FormData_pg_type, typalign)` equals
  `pgTypeAlignChar(oid)[0]`.
- New: `TestEncodeValuePGOidvectorAlignsToInt` — pin the
  `int2vector`/`oidvector` 4-byte alignment.

Verification: re-run `TestE2E_FailoverGoopgToPG/async`; the
`invalid attalign value:` FATAL should clear, exposing the
next blocker (likely the `base/5/2672` follow-up or a
`could not open relation` deeper in pg_database scan).

## Out of scope for 3cq

- The `base/5/2672` (pg_database_oid_index) one-shot FATAL on the
  first backend. That backend appears to be the
  `autovacuum launcher`-equivalent and dies *after* phase3,
  during a pg_database scan. Fixing the file-presence issue is
  a separate step (3cr) once 3cq lets user backends past
  InitPostgres.

- pg_type CATALOG_VARLEN columns (typdefaultbin, typdefault,
  typacl). Step 3cq emits them all as SQL NULL; only the fixed
  part has to be canonical because `ATTRIBUTE_FIXED_PART_SIZE`
  is what gets memcpy'd into the TupleDesc.

## Files this loop touches

- New: `internal/initdb/pg_attribute_attalign_offset_test.go` —
  pins pg_attribute heap byte 83 invariant.
- Modified: `internal/testport/e2e_failover_goopg_to_pg_test.go` —
  `log_min_messages = debug3` + `log_error_verbosity = verbose` in
  the standby `postgresql.auto.conf`. Test-only; only active when
  `GOOPG_RUN_BLOCKED_M0102_E2E=1`.
- New: this design doc.
- Modified: `.ralph/fix_plan.md` — records partial progress + new
  Step 3cq scope.
- Modified: `docs/design/README.md` — index entry for this doc.
