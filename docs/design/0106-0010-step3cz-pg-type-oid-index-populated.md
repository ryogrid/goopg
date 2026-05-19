# M0106-0010 step 3cz — pg_type_oid_index populated 2-page btree

Status: landed (2026-05-18)

## Problem (observed in Step 3cy diagnostic)

After Step 3cx fixed the `28000: role "ryo" does not exist` FATAL,
the next blocker captured by the always-on standby `pg.log` dump was:

```
XX000: cache lookup failed for type 23
  at TupleDescInitEntry, tupdesc.c:896
```

Type OID 23 is `int4`. PG's `TupleDescInitEntry` calls
`SearchSysCache1(TYPEOID, ObjectIdGetDatum(typeoid))` on every column of
every `TupleDesc` it builds. On the standby this takes the indexed path
through `pg_type_oid_index` (OID 2703). Until Step 3cz, `2703` on disk
was the Step-3k empty-btree placeholder (metapage-only with `btm_root=0`),
so the syscache lookup returned NULL even though the `pg_type` heap was
populated by Step 3cq.

Without `int4` resolving from the syscache, the first client backend's
trivial `SELECT 1` probe (issued by `WaitReady`) errored, and a
follow-up backend on the same postmaster crashed with SIGSEGV — almost
certainly derivative of the same NULL syscache entry leaking into
uninitialised `InitPostgres` state. The crash loop blocked
`standby.Stop()` and the test consumed its full 300 s budget.

## Fix

Apply the established Step-3cs / Step-3cw / Step-3cx pattern: build a
populated 2-page btree (metapage + leaf-root) for `pg_type_oid_index`,
seeded from the exact `pg_type` heap rows that `bootstrapPgTypeTuples`
wrote in Step 3cq.

### Heap-side change

`bootstrapPgTypeTuples(dataDir)` now returns `([]heapTID, error)` (was
`error`). The TIDs are the per-row `(block, offset)` pairs already
computed inside `writeMultiPageHeapRows`; capturing them is required
because the index leaf entries must point at the exact slot where each
type row landed.

### Index-side change

New function in `internal/initdb/btree_index_bootstrap.go`:

```go
func bootstrapPgTypeOidIndex(dataDir string, tids []heapTID) error
```

It:

1. Loads the canonical pg_type entry list via `pgTypeInitialEntries()`
   — the same list `bootstrapPgTypeTuples` consumed, so `entries[i]`
   and `tids[i]` align.
2. Builds an `(oid, block, off)` triple per entry and sorts
   ascending by OID (btree leaf key order).
3. Builds one IndexTuple per row via `pgBuildIndexTupleOidKey(block,
   off, oid)` — the exact byte layout pinned by
   `TestPgBuildIndexTupleOidKeyLayoutMatchesPG18`.
4. Builds the leaf-root page via `pgBuildBtreeLeafRootPage(tuples)` and
   the metapage via `pgBuildBtreeMetapageWithRoot(1, 0)`.
5. Writes the 2-page file under `base/1/2703` and `base/5/2703`.
   `pg_type` is a **per-database** catalog (unlike `pg_authid` or
   `pg_database`), so there is no `global/2703` copy.

### Init() wiring

`internal/initdb/initdb.go::Init` captures
`pgTypeTIDs, err := bootstrapPgTypeTuples(abs)` and calls
`bootstrapPgTypeOidIndex(abs, pgTypeTIDs)` immediately afterwards,
before any subsequent bootstrap touches `pg_type` indirectly via
syscache.

## Regression pins

New file: `internal/initdb/pg_type_oid_index_test.go`.

`TestBootstrapPgTypeOidIndexWritesPopulatedBtree`:

- Verifies both `base/1/2703` and `base/5/2703` exist and are exactly
  `2 * BlockSize` bytes (meta + leaf-root).
- Walks the leaf line-pointers and asserts:
  - Line-pointer count equals `len(tids)` (one per heap tuple).
  - OID keys are strictly ascending.
  - **Mandatory presence of an `oid=23` leaf** — the exact key whose
    absence triggered the Step 3cy FATAL. The test fails loudly if
    `pgTypeInitialEntries` ever stops including `int4`.

## Verification

- `go build ./...` — clean.
- `go test -count=1 -run
  'TestBootstrapPgTypeOidIndex|TestBootstrapPgTypeTuples'
  ./internal/initdb/` — PASS (2/2).
- `go test -count=1 ./internal/initdb/` — 15 pre-existing baseline
  failures only (same set as Step 3cw / 3cx; no new regressions).
- Cross-package smoke: `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — all PASS.

## Next blocker

E2E re-run under
`TestE2E_FailoverGoopgToPG/async` is expected to advance past the
`cache lookup failed for type 23` line. The Step 3cy diagnostic
mentioned a derivative `signal 11: Segmentation fault` on a follow-up
backend; the working hypothesis is that the SIGSEGV was caused by the
NULL syscache entry leaking into later state, so it should disappear
once Step 3cz fixes the lookup. If the SIGSEGV survives, promote it
to its own step (3da) with a fresh standby pg.log capture.
