# M0149\-0040 — Port subscription/t/003\_constraints.pl

Status: in progress. TestPort\_Subscription003Constraints is written and gated \(`GOOPG_TAP_003`\). It runs the
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

## Remaining
- M0149\-0040d / 0040e: for an UPDATE of the key, a goopg publisher sends the full old row \('O'\) where PG sends the
  key \('K'\), and a goopg subscriber matches the old tuple on every column where PG searches the replica identity
  index. 003's all\-goopg pairing then misses the row whose non\-key column differs locally.
