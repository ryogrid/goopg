# M0149\-0040 — Port subscription/t/003\_constraints.pl

Status: done. TestPort\_Subscription003Constraints is ungated and passes 3 of 3 runs in all three pairings \(M0149\-0040a–d\). It runs the
upstream script with `tapnode` nodes in three pairings: all\-goopg, PG 18.3 publisher with a goopg subscriber, and
goopg publisher with a PG 18.3 subscriber. `waitForSubCatchup` is Cluster.pm's `wait_for_catchup(appname)`: the
publisher's `pg_current_wal_lsn()` is at or below the walsender row's `replay_lsn`.

## Landed — M0149\-0040a, subscriber status replies

goopg's `LogicalReceiver` reported write = flush = apply = its last applied commit LSN. On a quiet publisher no
commit ever reached `pg_current_wal_lsn()`, so `replay_lsn` stayed empty and `wait_for_catchup` timed out.

PG's apply worker \(worker.c `LogicalRepApplyLoop` / `send_feedback` / `get_flush_position`\) works differently:

- `last_received` rises with every 'w' frame's start and walEnd and every keepalive's walEnd.
- The reply carries write = last received.
- Flush and apply are the last received position when no transaction is pending, so an idle subscriber confirms up
  to the publisher's WAL end. Otherwise they are the last flushed commit.
- An unrequested keepalive that moved nothing sends no reply.

The receiver now does the same. Its `inXact` flag is set between a pgoutput Begin and its Commit. goopg applies each
commit before reading the next frame, so the applied commit stands in for PG's flushed one \(PG maps remote commit
ends to local flush positions through `lsn_mapping`\).

## Landed — M0149\-0040c, triggers in the apply worker

The apply worker wrote the heap directly and fired no triggers. PG's ExecSimpleRelationInsert / Update / Delete fire
the table's row triggers, and the apply worker runs with `session_replication_role = replica`
\(ApplyWorkerMain → SetConfigOption\), so REPLICA and ALWAYS triggers fire and ORIGIN ones do not.

- `ApplyWorker.triggerContext` gives the apply Context the subscriber database's OID, so the routine registry
  resolves trigger functions. Its `GetSetting` reports `session_replication_role = replica`, which
  `triggerModeFires` reads.
- INSERT: BEFORE ROW triggers may replace the row or skip it \(RETURN NULL\). AFTER ROW triggers fire after the heap
  write.
- UPDATE: the located old row is OLD and fills NEW's unchanged\-TOAST and subscriber\-only cells. The updated\-column
  set is every column the remote tuple carries \(`remoteUpdatedColumns`, apply\_handle\_update's `updatedCols`\), so
  an `UPDATE OF bid` trigger fires for an UPDATE that only changed `id`, as 003 checks.
- DELETE: BEFORE ROW triggers see the located row and may skip the delete; AFTER ROW triggers follow it.
- AFTER ROW triggers fire immediately: the worker opens no after\-trigger query level. PG queues them and fires them
  at the end of each applied change \(AfterTriggerEndQuery in apply\_handle\_\*\); for a single row the order is the
  same.

## Landed — M0149\-0040b, real WAL positions on a goopg publisher

The logical walsender numbered its 'w' frames with a byte counter started at START\_REPLICATION's position, kept its
keepalive walEnd on that counter, and never registered in the walsender registry. A goopg publisher therefore had no
`pg_stat_replication` row, and nothing a subscriber reported could be compared with `pg_current_wal_lsn()`.

`runLogicalWalsender` now behaves as PG's walsender does in logical mode:

- **Registration.** It registers in the walsender registry, which backs `pg_stat_replication`. `sent_lsn` follows
  sentPtr; write/flush/replay follow the subscriber's 'r' replies \(ProcessStandbyReplyMessage\).
- **Data frames.** A 'w' frame carries the write location as both dataStart and walEnd \(WalSndPrepareWrite\). goopg
  decodes a whole transaction at its commit record, so Begin, changes and Commit all carry that record's end. PG
  gives Begin the transaction's first LSN and each change its own LSN \(deferred\).
  `SlotDecoder.SetRecordHooks` sets the location before each record and advances sentPtr after it.
- **Keepalives.** A keepalive carries sentPtr, the end of the last record read. sentPtr starts at the slot's
  `confirmed_flush_lsn` \(StartLogicalReplication\).
- **Keepalive timing.** A keepalive goes out as soon as the subscriber's reported write and flush positions are both
  behind sentPtr and no keepalive awaits a reply \(WalSndWaitForWal\), checked every 200 ms; otherwise every 10 s.
  An idle subscriber then confirms the WAL end and `wait_for_catchup` passes.

## Landed — M0149\-0040d, the old key on a goopg publisher

For an UPDATE of the key, or a DELETE, a goopg publisher sent the whole old row as 'O'. PG's
`logicalrep_write_update` / `logicalrep_write_delete` send 'O' only under REPLICA IDENTITY FULL. Otherwise they send
'K' with the old key, the tuple heap\_update / heap\_delete log for the replica identity \(ExtractReplicaIdentity\),
whose other columns are NULL.

In 003 the subscriber's trigger had kept `bid` at 1 while the publisher's row had 2. The full old row then matched
nothing on a goopg subscriber, which compares every non\-null cell. `encodePgoOldTuple` now chooses between 'K' and
'O' as PG does. Its key\-only walk still decodes every column, so later offsets stay right. A relation without
identity columns keeps 'O', because PG would refuse to publish the change at all.

## Remaining
- M0149\-0040e \(not needed by 003\): a goopg subscriber matches an old tuple on every non\-null cell, where PG's
  FindReplTupleInLocalRel searches the local replica identity index and skips a change whose row is missing.
- M0149\-0040f \(nearby\): goopg logical slots report LSNs one byte past the record end and an empty plugin column.

## Landed — M0149\-0040f, 0\-based logical slot positions

goopg's WAL reader numbers records 1\-based: `Record.StartLSN` is the 0\-based offset plus one, and `Record.EndLSN`
is PG's EndRecPtr \(the 0\-based exclusive end\). A logical slot stored the 1\-based next start \(EndLSN\+1, and
WrittenLSN\+1 at creation\). But slot positions leave the server as 0\-based PG LSNs:

- CREATE\_REPLICATION\_SLOT's consistent\_point;
- `pg_replication_slots`;
- the walsender's keepalives;
- a subscriber's 'r' replies, which confirm 0\-based positions back into the slot.

Once a confirmed position passed the slot's own \(a keepalive's walEnd, for instance\), RestartLSN pointed one byte
before the next record. A reconnecting decoder then failed with "invalid record header: padding bytes nonzero".

A logical slot now holds PG's 0\-based positions: it is created at WrittenLSN and advanced to the commit's EndLSN.
`NewSlotDecoder` opens the record iterator at RestartLSN \+ 1. Physical slots keep their convention.
