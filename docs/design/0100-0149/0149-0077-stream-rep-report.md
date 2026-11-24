# 0149-0077 — Re-port recovery/t/001\_stream\_rep.pl to the M0149 contract

Status: in progress. `TestPort_Recovery001StreamRepFull`
\(`internal/testport/recovery_001_stream_rep_test.go`\) ports all of 001 in three pairings. It is gated by
`GOOPG_TAP_001=1` until it passes; the shortcut `TestPort_Recovery001StreamRep` stays until then.

## Shape

A primary, `standby_1` streaming from it, and `standby_2` cascading from `standby_1`, both built from base backups. The
second backup is taken from the standby while the primary is down. The test then checks:

- streamed table, sequence and unlogged\\-sequence content; read\\-only refusals on standbys;
- `target_session_attrs` routing \\(libpq reads `in_hot_standby` and `default_transaction_read_only` from
  ParameterStatus\\);
- SHOW and `READ_REPLICATION_SLOT` on replication connections;
- walsender and walreceiver `pg_stat_io` WAL statistics;
- physical slots on the primary and on the cascading standby, and `hot_standby_feedback` xmin on both;
- a `primary_conninfo` change applied by reload;
- slot advancing persisted across a restart, with the old segment recycled;
- `pg_backup_start` + `BASE_BACKUP` in one session, and `BASE_BACKUP` cancellation \\(`pg_stat_progress_basebackup`\\).

## Slice 1 \\(landed\\)

- The port, and tapnode helpers:
  - `Host`, `Port`, `User`;
  - `PSQLConnstr` \\(psql `--dbname connstr`\\);
  - `PSQLCommands` / `PSQLCommandsCmd` \\(`--command` lists, foreground or background\\);
  - `AdvanceWAL` \\(Cluster.pm `advance_wal`\\);
  - the client environment `PGDATABASE=postgres`, `PGUSER=<bootstrap superuser>`, as Cluster.pm has.
- `pg_sequence_last_value(regclass)`: last\\_value when is\\_called, else NULL. NULL also for another session's temporary
  sequence and for an unlogged sequence during recovery. A non\\-sequence gives `cannot open relation` with
  errdetail\\_relkind\\_not\\_supported, as sequence.c does.

## Open \\(children in fix\\_plan\\)

- 0077a: a goopg standby shows no row for `SELECT * FROM seq1`; sequence state is in memory and not replayed.
- 0077b: a goopg standby accepts INSERT and reports `in_hot_standby` / `transaction_read_only` off.
- 0077c: `SHOW port` reports 5432, not the listening port.
- 0077d: the walsender records no `pg_stat_io` WAL reads.
- 0077e: on a PG 18.3 standby of a goopg primary, reading an unlogged sequence fails with "could not open file".
- 0077f: a goopg standby does not serve a cascading standby \\(PG\\-primary pairing\\).
