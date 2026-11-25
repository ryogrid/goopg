# M0149\-0030 — Port recovery/t/033\_replay\_tsp\_drops.pl

Status: skipped as a detour. TestPort\_Recovery033ReplayTspDrops is written and gated \(`GOOPG_TAP_033`\).

## Result of the first run

- The FILE\_COPY and WAL\_LOG sub\-tests pass on all three pairings. The standby replays creating and dropping
  databases and tablespaces, then restarts from an immediate stop.
- missing\-directory fails on all three pairings. The standby whose `pg_tblspc/<ts1>` was removed must log
  `WARNING: creating missing directory: pg_tblspc/…` when it replays `CREATE DATABASE db2 WITH TABLESPACE ts1
  STRATEGY=FILE_COPY`.
- Even with a PG 18.3 standby a goopg primary fails. pg\_waldump: goopg writes `CREATE_WAL_LOG create dir
  1663/16386` plus FPIs, where PG writes `CREATE_FILE_COPY copy dir 1663/1 to 16384/16385`.

## Path\-to\-TAP

missing\-directory ← M0149\-0030a database default tablespaces \(architecture\-sized: catalog dattablespace, routing
relations with reltablespace 0, template copy into the tablespace, movedb\) ← M0149\-0030b STRATEGY=FILE\_COPY and
XLOG\_DBASE\_CREATE\_FILE\_COPY ← M0149\-0030c dbase\_redo's recovery\_create\_dbdir. 0030c alone would fix the
PG\-primary pairing.

## M0149\-0030c — dbase\_redo's FILE\_COPY arm \(landed\)

goopg read XLOG\_DBASE\_CREATE\_FILE\_COPY as a WAL\_LOG body and only created `base/<db>`. A PG primary's FILE\_COPY
CREATE DATABASE therefore left the goopg standby with an empty database in the wrong place.
`applyDbaseCreateFileCopy` \(`internal/access/transam/xlog/dbase_redo_file_copy.go`\) follows dbcommands.c:

- It decodes xl\_dbase\_create\_file\_copy\_rec \{db\_id, tablespace\_id, src\_db\_id, src\_tablespace\_id\}.
- Paths come from `databasePath` \(GetDatabasePath: global, base/<db>, or pg\_tblspc/<spc>/<version dir>/<db>\).
- An existing target directory is removed first \(WARNING "some useless files may be left behind…" on failure\).
- `recoveryCreateDBDir` is recovery\_create\_dbdir. A missing parent of the target is accepted only under pg\_tblspc/.
  Once recovery is consistent it is created with WARNING "creating missing directory: <path>" when
  allow\_in\_place\_tablespaces is on, and is a fatal "missing directory" when it is off; before consistency it is
  created with DEBUG1. A missing source is created empty.
- The source directory's regular files are copied \(copydir without recursion\). Replay writes through the storage
  manager, so the files are current without a FlushDatabaseBuffers step.

The standby supplies reachedConsistency and the GUC through `xlog.SetDbaseRedoState`. 033's PG\-primary pairing now
passes all three sub\-tests.
