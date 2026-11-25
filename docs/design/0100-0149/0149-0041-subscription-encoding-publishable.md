# M0149\-0041 / 0042 — subscription/t/005\_encoding.pl and 006\_rewrite.pl

Status: 006 done; 005 skipped as a detour. Both ports use the three\-pairing `tapnode` harness of M0149\-0040.
TestPort\_Subscription006Rewrite is ungated and passes 3 of 3 runs in all three pairings \(M0149\-0040f,
M0149\-0042a–b\). 005 stays gated \(`GOOPG_TAP_005`\). `waitForSubscriptionSync` is Cluster.pm's
`wait_for_subscription_sync`: every `pg_subscription_rel` row is 'r' or 's', then `wait_for_catchup`.

## 005 — non\-UTF8 databases \(detour\)

`goopg init --encoding=LATIN1` exits 0, but the running server reports `server_encoding = UTF8` and every database
as UTF8. A goopg subscriber therefore stores the replicated value as UTF8, and the LATIN1 literal the test compares
with does not match.

Real LATIN1 support has more than three links:
- the database encoding honored at runtime;
- text stored and validated in it;
- client↔server conversion;
- the apply worker asking the publisher for its own encoding.

Filed as M0149\-0041a \(owner\-architecture\).

## Landed — M0149\-0041b, publishable tables only

A goopg `FOR ALL TABLES` publication listed the system catalogs goopg models as tables \(`pg_type`, `pg_attribute`,
`pg_largeobject`, `pg_largeobject_metadata`\), across every database. A PG subscriber tried to sync them and never
finished.

`catalog.IsPublishableTable` is `is_publishable_class` \(catalog/pg\_publication.c\). It admits permanent ordinary
tables with an OID of at least FirstNormalObjectId, outside `pg_catalog`, `information_schema` and `pg_toast`. It
rejects views, materialized views, foreign tables, sequences and virtual relations. `pg_get_publication_tables` and
the `pg_publication_tables` view apply it, each limited to the publication's database.

## Landed — `pg_get_replica_identity_index` \(M0149\-0042a step 1\)

A PG subscriber's table sync runs `fetch_remote_table_info` on the publisher, which joins `pg_index` on
`pg_get_replica_identity_index(relid)` to learn the key columns. goopg lacked the function. It now follows
RelationGetReplicaIndex:

- REPLICA IDENTITY DEFAULT: the primary key;
- REPLICA IDENTITY USING INDEX: that index;
- FULL, NOTHING, or no key: NULL.

A deferrable index never qualifies. The result has type `regclass`.

## Landed — end of a logical stream the subscriber closes \(M0149\-0042a step 2\)

A table\-sync worker that has caught up ends its stream itself. It sends a status update and CopyDone
\(walrcv\_endstreaming\), then waits for the walsender's reply:

1. CopyDone;
2. CommandComplete "COPY 0" \(StartLogicalReplication's EndCommand\);
3. a second CommandComplete "START\_REPLICATION" \(exec\_replication\_command's "dupe, but necessary per
   libpqrcv\_endstreaming", because its PQendcopy consumes the first\);
4. ReadyForQuery.

goopg's logical walsender just returned on the subscriber's CopyDone, so the sync worker blocked in state 'f'. Now:

- `walsenderPgoutputAdapter.endStreaming` sends that sequence once the decoder and keepalive goroutines stop.
- `stopSending` \(PG's streamingDoneSending\) drops every 'w' or 'k' frame after the subscriber's CopyDone, so no
  data follows the stream the subscriber saw.

The physical walsender's reply to a CopyDone \(CopyDone, one CommandComplete "START\_STREAMING"\) is not this
sequence \(deferral ledger\).

## Landed — a goopg walsender across ALTER TABLE \(M0149\-0042b\)

All\-goopg, the stall after `ALTER TABLE test1 ADD c int NOT NULL DEFAULT 0` had two causes on the goopg publisher.

- **Catalog changes reached pgoutput.** The ALTER TABLE writes pg\_type and pg\_class rows. A FOR ALL TABLES filter
  admits every relation, so `PgOutput.Change` tried to encode the pg\_type tuple. It failed \("pglz: truncated
  compressed varlena"\), the walsender died, and every reconnect died on the same record. PG never shows catalog
  changes to the output plugin: reorderbuffer.c skips relations that are not RelationIsLogicallyLogged
  \(IsCatalogRelation\), and pgoutput\_change returns early for relations that are not is\_publishable\_relation.
  `relationIsPublishable` now skips relations below FirstNormalObjectId and those in pg\_catalog,
  information\_schema and pg\_toast.
- **A frozen schema.** The walsender's `CatalogSnapshot` was built once at START\_REPLICATION, so the row inserted
  after ADD COLUMN went out with the old two\-column Relation message and the subscriber stored NULL. PG's pgoutput
  re\-sends the Relation message after a relcache invalidation \(rel\_sync\_cache\_relation\_cb → schema\_sent =
  false\). `CatalogSnapshot.Current` compares the captured definition with the live catalog on every change. On a
  different shape it takes the live definition and pgoutput re\-sends the Relation message. A relation created
  after START\_REPLICATION is also resolved from the live catalog, so a FOR ALL TABLES stream publishes it, as PG
  does.

Before this, that second case only worked because the walsender crashed on the CREATE TABLE's catalog rows and the
reconnect rebuilt the snapshot. Without the crash, 008's goopg\-publisher pairing lost test\_tab2's insert until
the live lookup was added.

Deviations \(deferral ledger\):

- **Decode\-time schema.** goopg decodes each change against the catalog as of decoding, not PG's historic
  snapshot as of the change. A change decoded after a later DDL already carries the new shape.
- **Typmod\-only changes** are not detected.
- **Per\-change cost.** The live check is a linear scan by OID, once per user\-table change.

## Remaining

Historical, resolved by M0149\-0040f: with a goopg publisher, the apply worker reconnects after the table sync and fails with "padding bytes nonzero". The
slot's confirmed position is one byte past the record end \(M0149\-0040f\), so the decoder starts mid\-record. That
is a 4th link for 006, which is therefore skipped as a detour; 0040f is filed as a proven blocker.
