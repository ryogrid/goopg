# 0149-0007 — Port recovery/t/029\_stats\_restart.pl

Status: in progress \(slices 1 and 2 landed; 42/44\).

Upstream: `postgres/src/test/recovery/t/029_stats_restart.pl`, `src/backend/utils/activity/pgstat.c`
\(`pgstat_write_statsfile`, `pgstat_read_statsfile`, `pgstat_restore_stats`, `pgstat_discard_stats`\).

## What the TAP checks

- pg\_stat\_io carries startup IO \(standalone backend WAL writes at init, startup WAL reads\).
- Database, relation and function stats survive a clean restart through `pg_stat/pgstat.stat`.
- An immediate shutdown \(crash\) discards them, and no stats file is left behind.
- An invalid file \(overwritten, or with bytes after the end marker\) discards them.
- Checkpointer and WAL counters and their `stats_reset` survive clean restarts; `pg_stat_reset_shared` resets one
  kind only; a crash restart gives WAL stats a new `stats_reset`.

## Slice 1: the statistics file \(landed\)

- `internal/executor/pgstat_file.go` writes and reads the file in **PostgreSQL 18's exact layout**, so PG reads a
  goopg file and goopg reads PG's \(the M0149 C3 contract cold\-starts PG on a goopg directory\):
  - int32 `PGSTAT_FILE_FORMAT_ID` 0x01A5BCB7;
  - one `'F'` chunk per fixed kind with `write_to_file` \(archiver 136, bgwriter 32, checkpointer 88, IO 51848,
    SLRU 512, WAL 40 bytes\), currently written as zeros;
  - one `'S'` chunk per entry: `PgStat_HashKey` \(16 bytes\) then the kind's data \(database 264, relation 216,
    function 24 bytes\);
  - `'E'`, and end of file.
  - The sizes were measured with a C program compiled against the PG 18.3 server headers
    \(`sizeof(((PgStatShared_X *) 0)->stats)`\); every field is 8 bytes, so field offsets follow declaration order.
- `WritePgStatFile` runs after the shutdown checkpoint of a clean shutdown \(the checkpointer's
  `pgstat_before_server_shutdown`\), through `pgstat.tmp` renamed into place.
- Startup: a crash recovery calls `DiscardPgStatFile` \(`pgstat_discard_stats`\); any other start calls
  `RestorePgStatFile`, which loads the file only when it parses completely with nothing after `'E'`, and removes
  it either way.
- `dbStatsManager` keeps the database entries, which a flush in a database creates, and the database each
  relation and function entry belongs to. The relation and function stores key on the OID alone.
- `pg_stat_have_stats(kind, dboid, objid)` answers from these stores.

## Slice 2: checkpointer and WAL stats \(landed\)

- **pg\_stat\_checkpointer** has PG 18's column names and types \(bigint counters including `num_done`, double
  precision times, timestamptz `stats_reset`\). `xlog.Checkpointer` counts `num_done` \(num\_performed\) and gains
  `ResetStats` and `RestoreStats`. The checkpointer's `'F'` chunk is written from its counters at shutdown, and
  initdb seeds a new checkpointer from the restored chunk.
- **pg\_stat\_wal** has PG 18's five columns. Its row comes from `catalog.PgStatWalRowFunc`: the restored WAL chunk
  plus what the WAL writer appended since startup or the last reset \(`pgStatWalState` in internal/initdb/open.go\).
  The WAL `'F'` chunk carries it across restarts.
- **pg\_stat\_reset\_shared**\(target\) validates the target as PG does; `checkpointer` and `wal` reset their
  counters and set `stats_reset` to now; NULL resets both.
- After a crash nothing is restored, so both kinds start with `stats_reset` = now, as `pgstat_reset_after_failure`
  gives them.

## Remaining slices

- `wal_fpi` and `wal_buffers_full` stay 0; `wal_bytes` counts record payloads, where PG counts whole records.
- Restartpoints are counted in `num_requested`; `restartpoints_*`, `sync_time`, `buffers_written` and
  `slru_written` stay 0.
- pg\_stat\_io rows for the startup process and the standalone \(bootstrap\) backend.
- Database entries hold no counters yet, relation entries only the counters goopg tracks, and the other fixed
  kinds are written as zeros.
