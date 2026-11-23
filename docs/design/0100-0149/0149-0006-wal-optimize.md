# 0149\-0006 — recovery/t/018\_wal\_optimize.pl: crash recovery after WAL\-skipping operations

Status: in progress \(2026\-11\-23\). TestPort\_Recovery018WalOptimize passes 40/42 assertions, 20/21 at each
wal\_level. Only check\_orphan\_relfilenodes fails. The test is listed in ci/batch/expected\-failures.csv.

## The test

It runs one sequence at wal\_level = minimal and again at replica, each step followed by a crash and a check:

- SET TABLESPACE;
- TRUNCATE alone, with INSERT, with COPY, with triggers, and with PREPARE TRANSACTION;
- savepoints around SET TABLESPACE, rolled back, released and nested;
- a hint\-bit case on a unique index after ROLLBACK TO;
- triggers firing during COPY.

At the end it checks that every file under base/<db> is a pg\_class relation, and the reverse.

## Fixes \(each named by a failing assertion or a startup failure\)

- **GUCs.** `wal_log_hints` \(bool, postmaster\) and `wal_skip_threshold` \(kB, user\) are registered with PG's
  defaults. goopg WAL\-logs every relation change at every wal\_level and never skips WAL, so minimal behaves like
  replica.
- **COPY DELIMITER.** The text reader split on tab whatever DELIMITER said, in every syntax and both from a file and
  from STDIN.
  - `DecodeCopyTextRowDelim` splits on the option.
  - The writer, its sibling, uses `EncodeCopyTextRowDelim`: it separates fields with the delimiter, escapes the
    delimiter inside values \(CopyAttributeOutText\) and writes the NULL string. Verified byte for byte against PG.
- **Subtransactions in commit and abort records.** Commit records listed no subxacts, so a subtransaction's writes
  had no committed status after a crash. Catalog rows written by an ALTER inside a released savepoint vanished.
  - `Manager.SubxidsOf` now feeds XACT\\_XINFO\\_HAS\\_SUBXACTS: the commit lists the committed children, the abort
    all of them.
  - ROLLBACK TO writes an abort record for the subtransaction and stamps it aborted in clog
    \(RecordTransactionAbort with isSubXact\).
- **Aborted subtransactions after a restart.** The rolled\\-back set lived in memory, but the parent links persist, so
  a rolled\\-back subxid resolved to its committed parent and its rows came back. `Manager.IsAborted` now also trusts
  clog, as TransactionIdDidCommit does.
- **Catalog row stamps inside a savepoint.** `stampCatalogRowsTuple` stamped the superseded row with the top\\-level
  xid while the new version carried the subxid. A ROLLBACK TO therefore left the old row deleted by a committed
  transaction, and the table disappeared after a restart.
  - It now stamps with `effectiveWriterXID`.
  - It no longer sets HEAP\\_XMAX\\_COMMITTED for a subtransaction's stamp, since PG trusts hint bits.
- **Unique checks after ROLLBACK TO.** `isLiveForUniqueCheck` treated a row inserted by our own rolled\\-back
  subtransaction as live, so re\\-inserting the key failed with 23505. A delete by a rolled\\-back subtransaction now
  leaves the row live; a delete by our own live subtransaction removes it.
- **PL/pgSQL `NEW.col` substitution** matched `NEW.id` inside `NEW.id2`. `replaceRecordRef` now requires identifier
  boundaries.
- **SET TABLESPACE is transactional.**
  - The old file is removed at commit and listed in the commit record.
  - A ROLLBACK, or a ROLLBACK TO the savepoint that made the move, restores the tablespace in memory and removes the
    copy \\(`deferTablespaceMove`, `BasicSession.RecordSavepointUndo`\\).
- **`pg_relation_filepath` / `pg_relation_filenode` for system relations.** They returned NULL for every seeded
  relation; they now read its pg\\_class seed row \\(`catalog.SeedRelationStorage`\\).

## Orphan check: fixed in slice 2

- **`base/<db>/0`.** The planner's relation sizer called `pool.NBlocks` with relfilenode 0 for a function scan, and the
  smgr open \(O\_CREATE\) created the file. `SetRelationSizer` now returns "unknown" for RelOid 0.
- **Shared-catalog indexes under base/<db>.** The per-database index loop wrote 2697, 2698, 6114 and 6115 into every
  database directory. It now skips every OID in the global relmap \(`isSharedRelOID`\).
- **Stale placeholder heaps.** 2614, 3765, 3768, 6003, 6101, 6137 and 9400 were removed from
  `mappedLocalCatalogPlaceholderOIDs`; none is a relfilenode PG 18 keeps in base/<db>. A fresh goopg base/5 now holds
  exactly PG's 224 files, and PG 18.3 still cold\-starts on the directory \(014/034/036/037 pass\).
- **TOAST heaps at CREATE TABLE.** `createToastStorage` creates the empty TOAST heap file with the table, as PG's
  `heap_create` does, and DROP TABLE removes it. It is skipped for temporary tables \(see below\).

## Remaining: check\_orphan\_relfilenodes

Three files are still orphans at both wal\_levels: `base/5/16860`, `base/5/16865` and `base/5/9410`.

- The temporary table's files are named `<oid>` where PG uses `t<procno>_<oid>`, and they survive the crash; PG's
  RemovePgTempFiles removes them at startup. Fixing this needs a backend id in the relation file locator
  \(filed M0149-0006a\).
- 9410 is the goopg\-private relation\-stats sidecar \(`catalog.GoopgRelStatsRelationId`\), stored in base/<db> under
  owner waiver M0125-0029. Moving it is owner\-gated \(filed M0149-0006b\).
