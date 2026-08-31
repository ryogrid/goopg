# M0106-0010 Step 3dh — pg_database_datname_index name-typed descriptor + seeded leaf

**Status**: accepted (2026-05-18)
**Milestone**: M0106-0010 (PG Relcache Init File Compatibility)
**Previous step**: 3dg (pg_authid_rolname_index typed-key descriptor)
**Next step**: TBD — re-run `TestE2E_FailoverGoopgToPG/async` with this fix
to confirm the FATAL `3D000: database "postgres" does not exist` is gone and
capture the next blocker.

## Symptom

Step 3dg fixed the SIGSEGV in `btnamecmp → __strncmp_avx2` that crashed every
PG-standby boot. With the SEGV gone, the next E2E run
(`tmp/m0106-step3dg/e2e_run2.log`) advanced to the first user-visible psql
connection and FATAL'd:

```
psql: error: connection to server at "127.0.0.1", port 38223 failed:
FATAL:  3D000: database "postgres" does not exist
```

The server-side log on the goopg-cloned PG18 backend confirms the same
ERRCODE at `postinit.c` during InitPostgres' `get_db_info()` lookup:

```
2026-05-18 13:14:16.252 GMT [1339395] FATAL:  3D000:
    database "postgres" does not exist
```

## Root cause

`bootstrapPostgresDatabase` (initdb.go) seeds the two canonical pg_database
heap rows correctly:

* offset 1 → `template1` (OID 1)
* offset 2 → `postgres`  (OID 5)

`bootstrapPgDatabaseOidIndex` (Step 3cs) populates `global/2672`
(`pg_database_oid_index`) with one IndexTuple per row, so the
`DATABASEOID` syscache lookup (`MyDatabaseId → live row`) succeeds.

What is missing is the **sibling** name-keyed index. The
`pg_database_datname_index` (PG18 OID 2671) is a 1-column UNIQUE btree on
`pg_database.datname` (name_ops), backing the `DATABASENAME` syscache.
`InitPostgres → get_db_info()` (`postgres/src/backend/utils/init/postinit.c`)
searches for the database name *before* MyDatabaseId is bound, and the
lookup goes through the syscache → `pg_database_datname_index`.

`global/2671` exists as an 8 KiB empty btree placeholder produced by
`bootstrapSharedCatalogPlaceholders`. With an empty btree the syscache
returns NULL and the backend rejects the connection with `3D000:
database "postgres" does not exist`.

Step 3dg's design note flagged this preemptively:

> Note: 2671 is also a name-typed key, so it carries the *same* latent SEGV
> that 3dg just fixed for 2676 — its idxSpec needs the same
> `Attrs: [{Name:"datname", TypeOID:19, Len:64, …}]` override.

So the fix has two halves: **(a)** populate the leaf, and **(b)** correct the
nailed descriptor before any backend exercises the index.

## Fix

### (a) Populate the leaf — `bootstrapPgDatabaseDatnameIndex`

New helper in `internal/initdb/btree_index_bootstrap.go`, modelled on the
sibling `bootstrapPgDatabaseOidIndex` and the rolname half of
`bootstrapPgAuthidIndexes`:

```
func bootstrapPgDatabaseDatnameIndex(dataDir string) error {
    entries := []nameTid{
        {name: "template1", tid: 1},
        {name: "postgres",  tid: 2},
    }
    sort.Slice(entries, ...by name...)
    tuples := pgBuildIndexTupleNameKey(0, e.tid, e.name) for each entry
    leaf := pgBuildBtreeLeafRootPage(tuples)
    meta := pgBuildBtreeMetapageWithRoot(1, 0)
    write meta+leaf → global/2671
}
```

Leaf ordering: btnamecmp does unsigned byte compare on the 64-byte
zero-padded NameData blob, so `"postgres"` (0x70…) comes before
`"template1"` (0x74…). The helper sorts entries by name to enforce the
btree invariant regardless of insertion order.

Wired into `bootstrap` in `internal/initdb/initdb.go` immediately after
`bootstrapPgDatabaseOidIndex`, before `bootstrapPgAuthidIndexes`.

### (b) Typed-key descriptor in `relcache_init.go`

OID 2671's `idxSpec` literal gains an explicit `Attrs` override mirroring
Step 3dg's fix for 2676:

```
{OID: 2671, Name: "pg_database_datname_index", Attrs: []nailedAttr{
    {Name: "datname", TypeOID: 19, Num: 1, Len: 64, NotNull: true},
}},
```

`buildPgAttributeBlob` consumes the override and emits `attbyval=0`,
`attlen=64`, `attalign='c'` — the byref/64-byte NameData contract that
`_bt_compare → index_getattr → btnamecmp` expects. Without this, the same
4-byte by-val Datum trap that crashed pg_authid_rolname_index in Step 3df
would reproduce verbatim on the first `_bt_compare` call against the seeded
leaf.

## Regression tests

`internal/initdb/pg_database_datname_index_test.go`:

* `TestNailedPgDatabaseDatnameIndexHasNameDescriptor` — asserts
  `RelKind='i'`, `RelNatts=1`, `Attrs[0].TypeOID=19`, `Len=64`,
  `Name="datname"`. Re-encodes the materialised pg_attribute blob and pins
  `attbyval=0`, `attalign='c'`, `attlen=64`. If anyone reverts the explicit
  Attrs override the test fails with a clear "want 0, got 1" on
  `attbyval` and points directly at the SEGV regression.
* `TestBootstrapPgDatabaseDatnameIndexWritesPopulatedBtree` — runs the
  bootstrap helper into a temp dir, reads back `global/2671`, asserts file
  size is exactly 2 pages, `btm_root=1`, nItems=2, and both leaf tuples are
  keyed on the canonical names ("template1", "postgres").
* `TestBootstrapPgDatabaseDatnameIndexLeafKeysAscending` — pins the
  on-disk ordering (`"postgres" < "template1"`). A regression that emits
  in insertion order would break `_bt_search` and surface as missed lookups
  even though both tuples are physically present.

## Verification

* `go test -count=1 -run 'TestNailedPgDatabaseDatnameIndex|TestBootstrapPgDatabaseDatnameIndex' ./internal/initdb/` — PASS.
* `go test -count=1 -run 'TestNailedPgAuthidRolnameIndex|TestBootstrapPgAuthid|TestPgBuildIndexTupleName|TestBootstrapPgDatabase|TestNailedPgDatabaseDatname' ./internal/initdb/` — PASS (Step 3dg's pins remain green).
* `go build ./...` — clean.
* `make ralph-state-guard` — PASS (gated by Ralph harness).
* E2E re-run (`TestE2E_FailoverGoopgToPG/async`) is the verification gate
  for the next step (3di); the design pins the *byte-level* fix, the E2E
  pins the *behavioural* fix.

## Files touched

* `internal/initdb/btree_index_bootstrap.go` — new
  `bootstrapPgDatabaseDatnameIndex` helper.
* `internal/initdb/initdb.go` — wires the helper into bootstrap.
* `internal/initdb/relcache_init.go` — name-typed `Attrs` override for OID
  2671.
* `internal/initdb/pg_database_datname_index_test.go` — 3 new regression
  pins (descriptor, leaf shape, leaf ordering).
* `docs/design/0106-0010-step3dh-pg-database-datname-index.md` — this doc.
* `docs/design/README.md` — index entry.
