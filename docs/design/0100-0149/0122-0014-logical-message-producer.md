# M0122-0014 logical-message producer

Status: landed 2026-10-30 (`a45162dc3`).

PG 18.3 reference: `postgres/src/backend/replication/logical/message.c` (LogLogicalMessage, logicalmsg_redo),
`postgres/src/include/replication/message.h` (xl_logical_message), `postgres/src/backend/replication/logical/logicalfuncs.c`
(pg_logical_emit_message_bytea / _text), `postgres/src/backend/replication/logical/decode.c` (logicalmsg_decode),
`postgres/src/backend/replication/logical/reorderbuffer.c` (ReorderBufferQueueMessage),
`postgres/src/backend/replication/pgoutput/pgoutput.c` (parse_output_parameters "messages", pgoutput_message),
`postgres/src/backend/replication/walsender.c` (IdentifySystem).

## Producer — `pg_logical_emit_message` (`internal/executor/expr_logicalmsg.go`)

`pg_logical_emit_message(transactional, prefix, message text | bytea [, flush])` returns pg_lsn. Like
LogLogicalMessage, it writes one RM_LOGICALMSG / XLOG_LOGICAL_MESSAGE record (`xlog.EncodeLogicalMessagePG`).

The record's main data is an `xl_logical_message`:

| bytes | field |
|---|---|
| 0–3 | dbId |
| 4 | transactional |
| 5–7 | padding (zero in goopg; uninitialised stack bytes in PG) |
| 8–15 | prefix_size, counting the NUL |
| 16–23 | message_size |
| 24– | the prefix, its NUL, then the message |

Behaviour:

- **dbId** is the connection database's `pg_database.oid`, resolved by name exactly as the logical walsender
  resolves its own database. Matching resolution is what keeps the decoder's database filter consistent.
- **Transactional message:** the transaction's xid is assigned first (GetCurrentTransactionId), so decoding can
  queue the message in that transaction.
- **Flush:** a non-transactional message, or one with `flush = true`, is flushed before returning (XLogFlush).
- **Result:** the end of the record, as XLogInsert returns it.
- **Strict:** a NULL argument gives NULL.
- **During recovery:** "cannot make new WAL entries during recovery".
- **pg_waldump check:** PG 18.3's pg_waldump decodes goopg's records as `desc: MESSAGE transactional, prefix "p";
  payload (5 bytes): 68 65 6C 6C 6F`. The record is 57 bytes, the same length as PG's own.

Redo stays a no-op (`recovery.go`), as logicalmsg_redo is.

## Decoding (`internal/access/transam/xlog/logical_message.go`)

`classifyDecodedXLog` decodes the record (`DecodeLogicalMessage`) and calls `Decoder.applyLogicalMessage`, which
follows logicalmsg_decode and ReorderBufferQueueMessage:

- A message from another database is skipped. The logical walsender sets `SetDatabase` to its connection's
  database.
- A transactional message is queued in its transaction as `ChangeKind ChangeMessage` (`MsgPrefix`, `MsgContent`).
  It never folds. `ApplyCommit` hands it to `MessagePlugin.LogicalMessage` between Begin and Commit, in WAL order
  with the row changes. An abort drops it.
- A non-transactional message goes to the plugin at once, outside any transaction frame.
- The message LSN is the end of the record (the decoding buffer's endptr).

`MessagePlugin` is optional, like PG's message_cb.

## pgoutput and the walsender

- **messages option.** `PgOutput.LogicalMessage` writes the MESSAGE frame (logicalrep_write_message) only when the
  subscriber set the `messages` option (`SetMessages`; the logical walsender parses it as defGetBoolean does).
  Otherwise the message is dropped, as pgoutput_message does.
- **IDENTIFY\_SYSTEM dbname.** IDENTIFY\_SYSTEM's `dbname` was always NULL. It now names the database on a
  `replication=database` connection (am_db_walsender, compared exactly as backend_startup.c does). It stays NULL on
  a physical one.
  - PG 18.3's pg_recvlogical needs the name: without it, it stops with "could not establish database-specific
    replication connection".
  - pg_receivewal and pg_basebackup refuse a physical connection that reports one.

## Oracle

`TestE2E_LogicalMessageReachesPgRecvlogical` drives goopg with PG 18.3's pg_recvlogical, using pgoutput,
`proto_version 1` and `messages true`:

- the transactional message arrives as MESSAGE flags 1 inside BEGIN / COMMIT;
- the non-transactional one arrives as MESSAGE flags 0 on its own;
- the message from a rolled-back transaction never arrives.

The test fails without the IDENTIFY\_SYSTEM fix. Unit pins: `TestLogicalMessageDecoding` (two mutations caught),
`TestLogicalMessageCodec`, `TestPgOutputMessagesOption`.

## Not done (ledgered)

- logicalmsg_decode's origin filter (FilterByOrigin; goopg writes no origin block).
- The snapshot-builder gates: SnapBuildProcessChange, and the CONSISTENT / XactNeedsSkip checks for
  non-transactional messages.
- The SQL decoding functions (`pg_logical_slot_get_changes` with test\_decoding), which would print messages.
- PG's function-resolution error for a wrong argument count.
- pgoutput's Relation message writes an empty namespace for `public` tables; PG writes `public`.
