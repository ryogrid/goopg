# 0149-0011 — Port recovery/t/016\_min\_consistency.pl

Status: done.

Upstream: `postgres/src/test/recovery/t/016_min_consistency.pl`; xlog.c `UpdateMinRecoveryPoint`, bufmgr.c
`FlushBuffer` during recovery.

## What the test checks

A standby with 128 kB shared\_buffers replays two full\-table UPDATEs \(full\-page writes, evictions\), takes a
restartpoint and is stopped cleanly while the primary is stopped immediately. Reading the table's relation file
directly, no page may carry a pd\_lsn past the standby's `Minimum recovery ending location` in pg\_control.

## Port

- `TestPort_Recovery016MinConsistency` runs the `.pl` body three times, through the tapnode streaming harness built
  for 015 \(0149\-0010\): all\-goopg, PG 18.3 primary with a goopg standby, and goopg primary with a PG 18.3 standby.
- The `.pl` compares the two `%X/%X` strings with Perl's `ge`. The port parses them and compares the LSNs, which is
  the check's intent.
- All three pairings pass with no product change: goopg's minRecoveryPoint is updated past every flushed page, and
  the run logs both values \(for example minRecoveryPoint 0/1293A80, largest page LSN 0/1293A58\).
