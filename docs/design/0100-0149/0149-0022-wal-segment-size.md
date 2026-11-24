# 0149-0022 — Configurable WAL segment size \(045 prerequisite\)

Status: 0022a done \(2026\-11\-25\); 0022b and 0022c open. The 045 port
`TestPort_Recovery045ArchiveRestartpointFull` \(`internal/testport/recovery_045_archive_restartpoint_test.go`\) is
gated by `GOOPG_TAP_045` until all three are done.

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

## 0022b, 0022c — open

- 0022b: goopg runs start\-time archive recovery before its listener and turns `recovery_target_action = pause`
  into a shutdown. PG instead pauses at the end of recovery ready for read\-only connections, with
  pg\_is\_wal\_replay\_paused\(\) true, and pg\_wal\_replay\_resume\(\) promotes.
- 0022c: a PG 18.3 node restoring a goopg primary's 045 archive never becomes ready; not yet diagnosed.
- Oracle note: 045 needs a record in the target's segment. On PG that is the bgwriter's RUNNING\_XACTS
  \(LogStandbySnapshot every 15 s\), written during the 320 checkpoints and archived by the final pg\_switch\_wal. A
  short loop makes real PG fail with "recovery ended before configured recovery target was reached" too.

Not done in 0022a: pg\_settings' wal\_segment\_size row is still the static 16777216, and initdb writes
min/max\_wal\_size only when `--wal-segsize` is given \(PG always writes them\).
