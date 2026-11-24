# 0149-0022 — Configurable WAL segment size \(045 prerequisite\)

Status: done \(2026\-11\-25\). `TestPort_Recovery045ArchiveRestartpointFull`
\(`internal/testport/recovery_045_archive_restartpoint_test.go`\) passes on all three pairings and is ungated. Its only
blocker was 0022a. The restore node is a standby: Cluster.pm init\_from\_backup's has\_restoring defaults to
`standby => 1`, as the real upstream run shows \("entering standby mode", "waiting for WAL to become available"\). At
the end of the archive it waits for more WAL, and pg\_last\_wal\_replay\_lsn\(\) reaches the segment boundary after the
last XLOG\_SWITCH.

045 initialises its primary with 1 MB WAL segments \(`--wal-segsize 1`\), so 320 checkpoint\-and\-switch rounds archive
320 small segments. goopg could only run 16 MB segments.

## 0022a — wal\_segment\_size

- **initdb**: `goopg init --wal-segsize N` takes PG's power\-of\-two check \(1–1024 MB\). It writes N into
  pg\_control's xlog\_seg\_size \(offset 228\) and writes a bootstrap segment of that size. The bootstrap checkpoint
  sits at segment 1's first byte plus the long page header, 0/100028 at 1 MB. It also seeds min\_wal\_size and
  max\_wal\_size as `pretty_wal_size(DEFAULT_MIN_WAL_SEGS = 5)` and `(DEFAULT_MAX_WAL_SEGS = 64)`.
- **One process\-wide size**: PG keeps wal\_segment\_size as a global read from pg\_control at startup. goopg now has
  the same, `xlog.WALSegmentSize()`. initdb.Open adopts the size from pg\_control \(`ControlFileData.XLogSegSize`\)
  and the WAL writer is built with it. FormatSegmentNameTLI used to hard\-code 16 MB; it now names by this size, and
  so does every `segSize == 0` fallback \(readers, iterator, walsender, walreceiver, basebackup, summarizer,
  checkpointer, archive restore\). Init sets it for its own process, 16 MB when the flag is absent. Unit tests that
  pass explicit small segment sizes are unaffected: names of low segment numbers are the same under any size up to
  16 MB.
- **highestLocalSegment** read only a file name's low 8 hex digits, which wraps every 4 GB of WAL. That is 4096
  segments at 1 MB. It now parses the absolute segment number.
- **SHOW wal\_segment\_size** reports the size from pg\_control \(`1MB`\).

## Not 045 blockers \(filed\)

The port first restored with recovery.signal, so two gaps looked like 045 blockers. Neither is one. Both stay
filed as PG behaviour goopg lacks:

- 0022b: goopg runs start\-time archive recovery before its listener and turns `recovery_target_action = pause` into
  a shutdown. PG instead pauses at the end of recovery ready for read\-only connections, and
  pg\_wal\_replay\_resume\(\) promotes.
- 0022c: an idle goopg primary writes no XLOG\_RUNNING\_XACTS between checkpoints. PG's bgwriter writes one every
  15 s \(LOG\_SNAPSHOT\_INTERVAL\_MS\) after important WAL. With archive recovery to a target at a switch boundary, this
  is what puts a record in the target's segment; without it real PG fails too, with "recovery ended before
  configured recovery target was reached".
- Harness: tapnode's psql now runs under PostgreSQL::Test::Utils' $timeout\_default \(180 s\). cluster.PSQL's 30 s
  cut off the 320\-checkpoint loop intermittently.

Not done in 0022a: pg\_settings' wal\_segment\_size row is still the static 16777216, and initdb writes
min/max\_wal\_size only when `--wal-segsize` is given \(PG always writes them\).
