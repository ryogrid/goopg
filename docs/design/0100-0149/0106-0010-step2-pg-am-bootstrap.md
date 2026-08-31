# M0106-0010 step 2 — pg_am heap-tuple bootstrap

Status: accepted (initial seed; pg_opclass/pg_amop/pg_amproc still pending in a
follow-up).

## Why

After step 1 unblocked PG's `nocachegetattr` / `deconstruct_array` assertions on
cached pg_class tuples, the next failure shows up inside
`RelationInitIndexAccessInfo`. PG looks up the index's access method via:

```
SearchSysCache1(AMOID, ObjectIdGetDatum(relam))
```

`relam` is 403 (`BTREE_AM_OID`) for every critical index. The syscache walks
the pg_am heap (`base/<dbnode>/2601`); if no row matches, the lookup returns
`InvalidOid` and the backend PANICs the moment it tries to open any pg_class
or pg_attribute index. A PG standby cloned from a goopg backup never gets past
its first index open without this seed.

## What

Write PG-native `pg_am` heap tuples for every seed access method in PG18's
`src/include/catalog/pg_am.dat` (heap=2, btree=403, hash=405, gist=783,
gin=2742, spgist=4000, brin=3580). The heap tuple body matches `FormData_pg_am`
byte-for-byte so PG's `GETSTRUCT(tup)` cast is valid:

| offset | field     | type              | width |
| ------ | --------- | ----------------- | ----- |
| 0      | oid       | `Oid`             | 4     |
| 4      | amname    | `NameData`        | 64    |
| 68     | amhandler | `regproc` / `Oid` | 4     |
| 72     | amtype    | `char`            | 1     |

Files in this loop:

- `internal/initdb/initdb.go` — new `pgAmColDefs`, `pgAmEntry`,
  `pgAmInitialEntries`, `pgAmRow`, `bootstrapPgAmTuples`. `Init` calls
  `bootstrapPgAmTuples` after the existing `pg_class` and `pg_attribute` heap
  seeds; pages land at `base/1/2601` and `base/5/2601` via
  `writeMultiPageHeapRows` (the same path used for pg_attribute).
- `internal/initdb/relcache_init.go` — `pgAmAttrs` gains the missing
  `amtype` (`char`, attlen=1) column and `nailedLocalRels` advertises
  `relnatts=4` for pg_am, so the relcache init file's TupleDesc agrees with
  the heap tuple layout.

`amhandler` is intentionally typed as `oid` in the init file (matching the
storage width PG uses for `regproc`); the goopg encoder produces the same
4-byte LE value either way, so PG's `Form_pg_am` cast resolves the field
correctly without a separate `regproc` encoder pathway.

## Out of scope (follow-up)

`RelationInitIndexAccessInfo` continues into `bthandler` via fmgr. Calling
the handler requires:

- a `pg_proc` row for the handler OID (e.g. 330 for `bthandler`);
- `pg_opclass` rows for the per-column operator classes that pg_index points
  at;
- `pg_amop` / `pg_amproc` rows for each opclass's strategy / support fns.

Those seeds extend the same `writeMultiPageHeapRows` pattern but are tracked
separately so this loop stays scoped to "unblock SearchSysCache1(AMOID)".

## Verification

Targeted unit tests in `internal/initdb/pg_am_bootstrap_test.go`:

- `TestPgAmRowBtreeMatchesFormPgAm` — re-encodes the btree row through
  `executor.EncodeRowPG` and checks every field at the documented offset.
  Any alignment / type regression that breaks the PG read path surfaces
  here without needing the E2E harness.
- `TestPgAmInitialEntriesCoverPg18Defaults` — guards against accidental
  loss of any of the seven seed AMs.
- `TestBootstrapPgAmTuplesWritesBtreeRowToBase1And5` — covers the
  integration: both `base/1/2601` and `base/5/2601` materialise an 8 KiB
  page and contain the `btree` NameData blob.

Package gates (re-run before this commit):

- `go test -count=1 ./internal/initdb/` — only pre-existing
  `TestSynchronousCommitFlushesByDefault` fails (confirmed via baseline diff
  with the same WIP `git stash` technique used in the M0106-0010 step 1
  loop). All other initdb tests, including the new pg_am cases, PASS.
- `go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/
  ./internal/catalog/ ./internal/mvcc/` — PASS.
- `go build ./...` — PASS.
