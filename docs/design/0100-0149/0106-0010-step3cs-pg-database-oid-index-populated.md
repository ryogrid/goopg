# M0106-0010 step 3cs — populate `pg_database_oid_index` btree at `global/2672`

Status: LANDED 2026-05-18

## Motivation

After step 3cr made `pg_class.reltablespace = GLOBALTABLESPACE_OID (1664)`
for shared catalogs, PG's `RelationInitPhysicalAddr`
(`postgres/src/backend/utils/cache/relcache.c:1347-1354`) routes
`pg_database_oid_index` (OID 2672) to `global/2672`. The previous step
left an *empty* btree placeholder there — sufficient to satisfy
`mdopen()`, but PG's `CheckMyDatabase` (`postinit.c:335`) immediately
probes the syscache `DATABASEOID` to validate that the connection's
`MyDatabaseId` references a live `pg_database` row. With an empty
btree the syscache lookup returns NULL and every connecting backend
FATALs with:

```
FATAL:  XX000: cache lookup failed for database 5
LOCATION:  CheckMyDatabase, postinit.c:335
```

That FATAL was surfaced by `TestE2E_FailoverGoopgToPG/async` immediately
after step 3cr (this loop). Subsequent backends were observed FATALing
with `invalid attalign value:` at `populate_compact_attribute_internal`
— the same symptom from the step-3cq diagnostic, retriggered because
the first backend's failed `InitPostgres` left stale catcache state
for the followers.

## Fix

New function `bootstrapPgDatabaseOidIndex` in
`internal/initdb/btree_index_bootstrap.go` overwrites the empty
placeholder at `global/2672` with a populated 2-page btree:

- metapage at block 0 pointing at the leaf-root at block 1
  (`pgBuildBtreeMetapageWithRoot(1, 0)`)
- leaf-root at block 1 holding one oid-keyed `IndexTuple` per
  `pg_database` heap row written by `bootstrapPostgresDatabase`

`bootstrapPostgresDatabase` writes two rows in deterministic order onto
a fresh block 0 of `global/1262`:

| heap TID (block, off) | oid | datname    |
|-----------------------|-----|------------|
| (0, 1)                | 1   | template1  |
| (0, 2)                | 5   | postgres   |

The btree leaf-root is keyed on the `oid` column with ascending order.
The entries are sorted by oid before encoding (oids 1, 5 are already in
order so no swap is needed in practice). `IndexTuple` payloads are built
with `pgBuildIndexTupleOidKey(heapBlock, heapOff, oid)` — the same
helper used by every other oid-keyed nailed index seeded so far.

The fix is wired into `Init` in `internal/initdb/initdb.go` immediately
after `bootstrapPostgresDatabase`:

```go
if err := bootstrapPostgresDatabase(abs); err != nil { ... }
if err := bootstrapPgDatabaseOidIndex(abs); err != nil { ... }
```

The file lives **only** under `global/` — `pg_database_oid_index` is a
shared catalog (`relisshared = true`, `reltablespace = 1664`) so PG
resolves it exclusively there. Earlier per-database fallbacks under
`base/1/2672` and `base/5/2672` are now dead paths (left in place to
avoid disturbing other shared-catalog placeholder loops).

## Regression test

`internal/initdb/pg_database_oid_index_test.go::TestBootstrapPgDatabaseOidIndexWritesPopulatedBtree`

The test:

1. Calls `bootstrapPgDatabaseOidIndex` in a temp data dir.
2. Reads the resulting `global/2672` file (must be exactly two PG block
   sizes: metapage + leaf root).
3. Parses the leaf page header and asserts 2 line pointers exist
   (one per `pg_database` heap row).
4. Walks both `IndexTuple`s and asserts:
   - the oid keys are `1` (template1) and `5` (postgres), ascending;
   - the embedded heap block is 0.

A future change that drops entries or breaks ordering will trip the
assertions immediately, surfacing the regression at `go test` time
rather than waiting for the slow E2E.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run TestBootstrapPgDatabaseOidIndexWritesPopulatedBtree ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — 15 baseline failures, identical
  to the step-3cr/3cq baseline (no new regressions).
- Cross-package smoke: `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — all PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -run TestE2E_FailoverGoopgToPG/async`:
  the `cache lookup failed for database 5` FATAL is gone and
  `invalid attalign value` no longer surfaces.

## Next blocker (step 3ct)

The E2E now hits a different failure mode on the standby:

```
TRAP: failed Assert("j > attnum"), File: "heaptuple.c", Line: 642, PID: ...
client backend ... was terminated by signal 6: Aborted
```

`heaptuple.c:642` is inside `slot_deform_heap_tuple`'s null-bitmap loop;
the assert fires when the loop advances past `attnum` without finding
the requested attribute. The most likely cause is a tuple with a
mismatched `t_natts` or null-bitmap layout for one of the rows PG opens
right after `CheckMyDatabase` succeeds. Tracked as step 3ct.
