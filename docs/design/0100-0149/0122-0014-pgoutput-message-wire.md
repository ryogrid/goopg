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

## Update 2026-10-30 — the Relation message's namespace, relreplident and atttypmod (`f0c7b6718`)

PG 18.3 reference: `postgres/src/backend/replication/logical/proto.c` (logicalrep_write_rel,
logicalrep_write_namespace, logicalrep_write_attrs, logicalrep_read_namespace).

`PgOutput.writeRelation` used to write three fields that didn't follow the source table:

| field | before | now | PG |
|---|---|---|---|
| namespace | `rel.Schema` verbatim: empty for a table created without a schema | `relationNamespace`: "public" for goopg's empty Schema, "" only for pg\_catalog | logicalrep_write_namespace |
| relreplident | always `'d'` | the table's `catalog.ReplIdentOrDefault` | `rd_rel->relreplident` |
| per-column typmod | always -1 | `ColumnDef.Typmod` | `att->atttypmod` |

`ColumnDef.Typmod` comes from `nodes.ColumnTypmod`. `TestAttTypmodSiblingsAgree` pins it against the pg_attribute
writer's `executor.pgAttTypmod`.

The empty namespace was a real interop defect. A PG subscriber reads an empty namespace as pg_catalog
(logicalrep_read_namespace), so it looked for `pg_catalog.t` and silently applied nothing.
`TestPort_PgoutputInteropGoopgToPG` never saw this: it creates `public.t` explicitly, and goopg records an
explicit schema verbatim.

**Pins:**

- `TestPgOutputRelationMessageFields`.
- **Oracle**, `TestPort_PgoutputInteropGoopgToPGUnqualifiedTable`: a PG 18.3 subscriber applies an unqualified goopg
  table. It fails with the mapping reverted.
- The pg_recvlogical E2E checks for `public` in the stream.

**Still open (ledgered):**

- Every column is still flagged as replica identity. logicalrep_write_attrs flags only the identity columns: all
  under FULL, the primary key under DEFAULT, the index columns under INDEX, none under NOTHING.
- `nodes.ColumnTypmod` has no interval arm, so a qualified interval column sends -1 where pg_attribute has the packed
  typmod.

## Update 2026-10-30 — replica-identity column flags and CheckCmdReplicaIdentity (`4a0cf54cc`)

PG 18.3 reference: `postgres/src/backend/replication/logical/proto.c` (logicalrep_write_attrs),
`postgres/src/backend/utils/cache/relcache.c` (RelationGetIndexList's rd_replidindex selection,
RelationGetIdentityKeyBitmap), `postgres/src/backend/executor/execReplication.c` (CheckCmdReplicaIdentity).

### Relation message flags

`ColumnDef.ReplIdent` drives the LOGICALREP_IS_REPLICA_IDENTITY flag. `xlog.identityColumns` builds the set:

| relreplident | flagged columns |
|---|---|
| FULL | every column |
| DEFAULT | the primary key's |
| USING INDEX | the indisreplident index's |
| NOTHING | none |

A deferrable (non-immediate) index never counts, as rd_replidindex never names one.

Every column used to be flagged. goopg's apply worker, like PG's, locates the row for an UPDATE sent without an old
tuple by the flagged columns, and goopg sends HOT updates under DEFAULT without one. The subscriber therefore searched
for the new non-key values and kept the stale row beside the new one. `TestApplyWorkerUpdatesNonKeyColumnOverPgoutput`
shows the result before the fix: `(7,alpha)`, `(8,gamma)`, `(7,beta)`. This is latent today, because goopg-to-goopg
subscription does not yet deliver end to end.

### CheckCmdReplicaIdentity

Flagging no columns for a table with no identity is only correct because PG never streams such a change.
`checkCmdReplicaIdentity`, called from `updateOp.Open` / `deleteOp.Open` once per result relation, refuses an
UPDATE (DELETE) on a table that publishes it but has no replica identity. A replica identity here means the immediate
primary key under DEFAULT, the immediate indisreplident index under USING INDEX, or FULL. The error is 55000 "cannot
update table \"%s\" because it does not have a replica identity and publishes updates" (or the delete form), with
PG's hint. Partitioned tables are skipped, as in PG. `hasReplicaIdentity` and `xlog.identityColumns` are siblings
and pick the same index.

Without the check, a PG subscriber that has a primary key would reject goopg's no-identity UPDATEs ("publisher did not
send replica identity column expected by the logical replication target relation").

**Pins:** `TestApplyWorkerUpdatesNonKeyColumnOverPgoutput` and `TestCheckCmdReplicaIdentity` (deferrable
mutation caught). A live probe shows PG's messages.

**regress publication** stays at 2209 diff lines. Its replica-identity cases run after `ALTER PUBLICATION … ADD
TABLE`, which goopg accepts as a no-op, or go through partitions and row filters.

**Still open (ledgered):**

- ALTER PUBLICATION ADD / SET / DROP TABLE is a no-op.
- goopg-to-goopg subscription does not deliver.
- Publication membership through a partitioned ancestor is not modelled.
- Row filters and column lists are not supported.
- REPLICA IDENTITY FULL UPDATEs send no old tuple.
