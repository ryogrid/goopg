# M0106-0010 Step 3a — pg_proc Heap Bootstrap for AM Handlers

Status: implemented (2026-05-17)
Milestone: M0106 (PG standby ↔ goopg primary)
Scope: `internal/initdb/`, `internal/executor/codec.go`

## Why this exists

Step 2 (commit `e999e30`) made PG18's `SearchSysCache1(AMOID, 403)` for
the btree access method return a populated `Form_pg_am` tuple instead
of NULL, by seeding `pg_am` heap tuples for the seven canonical AMs
under `base/{1,5}/2601`.

PG's `RelationInitIndexAccessInfo` does not stop after AMOID. It
continues into the AM-specific handler by calling
`OidFunctionCall0(amhandler)` — for btree, `OidFunctionCall0(330)`.
That call routes through fmgr:

```
OidFunctionCall0 → fmgr_info(330, &flinfo)
                 → fmgr_info_cxt_security
                 → SearchSysCache1(PROCOID, 330)
                 → GETSTRUCT(tup) → Form_pg_proc
```

If `pg_proc` has no row for OID 330, `SearchSysCache1` returns NULL
and PG PANICs with `"cache lookup failed for function 330"` during
standby startup. Step 3a closes that gap.

## What landed

1. **`encodeValuePG` learns three new types** (`internal/executor/codec.go`):
   - `oidvector` — KindBytes passthrough; the caller pre-encodes the
     varlena+ArrayType blob via `oidVectorBytes(...)`.
   - `regproc` — encoded as a 4-byte little-endian OID, matching the
     `regproc` typlen=4 / typbyval=t pg_type entry.
   - `char[]` / `_char` — empty binary `ArrayType` blob with
     elemtype=18, matching the existing `aclitem[]` / `text[]` pattern
     so PG's `deconstruct_array` does not trip its `ARR_ELEMTYPE`
     assertion when `proargmodes` is read as raw bytes.

   `physicalPGTypeAlign` extended to map oidvector, regproc, char[]
   and oid[] to their PG `typalign` of `'i'` (4 bytes).

2. **pg_proc bootstrap** (`internal/initdb/initdb.go`):
   - `oidVectorBytes([]uint32) []byte` builds the on-disk oidvector
     layout (4-byte varlena header + 20-byte ArrayType header +
     N×4 oid payload, all LE).
   - `pgProcEntry` / `pgProcColDefs()` / `pgProcInitialEntries()` /
     `pgProcRow()` / `bootstrapPgProcTuples()` mirror the pg_am
     pattern from step 2 and seed seven `Form_pg_proc`-shaped rows
     under `base/{1,5}/1255`. The seven entries match
     `postgres/src/include/catalog/pg_proc.dat`:

     | OID | proname                | prorettype | prosrc                 |
     |-----|------------------------|------------|------------------------|
     | 3   | heap_tableam_handler   | 269        | heap_tableam_handler   |
     | 330 | bthandler              | 325        | bthandler              |
     | 331 | hashhandler            | 325        | hashhandler            |
     | 332 | gisthandler            | 325        | gisthandler            |
     | 333 | ginhandler             | 325        | ginhandler             |
     | 334 | spghandler             | 325        | spghandler             |
     | 335 | brinhandler            | 325        | brinhandler            |

     All seven rows share: `pronamespace=11` (pg_catalog),
     `proowner=10` (bootstrap superuser), `prolang=12` (internal),
     `prokind='f'`, `provolatile='v'`, `proparallel='s'`,
     `pronargs=1`, `proargtypes=oidvector{2281}` (one `internal` arg).
     `prosrc` is the fmgr internal-lookup key — PG's
     `fmgr_info_C_lang` resolves `bthandler` etc. against
     `fmgrtab[]`, no shared-object lookup required.

   - `pgCatalogTypeOID` / `pgCatalogTypeLen` / `pgTypeByVal` /
     `pgTypeAlignChar` / `pgTypeStorageChar` learn the new type OIDs
     (24, 30, 269, 325, 1002, 1028, 2281).

   - `hasVarWidthCol` extended to recognise every varlena type used
     by pg_class and pg_proc, not just `text`, so the
     `HEAP_HASVARWIDTH` infomask bit is set on the resulting tuples.

3. **Relcache init-file descriptor** (`internal/initdb/relcache_init.go`):
   - `pgProcAttrs()` expanded from 13 → 30 columns to match PG18's
     `FormData_pg_proc`. Per-column `(TypeOID, Len, NotNull)` matches
     `postgres/src/include/catalog/pg_proc.h`.
   - `nailedLocalRels` entry for `pg_proc` bumps `relnatts` 13 → 30
     so PG's `heap_deformtuple` can correctly read `prosrc`
     (attnum 26) and the trailing varlena fields when populating
     the FmgrInfo cache.

4. **`Init` wires it up** — `bootstrapPgProcTuples(abs)` is invoked
   immediately after `bootstrapPgAmTuples(abs)` in `initdb.Init` so
   a fresh `goopg init` writes both files.

## Why the empty-array encoding

PG18's `SearchSysCache1` returns a `HeapTuple` whose bytes PG casts
directly into structures like `ArrayType*` via
`DatumGetArrayTypeP`. M0106-0009 demonstrated that a text varlena
`{}` fails the `ARR_ELEMTYPE` assertion at `arrayfuncs.c:3644`. The
fix there was to emit a 16-byte binary `construct_empty_array`-shaped
blob for `pg_class.relacl`. Step 3a applies the same pattern to every
`pg_proc` `CATALOG_VARLEN` field that is conceptually "NULL by default"
in the upstream `pg_proc.dat` — they all get a typed empty `ArrayType`
or empty varlena, not SQL NULL.

This avoids the null-bitmap path entirely (which M0106-0009 abandoned
because the bitmap shifts data and breaks the test's raw-byte
GETSTRUCT comparison).

## Out of scope (tracked separately)

- `pg_opclass` rows for the operator classes referenced by
  `pg_index.indclass` (step 3b).
- `pg_amop` / `pg_amproc` strategy- and support-function rows
  (step 3c).
- Operational catalog maintenance — DDL must regenerate pg_proc
  rows for newly created functions and re-emit the relcache init
  file (tracked under M0106-0011).

## Verification

- `go test -count=1 ./internal/initdb/` — all initdb tests pass
  except the pre-existing baseline failure
  `TestSynchronousCommitFlushesByDefault` (confirmed unchanged via
  baseline diff against `HEAD`).
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — all
  green.
- New regression tests pin the layout:
  - `TestPgProcRowBtreeHandlerMatchesFormPgProc` —
    field-by-field byte-offset asserts the bthandler row matches the
    PG18 `Form_pg_proc` layout (oid, proname, pronamespace,
    proowner, prolang, prokind, provolatile, pronargs, prorettype,
    proargtypes oidvector header + payload).
  - `TestPgProcInitialEntriesCoverAMHandlers` — the seven AM handler
    entries cover {3, 330, 331, 332, 333, 334, 335} with the right
    return types.
  - `TestBootstrapPgProcTuplesWritesRowsToBase1And5` — end-to-end
    bootstrap writes a page-sized `1255` file under `base/1` and
    `base/5` containing the bthandler proname.
  - `TestPgProcAttrsMatchesPg18FormPgProc` — the relcache
    init-file TupleDesc has all 30 columns in the right order,
    each with the right (TypeOID, Len, attnum).
