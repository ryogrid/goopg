(idle — nothing in flight)

Last loop (#53) COMPLETE: M0118-0009 — intra-grant-inplace ENABLER (design 0118-0113,
NOT a promotion). pg_class ROWMARK locking: `SELECT … FROM pg_class WHERE oid=<rel> FOR
{KEY SHARE|NO KEY UPDATE|SHARE|UPDATE}` records a rowmark; ALTER TABLE ADD PRIMARY KEY's
in-place relhasindex update waits on conflicting other-tree holders (FOR KEY SHARE does
NOT conflict). New catalog store pgClassRowMarks + lockRowsOp.maybeRecordPgClassRowMark +
execAlterTableAddPrimaryKey→waitForPgClassRowMarks. Perms 2-6 now byte-identical (first
divergence L62→L141). Committed + pushed.

Remaining M0118-0009 (Effort-L unbuilt subsystems):
- intra-grant-inplace perms 7-10: GRANT/REVOKE/DELETE FROM pg_class must take a LockTuple
  on the virtual pg_class row + AWAIT a conflicting rowmark's xmax + deadlock detection
  on virtual tuples (perm8 is an intentional deadlock). Runtime shared-catalog
  MVCC-tuple-lock + deadlock-detection-on-virtual-rows core.
- stats: needs the cumulative pg_stat_* subsystem (pg_stat_force_next_flush,
  pg_stat_get_function_*, track_functions, stats_fetch_consistency, pg_stat_slru).
Other failing M0118 specs (distinct unbuilt subsystems): index-only-bitmapscan
(EXPLAIN DECLARE CURSOR + BitmapOr plan), predicate-gin/gist (GIN/GiST AMs),
deadlock-parallel (lock-group abstraction), fk-partitioned-1/2 (ATTACH PARTITION +
partitioned-FK enforcement; probe shows "table pfk1 does not exist").
