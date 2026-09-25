# M0122-0014 logical-message producer

Status: draft.

The SQL `pg_logical_emit_message` catalog entries currently have no executor handler. PostgreSQL writes `RM_LOGICALMSG_ID` records in `postgres/src/backend/replication/logical/message.c` and emits them in logical decoding before commit ordering is finalized.

goopg can append PG-compatible records through `stripeWriterCore.AppendXLogPayload`, but its `Decoder` only buffers row `Change` values and `OutputPlugin` has no message callback. A correct implementation must add a buffered message event, classify `RmgrLogicalMessage` in the slot decoder, and deliver transactional messages only through the owning transaction's commit sequence. Nontransactional messages require an immediate durable/flush path. The SQL executor handler must use that same producer rather than emitting protocol frames directly.

No production code is changed by this draft. Next: review the transaction and nontransactional event ordering, then implement the WAL payload codec and decoder event path together.
