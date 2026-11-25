# 0149-0018 — Port recovery/t/002\_archiving.pl

Status: done \(2026\-11\-25\). `TestPort_Recovery002ArchivingFull`
\(`internal/testport/recovery_002_archiving_test.go`\) ports 002 in three pairings, all passing and ungated: all\-goopg;
a PG 18.3 primary with goopg standbys; a goopg primary with PG 18.3 standbys.

Harness: `tapnode` gains `EnableArchiving`, `EnableRestoring` and `ArchiveDir`, mirroring Cluster.pm's
enable\_archiving and enable\_restoring \(archive\_mode = on with a `cp` archive\_command; a `cp` restore\_command plus
standby.signal or recovery.signal\).

## Slice 1 \(0018a, landed\): the WAL archiver

`xlog.Archiver` \(`internal/access/transam/xlog/archiver.go`\) follows pgarch.c and xlogarchive.c:

- Notify:
  - the WAL writer announces each segment whose last byte has just been fsynced — XLogWrite's finishing\_seg arm,
    XLogArchiveNotifySeg — as `pg_wal/archive_status/<seg>.ready`;
  - a promotion announces the new timeline's history file the same way;
  - segments already complete when the server starts are not re\-announced.
- Archive:
  - `pgarch_readyXlog` order: timeline history files first, then the oldest segment;
  - archive\_command runs in the data directory, with `%p` = `pg_wal/<file>`, `%f` = the file name and `%%` = `%`;
  - on exit 0 the status file becomes `.done` and pg\_stat\_archiver counts it;
  - a failure logs PG's WARNING \("archive command failed with exit code N", DETAIL "The failed archive command
    was: …"\), counts as failed, and is retried 3 times one second apart before the archiver waits;
  - the archiver wakes on a notify, on a configuration reload \(pgarch's latch on SIGHUP\), and every 60 seconds;
  - an empty archive\_command with archive\_mode on gives PG's "archive\_mode enabled, yet archiving is not
    configured" warning;
  - archive\_mode = on archives only outside recovery; always archives in recovery too.
- Keep: segment removal keeps a segment that is not `.done` and recreates a missing `.ready`
  \(XLogArchiveCheckDone\). A removed segment's status files are deleted \(XLogArchiveCleanup\).
- Standby: with archive\_mode = on, received segments are marked `.done` \(XLogArchiveForceDone in the walreceiver\),
  and nothing is held back during recovery.
- Statistics: pg\_stat\_archiver reads `activity.ArchiverStatsRow`.
- Settings: archive\_mode \(postmaster, off/on/always\), archive\_command, archive\_library \(accepted, unused\),
  archive\_timeout \(accepted, unused\), wal\_retrieve\_retry\_interval.

Not done:

- archive\_library modules;
- archive\_timeout's forced switch;
- the `.partial` segment and history file at a timeline switch on the archive\-recovery path, where the archiver
  starts after the promotion;
- re\-announcing a segment that completed just before a crash, before its status file was written;
- pg\_stat\_reset\_shared\('archiver'\).

## Slice 2 \(0018b, landed\): a standby restoring from the archive

- With standby.signal, restore\_command and no primary\_conninfo, the standby runs `runArchiveFeeder`
  \(`cmd/goopg/standby_archive.go`\) in the walreceiver's slot. This is the XLOG\_FROM\_ARCHIVE source of PG's
  WaitForWALToBecomeAvailable.
- Each round:
  - take the segment that holds the next WAL byte \(the writer's WrittenLSN, on the writer's timeline\);
  - restore it with restore\_command into `pg_wal/RECOVERYXLOG` \(`%p`; `%r` is the segment of pg\_control's
    checkpoint redo\);
  - check its size and log "restored log file … from archive";
  - append the bytes the local WAL lacks through `AppendRaw`, the same entry the walreceiver uses, so the continuous
    replayer reads them;
  - remove RECOVERYXLOG.
- When the archive does not have the segment yet, the feeder waits wal\_retrieve\_retry\_interval. A FATAL restore
  failure \(a signal, an unrunnable command, a wrong size\) stops it.
- At promotion it makes a last pass, as PG's startup process reads all available WAL before it finishes recovery.
- Received segments are marked `.done` by the archiver hook \(archive\_mode = on in recovery\).
- Not done:
  - the alternation with streaming when both sources are configured: PG starts from the archive and switches to the
    stream, then back on failure; goopg uses one or the other;
  - timeline following from the archive \(history files, recovery\_target\_timeline = latest\);
  - restore\_command for missing segments at crash recovery on a standby.

## Slice 3 \(0018c, landed\): recovery commands and the promotion timeline

- Working directory: archive\_cleanup\_command \(the restartpoint's last step\) and recovery\_end\_command \(end of
  archive recovery\) run in the data directory, as from PG's postmaster after ChangeToDataDir
  \(`ExecuteRecoveryCommandIn`\). A relative path in the command therefore names a file there.
- Warning text: a failure logs PG's WARNING, `<name> "<command>": <wait_result_to_str>`.
- Restore log: every successful restore logs "restored log file "<file>" from archive" at LOG, like
  RestoreArchivedFile.
- New timeline at promotion: PG takes findNewestTimeLine\(recoveryTargetTLI\) \+ 1. Under archive recovery,
  existsTimeLineHistory restores each `<tli>.history` from the archive into RECOVERYHISTORY, falling back to pg\_wal.
  - `xlog.FindNewestTimelineArchive` does the same, and a standby with restore\_command uses it at promotion.
  - Before this, a standby fed only from the archive did not see that a sibling had already promoted onto timeline
    2, and reused it. Now it takes 3.
- Promotion removes RECOVERYXLOG and RECOVERYHISTORY \(exitArchiveRecovery\).
- Harness: `Node.LogSize` / `LogFrom` read a node's log from an offset. A goopg node's structured lines \(slog's
  `level=WARN msg="…"` and the log package's `… WARN …`\) render in PG's stderr form `WARNING:  <message>`, so
  upstream patterns such as `qr/WARNING:.*recovery_end_command/` apply to both flavours. The message wording itself is
  the server's.
- Not done:
  - the archive\-recovery \(recovery.signal\) promotion still takes its new timeline from the archive scan
    `RunArchiveRecovery` makes, not from a probe at the end;
  - restored history files are not kept in pg\_wal for cascading standbys \(restoreTimeLineHistoryFiles\) on the
    standby path.

## M0149\-0027 — the archiver process across start\-time archive recovery \(028\_pitr\_timelines\)

- The archiver holds a pg\_stat\_activity slot \(`activity.ArchiverIdx`, backend\_type `archiver`\) from the first pass
  where archiving is allowed — archive\_mode = on outside recovery, or always \(PgArchStartupAllowed\) — until shutdown.
  `ArchiverConfig.OnStart` / `OnStop` bracket it; a promotion's history\-file notification wakes the archiver, which then
  starts.
- `attachWALArchiver` \(cmd/goopg\) constructs the archiver and attaches it to the WAL writer once. Start\-time archive
  recovery calls it before its promotion, so the new timeline's history file and the segments the new timeline
  completes get their .ready. XLogArchiveNotify writes the status file whether or not the archiver process runs yet.
  Before this, the notification went to a nil archiver and was dropped.
- ShutdownXLOG: a primary with archive\_mode on or always rotates the last segment \(`Writer.SwitchSegment`,
  RequestXLogSwitch\) before the shutdown checkpoint, so every record but the checkpoint is archived.
- `adoptRestoredWAL` skips an empty timeline span. A PITR that stops on timeline 1 before the target timeline 2 forked
  writes "2 <EndOfLog>" with EndOfLog below the 1→2 switch point, as PG's writeTimeLineHistory\(newTLI,
  recoveryTargetTLI, EndOfLog\) does. tliOfPointInHistory never lands on timeline 2 there, so the restored WAL goes
  straight from timeline 1 to 3.
- Not done: promotion does not archive the old timeline's last segment as `.partial` \(M0149\-0096\).

## Open \(children in fix\_plan\)

None.
