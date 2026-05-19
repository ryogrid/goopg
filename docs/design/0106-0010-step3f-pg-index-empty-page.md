# M0106-0010 Step 3f — Empty pg_index heap page

Status: ACCEPTED
Date: 2026-05-17

## Problem

After Step 3e landed the canonical sortsupport / equalimage rows in
`pg_amproc`, the heterogeneous failover E2E test
(`TestE2E_FailoverGoopgToPG/async`) advanced past the
`nocachegetattr` / `pg_amproc` blockers but now FATALs every PG
backend that connects to the standby with:

```
FATAL: could not open file "base/5/2610": No such file or directory
```

OID 2610 is `pg_index`. During nailed-index initialisation (one of
the first acts of `RelationCacheInitializePhase3`), PG's
`RelationOpenSmgr → mdopen → BasicOpenFile` is dispatched for
every critical index — and the open is for the *parent table* of
the index, including `pg_index` itself. goopg's bootstrap had
mapped OID 2610 in `pg_filenode.map` (`internal/initdb/initdb.go`)
but never wrote the heap file to disk, so the open failed before
any tuple-level lookup could even start.

## Decision

Write an *initialised* but *empty* heap page for `pg_index` to
`base/{1,5}/2610` as part of goopg init. An empty page is the
minimum that satisfies `BasicOpenFile` and the immediately
following `RelationGetNumberOfBlocks` — PG only refuses to read a
*nonexistent* segment, not an empty one, and an `InitPage`'d page
distinguishes itself from an all-zero hole.

The per-index `Form_pg_index` rows (21-column shape including
`int2vector indkey`, `oidvector indclass`, `oidvector indcollation`,
`int2vector indoption`, `pg_node_tree indexprs / indpred`) are
deliberately deferred to Step 3g. Step 3f is *only* the file-
existence unblock; running the E2E again with this change applied
surfaces the next, structurally distinct blocker:

```
FATAL: cache lookup failed for index 2662
```

— exactly the symptom we expect when pg_index has no tuples
matching `pg_class_oid_index` (OID 2662). Step 3g will close that
by encoding `Form_pg_index` rows for every nailed local index.

## Implementation

`internal/initdb/initdb.go`:

- `pgIndexMinimalColDefs()` — 4-column shape matching the existing
  `pgIndexAttrs()` declaration in `relcache_init.go` (oid,
  indrelid, indnatts, indislive). Step 3g will expand both
  declarations together to the full 21-column PG18 layout.
- `bootstrapPgIndexTuples()` — calls
  `writeMultiPageHeapRows(dataDir, "2610", cols, nil)`. With a
  nil row slice, `writeMultiPageHeapRows` initialises a single
  heap page via `storage.InitPage` and writes it to
  `base/1/2610` *and* `base/5/2610` (the function unconditionally
  mirrors both directories).
- `Init()` invokes the new bootstrap immediately after
  `bootstrapPgAmprocTuples`, preserving the strict ordering of
  Step 3a → 3e.

## Verification

Regression pin: `TestBootstrapPgIndexTuplesWritesEmptyPageToBase1And5`
in `internal/initdb/pg_index_bootstrap_test.go` —

- writes the file to `base/{1,5}/2610`
- asserts `len == BlockSize` (8 KiB)
- asserts the page is not all-zero (i.e. `InitPage` actually ran)

A second pin (`TestPgIndexMinimalColDefsMatchesRelcacheAttrs`)
guards the alignment between the empty-page schema and the
relcache init-file `pgIndexAttrs()` so the Step 3g expansion of
the per-row encoder must update both in lockstep.

Gates run:

- `go test -count=1 ./internal/initdb/ ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS except the pre-existing
  `TestSynchronousCommitFlushesByDefault` failure (M0106-0012,
  baseline-stash confirmed).
- `TestE2E_FailoverGoopgToPG/async` — advances from
  `could not open file "base/5/2610"` to
  `cache lookup failed for index 2662`, confirming Step 3f
  unblocks the file open and surfaces the expected next blocker.

## Out of scope (Step 3g)

- Per-index `Form_pg_index` row encoder — fixed-size 24 bytes
  (oid + oid + 2×int2 + 11 bools + 1 pad) followed by the
  varlen `indkey` / `indcollation` / `indclass` / `indoption` /
  `indexprs` / `indpred` block.
- `int2vector` codec support in `internal/executor/codec.go`
  (mirror of the existing `oidvector` handling — elemtype=21
  payload of 2-byte values).
- `pgIndexAttrs()` / `nailedLocalRels` natts expansion 4 → 21
  so PG's `heap_deformtuple` reads every column the relcache
  init file advertises.
- Initial row set: one entry per nailed local index in
  `internal/initdb/relcache_init.go::nailedLocalRels` + the
  shared-catalog indexes that PG looks up against the local
  per-database `pg_index` (template1=1 + postgres=5).
