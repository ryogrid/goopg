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
