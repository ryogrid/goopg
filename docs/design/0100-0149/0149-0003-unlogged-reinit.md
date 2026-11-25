# 0149\-0003 — recovery/t/014\_unlogged\_reinit.pl: unlogged relations after a crash

Status: landed 2026\-11\-23. TestPort\_Recovery014UnloggedReinit passes 27/27 assertions all\-goopg and on a PG
18\.3 cold start of the crashed goopg directory \(M0149 C3\).

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

## Landed: unlogged relations \(slice 2\)

- **Init forks.**
  - An unlogged heap gets an empty `_init` file logged with XLOG\_SMGR\_CREATE for INIT\_FORKNUM
    \(heap\_create\_init\_fork; `createHeapInitFork`\).
  - An index on an unlogged table gets an empty btree, metapage included, in its init fork
    \(btbuildempty; `buildEmptyIndexInitFork`\).
  - An unlogged sequence's init fork receives fill\_seq\_with\_data's page, logged as XLOG\_SEQ\_LOG. This
    happens at CREATE, ALTER and RESTART \(definition rewrites\). setval changes only the main page, as do\_setval
    does.
  - An unlogged table's serial or identity sequence is unlogged too \(generateSerialExtraStmts\).
  - Replay of XLOG\_SMGR\_CREATE for an init fork creates an empty file, as smgr\_redo does.
- **ResetUnloggedRelations** \(`storage/reinit.go`, a port of reinit.c\) runs around redo whenever startup is in
  recovery:
  - cleanup before redo removes every fork but `_init` of a relation that has one;
  - init after redo copies `_init` over the main fork and fsyncs;
  - both passes cover base/ and every pg\_tblspc/<oid>/<version>;
  - a standby skips the init pass, which PG runs at the end of recovery;
  - the storage manager's open handles are closed first, so no cached block count survives the copy.
- **No WAL for unlogged forks** \(RelationNeedsWAL\).
  - The buffer pool keeps a set of unlogged relations. CREATE registers them, and startup re\-registers them from
    the catalog.
  - Writes to their main, FSM and VM forks emit no change record, FPI, smgr\-create or truncate record; the init
    fork is still logged.
  - This was a proven requirement: PG's init pass creates the main fork with O\_EXCL and stopped with `could not
    create file ...: File exists` after redo had recreated it from goopg's WAL.
- **XLOG\_SEQ\_LOG replay** takes the fork from block reference 0 and creates block 0 when the file is empty, as
  XLogInitBufferForRedo does. It used to write the main fork for every record.
- **Sequence state follows PG's page.**
  - goopg kept `current = next value - increment` for a sequence not yet called, so the first nextval moved after
    ALTER ... INCREMENT, and pages carried last\_value = start \- increment.
  - `current` is now last\_value and `called` is is\_called, as on PG's page. nextval returns last\_value unchanged
    when not called.
  - setval, RESTART, ResetSequence and the RESTART IDENTITY rollback \(which now saves is\_called too\) follow.
  - The WAL pre\-log horizon of a not\-yet\-called state covers nothing, so the first nextval is logged.
- **Catalog rows a PG server reads:**
  - ALTER SEQUENCE's non\-HOT pg\_sequence update now inserts the new version's pg\_sequence\_seqrelid\_index
    entry. Without it PG raised "cache lookup failed for sequence".
  - An identity column's sequence dependency is deptype 'i' \(DEPENDENCY\_INTERNAL\), as getIdentitySequence
    requires; serial and OWNED BY stay 'a'.
- **Harness.** PG's redo of XLOG\_TBLSPC\_CREATE points the copy's pg\_tblspc link back at the logged location,
  so a copy cannot isolate an external tablespace. The test snapshots the tablespace with `tapnode.SnapshotDir`
  and restores it between the PG cold start and goopg's own restart.

## Not done \(M0149\-0003a\)

- Index init forks exist only for btree indexes.
- ALTER TABLE SET LOGGED / UNLOGGED neither creates nor removes init forks, and does not change the WAL set.
- The init pass is not run when a standby is promoted.
- DROP leaves the relation in the pool's unlogged set; the relfilenode is not reused.
- After a clean restart a sequence resumes at its WAL pre\-log horizon. PG resumes from the page flushed at the
  shutdown checkpoint.
- A sequence page written by an older goopg for a never\-called sequence holds last\_value = start \- increment, and
  now reads that way.
- XLOG\_SEQ\_LOG's encoder writes spcOid 1663 for every sequence.
