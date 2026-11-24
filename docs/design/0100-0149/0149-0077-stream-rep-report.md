# 0149-0077 — Re-port recovery/t/001\_stream\_rep.pl to the M0149 contract

Status: in progress. `TestPort_Recovery001StreamRepFull`
\(`internal/testport/recovery_001_stream_rep_test.go`\) ports all of 001 in three pairings. It is gated by
`GOOPG_TAP_001=1` until it passes; the shortcut `TestPort_Recovery001StreamRep` stays until then.

## Shape

A primary, `standby_1` streaming from it, and `standby_2` cascading from `standby_1`, both built from base backups. The
second backup is taken from the standby while the primary is down. The test then checks:

- streamed table, sequence and unlogged\-sequence content; read\-only refusals on standbys;
- `target_session_attrs` routing \(libpq reads `in_hot_standby` and `default_transaction_read_only` from
  ParameterStatus\);
- SHOW and `READ_REPLICATION_SLOT` on replication connections;
- walsender and walreceiver `pg_stat_io` WAL statistics;
- physical slots on the primary and on the cascading standby, and `hot_standby_feedback` xmin on both;
- a `primary_conninfo` change applied by reload;
- slot advancing persisted across a restart, with the old segment recycled;
- `pg_backup_start` + `BASE_BACKUP` in one session, and `BASE_BACKUP` cancellation \(`pg_stat_progress_basebackup`\).

## Slice 1 \(landed\)

- The port, and tapnode helpers:
  - `Host`, `Port`, `User`;
  - `PSQLConnstr` \(psql `--dbname connstr`\);
  - `PSQLCommands` / `PSQLCommandsCmd` \(`--command` lists, foreground or background\);
  - `AdvanceWAL` \(Cluster.pm `advance_wal`\);
  - the client environment `PGDATABASE=postgres`, `PGUSER=<bootstrap superuser>`, as Cluster.pm has.
- `pg_sequence_last_value(regclass)`: last\_value when is\_called, else NULL. NULL also for another session's temporary
  sequence and for an unlogged sequence during recovery. A non\-sequence gives `cannot open relation` with
  errdetail\_relkind\_not\_supported, as sequence.c does.

## Slice 2 \(0077a, landed\): sequences on a goopg standby

- A goopg sequence's counter lives in the sequence registry, and `SELECT * FROM seq` reads it through the table's
  `VirtualRows`. On a standby:
  - the heap catalog reload registered the relkind S relation first, and `CreateSequenceCatalogRelation` then
    returned on "already exists", so the relation had no reader. It now wires the reader onto the existing relation;
  - `XLOG_SEQ_LOG` replay \(seq\_redo\) rebuilt the page but never touched the registry. `StreamReplayer` now has a
    sequence hook, and `Runtime.RefreshSequenceAfterReplay` reads block 0 of the relation and sets the counter. The
    standby's catalog refresh after a commit also reloads sequences, so a newly created sequence registers.
- `SELECT * FROM seq` shows the page tuple, as PG's `read_seq_tuple` does. That is the logged\-ahead `last_value` and
  its `log_cnt` \(`33|0|t` after one nextval\), not the primary's live counter. It does so until this server calls
  nextval or setval itself \(`SetSequencePageState`\). The startup reload uses the same path, so a restarted primary
  also shows the page, like PG.

## Slice 3 \(0077b, landed\): a read\-only hot standby

- During recovery PG forces every transaction read\-only, and ExecCheckXactReadOnly refuses data\-modifying statements
  with "cannot execute <CMD> in a read\-only transaction" \(25006\). goopg applied the standby's INSERT. The INSERT,
  UPDATE, DELETE, MERGE and upsert operators now check `ctx.IsStandby` in Open \(`preventCommandDuringRecovery`\).
  The flag is evaluated per statement, so promotion lifts it.
- A standby session reports `in_hot_standby=on` in its startup ParameterStatus block. libpq's
  `target_session_attrs` uses it, with `default_transaction_read_only`, to classify the server.
- Not done \(M0149\-0084, a nearby task\):
  - read\-only transactions on a primary, and the `transaction_read_only` GUC;
  - DDL refusal on a standby;
  - re\-reporting `in_hot_standby` at promotion.

## Slice 4 \(0077c, landed\): SHOW port

- `goopg start --listen host:port` is goopg's \-h/\-p. The `port` GUC \(PGC\_POSTMASTER\) is now set from the listen port
  with command\-line source, as postmaster's \-p does. libpq's target\_session\_attrs cases compare `SHOW port` with
  the node's port.
- Sequence refresh race: a running standby's catalog refresh after a commit could put an older page read back over
  the counter the sequence replay hook had already set \(1 run in 3\). The refresh now registers only sequences it
  does not know yet; known sequences follow their replayed pages through the hook alone.

## Slice 5 \(0077d, landed\): WAL I/O in pg\_stat\_io

- PG's walsender reads WAL through WALRead, which counts each read in pg\_stat\_io as an IOOP\_READ on IOOBJECT\_WAL,
  IOCONTEXT\_NORMAL. The walreceiver's XLogWalRcvWrite counts each write as an IOOP\_WRITE. goopg's walsender
  serves chunks from its WAL iterator, and the walreceiver appends them to the local WAL. Each now counts one op of the
  chunk's size into the cumulative pg\_stat\_io store \(`executor.CountWALSenderRead` / `CountWALReceiverWrite`\).

## Open \(children in fix\_plan\)

- 0077g: `pg_replication_slots.xmin` shows 0 instead of NULL.
- 0077e: on a PG 18.3 standby of a goopg primary, reading an unlogged sequence fails with "could not open file".
- 0077f: a goopg standby does not serve a cascading standby \(PG\-primary pairing\).
