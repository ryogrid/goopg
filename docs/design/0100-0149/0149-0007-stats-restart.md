# 0149-0007 — Port recovery/t/029\_stats\_restart.pl

Status: in progress \(slice 1 landed\).

Upstream: `postgres/src/test/recovery/t/029_stats_restart.pl`, `src/backend/utils/activity/pgstat.c`
\(`pgstat_write_statsfile`, `pgstat_read_statsfile`, `pgstat_restore_stats`, `pgstat_discard_stats`\).

## What the TAP checks

- pg\\_stat\\_io carries startup IO \\(standalone backend WAL writes at init, startup WAL reads\\).
- Database, relation and function stats survive a clean restart through `pg_stat/pgstat.stat`.
- An immediate shutdown \\(crash\\) discards them, and no stats file is left behind.
- An invalid file \\(overwritten, or with bytes after the end marker\\) discards them.
- Checkpointer and WAL counters and their `stats_reset` survive clean restarts; `pg_stat_reset_shared` resets one
  kind only; a crash restart gives WAL stats a new `stats_reset`.

## Slice 1: the statistics file \\(landed\\)

- `internal/executor/pgstat_file.go` writes and reads the file in **PostgreSQL 18's exact layout**, so PG reads a
  goopg file and goopg reads PG's \\(the M0149 C3 contract cold\\-starts PG on a goopg directory\\):
  - int32 `PGSTAT_FILE_FORMAT_ID` 0x01A5BCB7;
  - one `'F'` chunk per fixed kind with `write_to_file` \\(archiver 136, bgwriter 32, checkpointer 88, IO 51848,
    SLRU 512, WAL 40 bytes\\), currently written as zeros;
  - one `'S'` chunk per entry: `PgStat_HashKey` \\(16 bytes\\) then the kind's data \\(database 264, relation 216,
    function 24 bytes\\);
  - `'E'`, and end of file.
  - The sizes were measured with a C program compiled against the PG 18.3 server headers
    \\(`sizeof(((PgStatShared_X *) 0)->stats)`\\); every field is 8 bytes, so field offsets follow declaration order.
- `WritePgStatFile` runs after the shutdown checkpoint of a clean shutdown \\(the checkpointer's
  `pgstat_before_server_shutdown`\\), through `pgstat.tmp` renamed into place.
- Startup: a crash recovery calls `DiscardPgStatFile` \\(`pgstat_discard_stats`\\); any other start calls
  `RestorePgStatFile`, which loads the file only when it parses completely with nothing after `'E'`, and removes
  it either way.
- `dbStatsManager` keeps the database entries, which a flush in a database creates, and the database each
  relation and function entry belongs to. The relation and function stores key on the OID alone.
- `pg_stat_have_stats(kind, dboid, objid)` answers from these stores.

## Remaining slices

- pg\\_stat\\_checkpointer column types \\(int8, float8, timestamptz\\) and the missing `num_done` column; the
  checkpointer chunk written from and restored into `xlog.Checkpointer`; `pg_stat_reset_shared('checkpointer')`.
- pg\\_stat\\_wal counters \\(`wal_records`, `wal_bytes`, ...\\) are fixed zeros; they need real accounting, the WAL
  chunk, and `pg_stat_reset_shared('wal')`. After a crash every fixed kind gets a new `stats_reset`
  \\(`pgstat_reset_after_failure`\\).
- pg\\_stat\\_io rows for the startup process and the standalone \\(bootstrap\\) backend.
- Database entries hold no counters yet, relation entries only the counters goopg tracks, and the other fixed
  kinds are written as zeros.
