# M0149\-0041 / 0042 — subscription/t/005\_encoding.pl and 006\_rewrite.pl

Status: 005 skipped as a detour; 006 in progress. Both ports are written with the three\-pairing `tapnode` harness of
M0149\-0040 and gated \(`GOOPG_TAP_005`, `GOOPG_TAP_006`\). `waitForSubscriptionSync` is Cluster.pm's
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

## Remaining \(M0149\-0042a\)

- **goopg publisher → PG subscriber:** the PG table\-sync worker copies `test1` and sets state 'f', then never
  reports finishing. Its catch\-up from the temporary tablesync slot against the apply worker's position stalls.
- **all\-goopg:** the initial sync passes, but the publisher's slot then goes inactive and the first catch\-up times
  out.
