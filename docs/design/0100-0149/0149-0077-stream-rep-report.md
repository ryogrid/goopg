# 0149-0077 — Re-port recovery/t/001\_stream\_rep.pl to the M0149 contract

Status: done \(2026\-11\-25\). `TestPort_Recovery001StreamRepFull`
\(`internal/testport/recovery_001_stream_rep_test.go`\) ports all of 001 in three pairings, all passing and ungated:
all\-goopg; a PG 18.3 primary with goopg standbys; a goopg primary with PG 18.3 standbys. The earlier smoke
`TestPort_Recovery001StreamRep` remains.

## Shape

A primary, `standby_1` streaming from it, and `standby_2` cascading from `standby_1`, both built from base backups. The
second backup is taken from the standby while the primary is down. The test then checks:

- streamed table, sequence and unlogged\-sequence content; read\-only refusals on standbys;
- `target_session_attrs` routing \(libpq reads `in_hot_standby` and `default_transaction_read_only` from
  ParameterStatus\);
- SHOW and `READ_REPLICATION_SLOT` on replication connections;
- walsender and walreceiver `pg_stat_io` WAL statistics;
- physical slots on the primary and on the cascading standby, and `hot_standby_feedback` xmin on both;
- a `primary_conninfo` change applied by reload;
- slot advancing persisted across a restart, with the old segment recycled;
- `pg_backup_start` + `BASE_BACKUP` in one session, and `BASE_BACKUP` cancellation \(`pg_stat_progress_basebackup`\).

## Slice 1 \(landed\)

- The port, and tapnode helpers:
  - `Host`, `Port`, `User`;
  - `PSQLConnstr` \(psql `--dbname connstr`\);
  - `PSQLCommands` / `PSQLCommandsCmd` \(`--command` lists, foreground or background\);
  - `AdvanceWAL` \(Cluster.pm `advance_wal`\);
  - the client environment `PGDATABASE=postgres`, `PGUSER=<bootstrap superuser>`, as Cluster.pm has.
- `pg_sequence_last_value(regclass)`: last\_value when is\_called, else NULL. NULL also for another session's temporary
  sequence and for an unlogged sequence during recovery. A non\-sequence gives `cannot open relation` with
  errdetail\_relkind\_not\_supported, as sequence.c does.

## Slice 2 \(0077a, landed\): sequences on a goopg standby

- A goopg sequence's counter lives in the sequence registry, and `SELECT * FROM seq` reads it through the table's
  `VirtualRows`. On a standby:
  - the heap catalog reload registered the relkind S relation first, and `CreateSequenceCatalogRelation` then
    returned on "already exists", so the relation had no reader. It now wires the reader onto the existing relation;
  - `XLOG_SEQ_LOG` replay \(seq\_redo\) rebuilt the page but never touched the registry. `StreamReplayer` now has a
    sequence hook, and `Runtime.RefreshSequenceAfterReplay` reads block 0 of the relation and sets the counter. The
    standby's catalog refresh after a commit also reloads sequences, so a newly created sequence registers.
- `SELECT * FROM seq` shows the page tuple, as PG's `read_seq_tuple` does. That is the logged\-ahead `last_value` and
  its `log_cnt` \(`33|0|t` after one nextval\), not the primary's live counter. It does so until this server calls
  nextval or setval itself \(`SetSequencePageState`\). The startup reload uses the same path, so a restarted primary
  also shows the page, like PG.

## Slice 3 \(0077b, landed\): a read\-only hot standby

- During recovery PG forces every transaction read\-only, and ExecCheckXactReadOnly refuses data\-modifying statements
  with "cannot execute <CMD> in a read\-only transaction" \(25006\). goopg applied the standby's INSERT. The INSERT,
  UPDATE, DELETE, MERGE and upsert operators now check `ctx.IsStandby` in Open \(`preventCommandDuringRecovery`\).
  The flag is evaluated per statement, so promotion lifts it.
- A standby session reports `in_hot_standby=on` in its startup ParameterStatus block. libpq's
  `target_session_attrs` uses it, with `default_transaction_read_only`, to classify the server.
- Not done \(M0149\-0084, a nearby task\):
  - read\-only transactions on a primary, and the `transaction_read_only` GUC;
  - DDL refusal on a standby;
  - re\-reporting `in_hot_standby` at promotion.

## Slice 4 \(0077c, landed\): SHOW port

- `goopg start --listen host:port` is goopg's \-h/\-p. The `port` GUC \(PGC\_POSTMASTER\) is now set from the listen port
  with command\-line source, as postmaster's \-p does. libpq's target\_session\_attrs cases compare `SHOW port` with
  the node's port.
- Sequence refresh race: a running standby's catalog refresh after a commit could put an older page read back over
  the counter the sequence replay hook had already set \(1 run in 3\). The refresh now registers only sequences it
  does not know yet; known sequences follow their replayed pages through the hook alone.

## Slice 5 \(0077d, landed\): WAL I/O in pg\_stat\_io

- PG's walsender reads WAL through WALRead, which counts each read in pg\_stat\_io as an IOOP\_READ on IOOBJECT\_WAL,
  IOCONTEXT\_NORMAL. The walreceiver's XLogWalRcvWrite counts each write as an IOOP\_WRITE. goopg's walsender
  serves chunks from its WAL iterator, and the walreceiver appends them to the local WAL. Each now counts one op of the
  chunk's size into the cumulative pg\_stat\_io store \(`executor.CountWALSenderRead` / `CountWALReceiverWrite`\).

## Slice 6 \(0077g, landed\): NULL slot xmins

- pg\_get\_replication\_slots sets the xmin and catalog\_xmin columns to NULL when the xid is not valid. goopg
  printed 0 for a physical slot's xmin and an empty string for catalog\_xmin. Both now render
  `catalog.VirtualNull`, so 001's `xmin IS NULL AND catalog_xmin IS NULL` check passes on the primary's slot and
  on the slot that standby\_2 uses on standby\_1. goopg does not track physical\-slot xmin yet, so it stays NULL
  until hot\_standby\_feedback lands \(0077i\).

## Slice 7 \(0077h, landed\): event triggers on a running standby

- goopg renders pg\_event\_trigger from the in\-memory registry. At startup the registry is loaded from the heap.
  A streaming standby refreshes its catalog after each replayed commit that changed a system catalog
  \(RefreshCatalogAfterReplay, goopg's relcache invalidation\), but that refresh only registered new tables and
  sequences. A trigger created on the primary therefore appeared on a standby only after the standby restarted:
  001 passed on standby\_1, which restarts, and failed on standby\_2, which does not.
- The refresh now rebuilds the registry and the TID cache from the replayed heap's committed rows
  \(`reloadUserEventTriggersFromHeapMode` with sync, `catalog.ReplaceEventTriggersDuringRecovery`\). This is safe
  because no local DDL runs during recovery, so the replayed heap is the only source of truth. The rebuild covers
  CREATE, ALTER \(ENABLE, RENAME, OWNER\) and DROP.

## Slice 8 \(0077i, landed\): hot\_standby\_feedback

- GUC `hot_standby_feedback` \(PGC\_SIGHUP, off\). The walreceiver reads it with each status tick and sends
  XLogWalRcvSendHSFeedback's 'h' message: `'h' | sendTime | xmin | epoch | catalog_xmin | epoch`, epochs 0 because
  goopg xids are 32\-bit. xmin is GetReplicationHorizons: `transam.Manager.ReplicationHorizons` returns the data
  horizon \(snapshots, nextXID, physical slots' xmin\) and the logical slots' catalog\_xmin separately. After the GUC
  is turned off, one message with both values invalid clears the upstream's copy; nothing is sent after that
  \(primary\_has\_standby\_xmin\).
- The walsender's ProcessStandbyHSFeedbackMessage for a slot: `Slots.SetPhysicalXmins` replaces the slot's xmin and
  catalog\_xmin \(PhysicalReplicationSlotNewXmin\) and persists them in slotdata.xmin. pg\_replication\_slots shows
  them. `Slots.MinXmin` is installed as the transaction manager's replication\-slot xmin source, so it floors
  OldestXmin like PG's replication\_slot\_xmin. A cascading standby's own feedback therefore carries its
  downstream slot's xmin.
- Walreceiver restart on reload: primary\_conninfo and primary\_slot\_name are re\-read per connection, and a
  watcher ends the current connection when a reload changes either \(StartupRereadConfig, pendingWalRcvRestart\).
- Not done:
  - feedback without a slot does not hold the horizon \(PG sets the walsender's MyProc\-\>xmin, shown as
    pg\_stat\_replication.backend\_xmin\);
  - TransactionIdInRecentPast is not checked;
  - the slot file is written on every change instead of being marked dirty for the next checkpoint;
  - a reload does not trigger immediate feedback \(the next status tick sends it\).

## Slice 9 \(0077j, landed\): physical slot advance and segment recycling

- `pg_replication_slot_advance(slot_name, upto_lsn)` follows slotfuncs.c:
  - an invalid target \(0/0\) is "invalid target WAL LSN";
  - the move\-to point is min\(target, flush\), or the replay position on a standby;
  - an unknown slot, an active slot, a slot that never reserved WAL, or a target below restart\_lsn each error with
    PG's message;
  - otherwise restart\_lsn moves forward \(`Slots.AdvancePhysical`\) and is persisted at once. PG marks the slot dirty
    and writes it at the next checkpoint.
- Logical slots are refused for now: pg\_logical\_replication\_slot\_advance needs a decoding pass up to the target.
- Retention bug: a slot's restart\_lsn is a PG byte position, but `SlotAwareRetainer` passed it to the writer, whose
  keep LSN is 1\-based. A restart\_lsn exactly on a segment boundary — the result of advancing to
  pg\_current\_wal\_lsn\(\) after pg\_switch\_wal — kept the previous segment as well. The retainer now converts with
  \+1, as KeepLogSeg's XLByteToSeg\(restart\_lsn\) does.

## Slice 10 \(0077k, landed\): session\-level backups

- `pg_backup_start(label, fast)` and `pg_backup_stop(wait_for_archive)` follow xlogfuncs.c. The state is per session,
  keyed by the connection's ProcArray slot, which is fixed for the connection's lifetime — PG keeps it in the
  backend\-local `sessionBackupState` \(`executor/session_backup.go`\).
- Start:
  - refuses a second backup in the session \("a backup is already in progress in this session"\), labels of
    MAXPGPATH or more, and wal\_level minimal;
  - otherwise checkpoints and returns the redo LSN, converted to a PG byte LSN as BASE\_BACKUP does for
    backup\_label.
- Stop:
  - without a running backup, "backup is not in progress" with the hint "Did you call pg\_backup\_start\(\)?";
  - otherwise it switches to a new WAL segment and returns `(lsn, labelfile, spcmapfile)` as composite text. With
    wait\_for\_archive and archiving off, it emits PG's NOTICE.
- BASE\_BACKUP checks the same state and refuses inside a session with a running backup \(SendBaseBackup\).
- A disconnect without pg\_backup\_stop aborts the backup and logs "aborting backup due to backend exiting before
  pg\_backup\_stop was called".
- Not done:
  - no XLOG\_BACKUP\_END record;
  - no backup history file in pg\_wal;
  - no forcePageWrites while a backup runs;
  - no wait for archiving;
  - no standby\-side restrictions \(do\_pg\_backup\_start in recovery\);
  - the fast flag is ignored, because the checkpoint is always immediate.

## Slice 11 \(0077l, landed\): finding and cancelling a running BASE\_BACKUP

- Progress:
  - `utils/activity/progress_basebackup.go` is the BASEBACKUP slot of PG's command\-progress machinery
    \(pgstat\_progress\_start\_command / basebackup\_progress\_\*\);
  - ReplyBaseBackup registers a slot under the walsender's pid and moves it through the phases: waiting for
    checkpoint, streaming database files, waiting for WAL archiving;
  - the streamer adds each chunk to backup\_streamed and counts finished archives;
  - `pg_stat_progress_basebackup` reads the registry instead of returning no rows. backup\_total stays NULL,
    because goopg makes no size estimate.
- Activity: the postmaster reports every replication command as active with its text, and as idle afterwards
  \(exec\_replication\_command's pgstat\_report\_activity\).
- MAX\_RATE is parsed and validated against 32..1048576 kB/s \(22003, PG's message\). The streamer sleeps until
  the bytes sent fit the rate, like bbsink\_throttle, and wakes early on cancel.
- Cancel: HandleCommand now receives the statement context as well as the connection context, and BASE\_BACKUP runs
  under it. A CancelRequest therefore ends the backup with 57014 "canceling statement due to user request", and the
  session goes on. Streaming commands keep the connection context.
- Result: `TestPort_Recovery001StreamRepFull/all-goopg` passes end to end and is no longer gated.
- Not done:
  - no backup size estimate \(PROGRESS option\);
  - throttling averages over the whole backup instead of PG's per\-sample sleep;
  - a walsender's backend\_type is still "client backend" \(M0149\-0090\).

## Slice 12 \(0077e, landed\): one pg\_class row for an unlogged sequence

- On a PG 18.3 standby of a goopg primary, `pg_sequence_last_value('ulseq')` failed with "could not open file
  base/5/<n>". PG returns NULL for an unlogged sequence during recovery, but only when the relcache entry says
  relpersistence 'u'.
- Cause: the standby's pg\_class held two live rows for ulseq, 'p' and 'u'. CREATE UNLOGGED SEQUENCE, and an
  UNLOGGED table's serial/identity sequence, wrote the catalog rows once through `createSeqCatalogTable` and then ran
  `syncTableToCatalogHeap` again after setting Unlogged. That function is an insert\-only funnel for pg\_type,
  pg\_class, pg\_attribute and their indexes, so the second call inserted a second copy instead of updating.
- Fix: `createSeqCatalogTable` takes the unlogged flag, and the single write carries relpersistence 'u', as
  DefineSequence's heap\_create\_with\_catalog does.
- Result: the goopg\-primary/PG\-standbys pairing of 001 passes end to end and runs ungated.
- The ALTER SEQUENCE … SET LOGGED/UNLOGGED path still re\-runs the insert\-only funnel \(M0149\-0091\).

## Slice 13 \(0077f, landed\): cascading from a goopg standby of a PG primary

- Base backup from a standby:
  - standby\_2 is built from a pg\_basebackup of standby\_1, a goopg standby that had not adopted a restartpoint;
  - the checkpointer had no redo of its own, so BASE\_BACKUP fell back to the end of WAL;
  - the backup started where no WAL existed: pg\_basebackup \-X stream received nothing, backup\_label named an
    unwritten redo, and standby\_2's walreceiver asked for segment 0;
  - do\_pg\_backup\_start in recovery uses pg\_control's checkPoint / checkPointCopy.redo — on a fresh standby, the
    base backup's checkpoint, whose WAL the standby still keeps. BASE\_BACKUP now falls back to pg\_control the same
    way.
- OIDs of virtual catalog relations:
  - virtual pg\_catalog relations registered through `RegisterVirtualTable` without an OID took the next
    user\-range OID: pg\_subscription\_rel 16397, pg\_stat\_wal\_io 16387, and others;
  - a PG primary assigns those same OIDs to user objects, so on the goopg standby `LookupTableByOIDAllDBs` sometimes
    returned the catalog instead of the replayed sequence. Map iteration order decided which, so `SELECT * FROM
    seq1` came back empty in some runs.
  - PG never allocates system relations in the user range. RegisterVirtualTable now takes PG's own OID: the
    seeded pg\_class row for a system catalog, or the pinned system and information\_schema view OIDs through
    `catalog.SystemRelOIDResolver`, which initdb installs.
  - A goopg\-only system view \(pg\_stat\_aio, pg\_stat\_aio\_targets, pg\_stat\_wal\_io\) counts down from 16383,
    below FirstNormalObjectId.
- Result: all three pairings pass 3/3 and the GOOPG\_TAP\_001 gate is gone. The must\-pass regress cases are
  identical to HEAD.

## Open \(children in fix\_plan\)

None. Nearby defects filed during the port: M0149\-0084 to M0149\-0092.
