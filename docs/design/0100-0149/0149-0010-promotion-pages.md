# 0149-0010 — Port recovery/t/015\_promotion\_pages.pl

Status: in progress \(slice 1 landed\).

Upstream: `postgres/src/test/recovery/t/015_promotion_pages.pl`; Cluster.pm `backup`, `init_from_backup`,
`enable_streaming`, `promote`, `wait_for_catchup`, `poll_query_until`.

## What the test checks

A standby replays a VACUUM \(visibility bits, wal\_log\_hints = off\) and a TRUNCATE, is promoted, then truncates,
vacuums and inserts 1000 rows before its first checkpoint, and is crash\-stopped. Crash recovery must not trip over
invalid page references, and `SELECT count(*) FROM test1` must return 1000.

## Slice 1: the streaming harness \(landed\)

internal/testutil/tapnode/streaming.go adds the Cluster.pm surface the test needs:

- `Backup(name)` runs `pg_basebackup --no-sync --checkpoint fast` against the running node, as Cluster.pm's
  `backup` does.
- `InitFromBackupGoopg(root, backup, hasStreaming)` copies the backup into a new goopg node and, with
  hasStreaming, appends `primary_conninfo` \(the root's connstr\) and writes `standby.signal`, as
  `init_from_backup` + `enable_streaming` + `set_standby_mode` do.
- `Promote` runs `pg_ctl promote`, `Restart` is a fast stop plus start, and `PollQueryUntil` polls for 180 s.
- The test's `waitForCatchup` is `wait_for_catchup` in replay mode, over the primary's pg\_stat\_replication.

All of it works against goopg: pg\_basebackup, streaming and pg\_ctl promote succeed.

## Blocker \(M0149\-0010a\)

The run stops at the first statement after promotion: `truncate test1` fails with "relation \"test1\" does not
exist". The table was created on the primary after the standby attached, and it never appears in the goopg
standby's catalog — not before promotion, not after it, and not after a standby restart. Every existing
goopg\-to\-goopg replication test creates its tables before the clone, so the gap was not covered. In PG the
catalog rows arrive as ordinary heap WAL and invalidation records make the relcache see them.
