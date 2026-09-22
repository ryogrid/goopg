# pgoutput MESSAGE Wire Frames

| Field | Value |
| --- | --- |
| Status | landed 2026-09-22 (`8170a6f29`) |
| Date | 2026-09-22 |
| Milestone | M0122-0014 |
| Scope | pgoutput protocol-v1 MESSAGE frame encode, decode, and subscriber handling |

## Problem

PostgreSQL pgoutput can carry a logical MESSAGE frame. The current decoder
rejects its `M` kind, which means a goopg subscriber cannot consume an otherwise
valid upstream stream when messages are enabled. The publisher also lacks a
frame API, even though callers that already have a decoded logical-message event
need no relation metadata or tuple codec to serialize one.

## Decision

Add the protocol-v1 shape from PostgreSQL `logicalrep_write_message`:

```text
M | flags(1) | lsn(8) | prefix(cstring) | payload-length(4) | payload
```

Bit zero of flags records whether the message is transactional. `PgOutput.Message`
will write that frame, and `DecodeMessage` will expose the flag, LSN, prefix,
and raw payload. The apply worker accepts a decoded MESSAGE as a no-op: message
delivery is an extension/plugin channel, not a table mutation, so it must not
abort an otherwise valid subscriber transaction.

This slice deliberately does not claim SQL-level `pg_logical_emit_message`
support. That requires creating and classifying RM_LOGICALMSG WAL records, then
feeding them into the logical decoder in transactional order. It remains tracked
as an open M0122-0014 continuation rather than being hidden by wire support.

## Verification

Unit tests pin the canonical big-endian frame, decode all exposed fields, reject
truncated payloads, and show that applying the decoded frame preserves an active
transaction for its following commit. The focused xlog and executor suites are
the required gates.

## References

- `postgres/src/backend/replication/logical/proto.c:640-661`
- `postgres/src/backend/replication/pgoutput/pgoutput.c:1722-1759`
- `postgres/src/backend/replication/logical/message.c:43-81`

## Verified against the oracle, not only the source \(2026\-09\-22\)

The frame above was read out of `logicalrep_write_message`; it was then
**checked byte for byte against PostgreSQL 18.3's own output**, which is the
difference between "matches the code I read" and "matches what the server
emits".

On a private cluster with `wal_level=logical`, a `pgoutput` slot and

```sql
SELECT pg_logical_emit_message(true, 'gooprefix', 'hello-goopg');
SELECT encode(data,'hex') FROM pg_logical_slot_peek_binary_changes(
    's_msg', NULL, NULL,
    'proto_version','1','publication_names','p','messages','true');
```

PostgreSQL emits

```text
4d01000000000206b200676f6f707265666978000000000b68656c6c6f2d676f6f7067
 M flags=01  lsn=0x0206b200   "gooprefix\0"   len=0x0b   "hello-goopg"
```

and `PgOutput.Message(true, 0x0206b200, "gooprefix", []byte("hello-goopg"))`
produces that string exactly.

## Forward\-compat boundary

Protocol **v2 inserts a 4\-byte xid between `M` and the flags byte** while
streaming an in\-progress transaction \(`logicalrep_write_message`'s
`TransactionIdIsValid(xid)` branch\). goopg requests `proto_version 1` only —
`internal/replication/logicalreceiver.go` states that in its `ProtoVersion`
doc comment — so the v1 layout is correct for what it negotiates. A future v2
negotiation must EXTEND this arm; inheriting it would silently read the xid's
first byte as flags.

## Scope boundary, restated

This is the wire half. SQL\-level `pg_logical_emit_message` requires
`RM_LOGICALMSG` records to be created, classified and fed to the decoder in
transactional order, and stays an open M0122\-0014 continuation with its own
draft design \(`0122-0014-logical-message-producer.md`\).
