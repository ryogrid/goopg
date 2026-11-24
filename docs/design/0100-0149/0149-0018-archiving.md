# 0149-0018 — Port recovery/t/002\_archiving.pl

Status: in progress. `TestPort_Recovery002ArchivingFull` \(`internal/testport/recovery_002_archiving_test.go`\)
ports 002 in three pairings: all\-goopg; a PG 18.3 primary with goopg standbys; a goopg primary with PG 18.3
standbys. The goopg\-primary/PG\-standbys pairing passes and always runs. The two pairings with goopg standbys run
only with `GOOPG_TAP_002=1`.

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

## Open \(children in fix\_plan\)

- 0018b: a goopg standby restoring WAL from the archive \(standby mode with restore\_command\).
- 0018c: archive\_cleanup\_command at a standby restartpoint, recovery\_end\_command at promotion \(including the
  failure WARNING\), recovery commands running in the data directory, and the history\-file restore at promotion
  with RECOVERYHISTORY / RECOVERYXLOG cleanup.
