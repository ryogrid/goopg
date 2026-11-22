# 0149\-0003 — recovery/t/014\_unlogged\_reinit.pl: unlogged relations after a crash

Status: in progress \(2026\-11\-23\). TestPort\_Recovery014UnloggedReinit is ported; it passes 5/27 assertions
all\-goopg and is listed in ci/batch/expected\-failures.csv until it passes.

## The test

- It creates, in the default tablespace and in an external tablespace:
  - an unlogged table and an unlogged sequence;
  - a sequence rewritten by ALTER SEQUENCE;
  - an identity sequence reset by TRUNCATE ... RESTART IDENTITY.
- It checks that every unlogged relation has a main fork and an `_init` fork.
- It crashes the server, writes fake `_vm` / `_fsm` forks and removes the main forks.
- After a restart, ResetUnloggedRelations \(storage/file/reinit.c\) must have:
  - removed every fork but `_init` before redo;
  - copied `_init` over the main fork after redo.
  Each sequence then restarts at its initial value.

## Landed: external tablespaces

- `CREATE TABLESPACE ... LOCATION '/abs/path'` used to be refused with "tablespaces with an external location are not
  supported". `storage.CreateTablespaceDirectories` ports create\_tablespace\_directories \(commands/tablespace.c\):
  - chmod the location to 0700; a missing location is 58P01 `directory "..." does not exist`;
  - create `PG_<major>_<catversion>` inside it; if it already exists, 55006 "already in use as a tablespace";
  - symlink `pg_tblspc/<oid>` to the location.
- Relation paths \(`pg_tblspc/<oid>/<version>/<db>/<relfilenode>`\) resolve through the link, so no file relocation
  code was needed.
- tblspc\_redo uses the same helper in recovery mode, with the record's path. DROP TABLESPACE and its redo use
  `DestroyTablespaceDirectories`: the version directory and the link go, the location stays.
- CREATE TABLE ... TABLESPACE created the empty main fork under base/ before the clause was applied; it now moves
  into the tablespace \(`placeTableInTablespace`\).
- tapnode's ColdStartPG copies each external tablespace with the data directory and re\-points the copy's link, so
  the PG cross\-check never shares goopg's files.
- Regress tablespace 171 → 164 mismatch lines.

## Remaining \(the next slices\)

- Init forks:
  - CREATE UNLOGGED TABLE: heap\_create\_init\_fork, an empty `_init` file plus XLOG\_SMGR\_CREATE for INIT\_FORKNUM.
  - Its indexes: ambuildempty, a metapage in `_init`, logged with log\_newpage.
  - Unlogged sequences: fill\_seq\_with\_data writes the initial page to `_init` as well.
- ResetUnloggedRelations at crash start:
  - UNLOGGED\_RELATION\_CLEANUP before redo;
  - UNLOGGED\_RELATION\_INIT after redo, which copies `_init` to the main fork and fsyncs.
  - It must cover base/ and every pg\_tblspc/<oid>/<version>.
- Sequence state after the reset: goopg must take nextval's state from the reset page, not from memory or WAL.
- goopg fails to start after the test removes an unlogged main fork; the reset above has to run first.
- Unlogged data is still WAL\-logged. PG skips it \(RelationNeedsWAL\). The test does not observe this.
