# 0149-0019 — Port recovery/t/020\_archive\_status.pl

Status: done \(2026\-11\-25\). `TestPort_Recovery020ArchiveStatusFull`
\(`internal/testport/recovery_020_archive_status_test.go`\) ports 020 in three pairings, all passing and ungated:
all\-goopg; a PG 18.3 primary with goopg standbys; a goopg primary with PG 18.3 standbys.

020 exercises the archiver built for 002 \(docs/design/0100\-0149/0149\-0018\-archiving.md\): failed archive\_command
counts in pg\_stat\_archiver, `.ready` kept across crash recovery and a cold backup, standbys with archive\_mode = on
\(backup `.ready` files dropped, restored segments `.done`\) and archive\_mode = always \(`.ready` kept and created,
archived from the standby\). These held already. The gaps were around them:

- **pg\_wal\_lsn\_diff**: the function did not exist. It is pg\_lsn\_mi \(lsn1 − lsn2 as numeric\), the same
  arithmetic as the `pg_lsn - pg_lsn` operator, typed numeric for the planner.
- **Replay position after a switch**: XLogReadRecord makes an XLOG\_SWITCH record "extend to end of segment", so
  lastReplayedEndRecPtr — and pg\_last\_wal\_replay\_lsn\(\) — is the next segment boundary once the switch is replayed.
  goopg's StreamReplayer reported the switch record's own end. A standby therefore never reached the LSN
  pg\_switch\_wal\(\) returned until the next segment arrived, which an archive\-fed standby may never get. It now
  reports the boundary, which is also where a promotion right after a switch starts the new timeline.
- **Restored segments**: KeepFileRestoredFromArchive marks a restored segment `.done` \(or `.ready` with
  archive\_mode = always\) as soon as it lands. goopg's archive feeder appended the segment without flushing, and status
  files come from the writer's flush hook. With nothing else flushing — an archive\-only standby of a PG primary — the
  segment got none. The feeder now flushes each restored segment.
- **pg\_stat\_reset\_shared\('archiver'\)**: it accepted the target but reset nothing. It now clears every counter and
  sets stats\_reset; the NULL form resets the archiver too.
- **log\_min\_messages and the archiver's shutdown**:
  - log\_min\_messages was registered but unused; the server log was fixed at Info. A debugN setting now enables
    the server's debug messages, at start and on reload \(`serverLogLevel`\).
  - On shutdown the archiver makes its last pass, then logs the shell archive module's DEBUG1 "archiver process
    shutting down". The server waits for it before exiting, as pgarch exits before the postmaster.

Not done:

- the other log\_min\_messages levels \(notice, error, fatal, panic\) still all show goopg's INFO lines, which are
  PG's LOG messages;
- per\-message level fidelity between goopg's INFO and PG's LOG/INFO.
