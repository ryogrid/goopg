# M0149\-0057 — Port subscription/t/027\_nosuperuser.pl

Status: in progress. The goopg publisher → PG 18.3 subscriber pairing passes. The goopg\-subscriber pairings still
need M0149\-0057a, 0057b and 0057e. TestPort\_Subscription027Nosuperuser is gated by GOOPG\_TAP\_027.

027's subscription is owned by a role whose superuser bit and role memberships the test changes. Replication
must stop with PG's errors when the owner cannot act as the table owner, when the table forces row\-level
security, or when the table owner lacks INSERT / UPDATE / DELETE / SELECT. It must then resume from exactly where
it stopped once the problem is fixed. A hash\-partitioned table is published with publish\_via\_partition\_root,
and the two nodes use different hash remainders. The password\_required block needs Unix\-domain sockets and is
skipped, as upstream does without them; tapnode connects over TCP.

## Landed — M0149\-0057c, the goopg publisher re\-sends what the subscriber has not confirmed

The test makes the PG subscriber fail and reconnect many times. A goopg publisher sometimes lost the failed
transaction for good. Three defects combined:

1. **The slot moved on decode.** SlotDecoder.Run advanced confirmed\_flush\_lsn, and with it restart\_lsn, at every
   decoded commit. PG moves confirmed\_flush only on the subscriber's flush feedback
   \(LogicalConfirmReceivedLocation\). The logical walsender now sets `SetFeedbackOnly`.
2. **Every message was stamped with the commit end.** goopg decodes a transaction at its commit record and stamped
   the CopyData walEnd of B, the changes and C with that record's end. The PG apply worker raises last\_received
   from each message header and, when it has drained the socket with no locally pending transaction, confirms
   flush = last\_received. So it confirmed a transaction as soon as its BEGIN had arrived. PG's write\_location is
   `txn->first_lsn` for BEGIN, the change's LSN for a change, and `txn->end_lsn` for COMMIT
   \(begin/change/commit\_cb\_wrapper\). `Decoder.SetWriteLocationHook` now reports those positions, and the
   walsender stamps each message with them.
3. **restart\_lsn followed the flush position into an unfinished transaction.** PG keeps restart\_lsn at the oldest
   running transaction \(candidate\_restart\_lsn from xl\_running\_xacts\). goopg now records, for every transaction,
   the start of its first record \(ReorderBuffer `restartLSN`, from `Decoder.SetRecordStart`\).
   `SlotDecoder.RestartCapFor(flush)` caps restart\_lsn at the earliest restart point of any transaction that was
   sent but commits after flush, and of any still being decoded. `Slots.AdvanceLogicalFeedback` applies it.

On reconnect the decoder skips what the subscriber already has: start\_decoding\_at = max\(client start point,
confirmed\_flush\), and a commit or non\-transactional message ending at or before it is dropped
\(SnapBuildXactNeedsSkip; `Decoder.SetSkipThrough`\).

## Landed — M0149\-0057d, publish\_via\_partition\_root

- `Publication.PublishViaRoot` covers the option, ALTER … SET, the pg\_publication.pubviaroot journal and reload,
  and the views.
- With it set, the table lists \(pg\_get\_publication\_tables, pg\_publication\_tables\) keep a partitioned member as
  itself. Schema publications list partitioned tables and drop partitions \(PUBLICATION\_PART\_ROOT,
  `catalog.PublicationSchemaTablesMode`\).
- The walsender filter's `PublishAsAncestor` is GetTopMostAncestorInPublication over the viaroot publications.
  PgOutput then sends the partition's change as the root's \(Relation message and OID\) when the two column
  layouts match \(`sameTupleLayout`\). PG converts other layouts through a tuple conversion map; goopg sends those
  as the leaf \(ledgered\).

Tests: TestDecoderWriteLocationsAndSkip, TestRestartCapFor.

## Open

- M0149\-0057a: the apply worker runs each change as the table owner, with the SET ROLE check, the RLS refusal and
  the table privilege checks, and restarts when the owner loses superuser.
- M0149\-0057b: goopg tablesync copies a partitioned remote table with `COPY (SELECT …)`.
- M0149\-0057e: apply routes UPDATE / DELETE for a partitioned local table.
