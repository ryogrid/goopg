# M0119 — pg_relation_filepath, and the dbsize functions' relation argument

Status: landed 2026-10-21.

## 1. Gap

- `SELECT pg_relation_filepath('t')` raised 42883. pg_proc declared the
  function (OID 3034), but the executor had no implementation.
- `pg_relation_filenode` returned the wrong value for two relkinds:
  - NULL for every index (PG: the index's filenode);
  - the view's own OID for a view (PG: NULL).
- `pg_relation_size('t')`, `pg_table_size`, `pg_indexes_size` and
  `pg_total_relation_size` took no name literal: `resolveRegclassOID` parsed
  only digits. `pg_relation_size('t')` returned NULL where PG returns the
  size, and a missing name returned NULL where PG raises 42P01.

The upstream TAP tests call `pg_relation_filepath('<table>')` to find the
file they corrupt or inspect:

- pg_amcheck 003/004;
- pg_checksums 002;
- pg_basebackup 010;
- pg_combinebackup 010;
- recovery 014/016/018.

## 2. PG (src/backend/utils/adt/dbsize.c)

`pg_relation_filenode` and `pg_relation_filepath` look up the pg_class row.
They return NULL when:

- the OID has no row;
- the relkind lacks storage (RELKIND_HAS_STORAGE covers r, i, S, t, m).

Otherwise the result is `relfilenode`, or the relation mapper's number for a
mapped catalog. The path is `relpathbackend(rlocator, backend,
MAIN_FORKNUM)` (common/relpath.c):

- `global/<n>` for the global tablespace;
- `base/<db>/<n>` for the default tablespace;
- `pg_tblspc/<spc>/PG_18_<catversion>/<db>/<n>` for any other;
- a `t<procnumber>_` prefix on a temporary relation's file name.

The argument is `regclass`, so an unknown literal goes through
`regclassin`: a name resolves on the search path, and a missing one raises
42P01.

## 3. Change

- `catalog.TableRelkind` holds the virtual pg_class builder's relkind rule
  for table-registry relations; the builder now calls it.
  `(*InMemory).IndexRelkind` returns `I` for an index with partition
  children, else `i`.
- `relationStorageForOID` (executor) resolves a table, index or synthetic
  TOAST OID to the RelFileNode goopg stores it under. It refuses three
  cases:
  - InvalidOid;
  - an unknown OID;
  - a relkind without storage.
- `pg_relation_filenode` returns that node's relation number.
  `pg_relation_filepath` returns `storage.RelMainPath`, which is relDir plus
  the number: the file the storage manager opens.
- `resolveRegclassOID` sends a string argument through
  `regIdentifierInput(…, "regclass")`, the shared `regclassin` port, and
  propagates its error. All five dbsize callers take the error.
  `relationFileNodeForOID`, the size functions' resolver, refuses
  InvalidOid.

## 4. Measured against PG 18.3

The same script ran on a scratch PG 18.3 and on goopg. For t, t\_pkey, v, s,
pt, pt1, pti, pt1\_a\_idx and mv, these results are identical:

- NULL-ness of both functions;
- `filenode = relfilenode`;
- the path shape;
- by-name equals by-OID;
- OID 0 and an unknown OID give NULL;
- the 42P01 error text and cursor position.

One row differs: `base/` + pg\_database.oid + `/` + relfilenode equals the
path in PG but not in goopg. goopg's `pg_database` shows the postgres
database as 16384, while its files live under `base/5`. That is the known
three-oid-space split (`0119-0006bv`). The path names the real file.

## 5. Not done (ledgered)

- A temporary relation's path has no `t<procnumber>_` prefix. goopg stores
  temporary relations in the shared layout, so the function names the real
  file.
- The six nailed system-index stubs (pg\_class\_oid\_index and others) are not
  in the OID index registry, so both functions return NULL for them. PG
  returns their filenodes.
- The virtual pg\_class shows `relfilenode = oid` even when
  `Table.RelFileNodeOID` (a cold-start-adopted PG cluster) differs. The
  functions report the RelFileNodeOID storage; pg\_class does not.
- Shared catalogs (pg\_database and others) are not table-registry
  relations, so the functions return NULL for them. PG returns
  `global/<n>`.

## 6. Pin

`internal/postmaster/pg_relation_filepath_test.go`
(TestPgRelationFilepathAndFilenode) checks:

- the storage/NULL split per relkind;
- `filenode = relfilenode`;
- that the path names an existing file in the data directory;
- the name, InvalidOid and unknown-OID forms;
- `pg_relation_size` by name;
- 42P01 for a missing name.

Dropping the relkind check fails it on the view and the partitioned table.
