# 0149-0010 — Port recovery/t/015\_promotion\_pages.pl

Status: in progress \(all\-goopg 1/1; mixed PG runs remain, M0149\-0010b\).

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

## Slice 2: the standby sees replayed DDL \(M0149\-0010a, landed\)

A PG 18.3 cold start on a copy of the goopg standby saw test1 and all its rows, so replay into the heap files was
correct. Two goopg gaps hid the table:

- **No catalog refresh during replay.** The running standby's in\-memory catalog is loaded once at startup.
  StreamReplayer now notes the databases whose system\-catalog relations \(relfilenode below 16384\) a record
  changed, and after the next replayed commit calls `Runtime.RefreshCatalogAfterReplay`, which re\-runs the
  idempotent `loadUserTablesFromHeapForDB` for that database. This stands in for the relcache invalidation PG's
  commit records carry \(XACT\_XINFO\_HAS\_INVALS\).
- **Replayed commits never reached clog.** `ReplayXactCommit` only advanced nextXID. After promotion, a restart's
  implicit\-abort sweep \(`MarkUnknownAsAborted`\) therefore marked the primary's DDL xids aborted and the table
  vanished. ReplayXactCommit and ReplayXactAbort now stamp clog, for the record's subtransactions as well, as
  xact\_redo\_commit's TransactionIdCommitTree does.

With both, TestPort\_Recovery015PromotionPages passes all\-goopg.

## Remaining

- Mixed runs \(M0149\-0010b\): PG 18.3 primary with a goopg standby, and a goopg primary with a PG 18.3 standby. A
  PG standby started on a goopg standby's copy crashed in standby\_redo; that is the first thing to reproduce.
- The refresh only registers new tables: drops, renames and ALTERs replayed on a running standby are not
  reflected until a restart, and indexes, defaults and other catalogs are not refreshed.
