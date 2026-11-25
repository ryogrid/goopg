# M0149\-0045 — Port subscription/t/009\_matviews.pl

Status: done. TestPort\_Subscription009Matviews passes 3 of 3 runs in all three pairings \(all\-goopg; PG 18.3
publisher with goopg subscriber; goopg publisher with PG 18.3 subscriber\).

## What the test checks

Logical decoding produces change information for a materialized view's heap, but logical replication must
ignore it \(bug \#15044\). The publisher runs CREATE MATERIALIZED VIEW under a FOR ALL TABLES publication, and the
subscriber has no such relation. `wait_for_catchup` must still return.

## Landed — matview changes never reach pgoutput

The port passed on its first run. A probe showed that was luck. After a `REFRESH MATERIALIZED VIEW testmv1`, the
goopg walsender streamed the matview's rows, and a goopg subscriber's apply worker failed on each one \("INSERT
for relation public.testmv1 has no local table"\) and stopped. Every later change, here a row inserted into test1,
was lost while the slot stayed behind.

PG rejects these relations in is\_publishable\_class \(relkind must be RELATION or PARTITIONED\_TABLE, and the
relation must be permanent and user\\-created\), which pgoutput\_change checks for every change.

`RelationDef.NotPublishable` now records `!catalog.IsPublishableTable(t)`, the same rule the publication table
lists use \(M0149\-0041b\). `relationIsPublishable` skips such relations alongside the catalog / OID check of
M0149\-0042b. Materialized views, views, sequences, foreign, temporary and unlogged tables are all covered.

Unit test TestPgOutputSkipsMaterializedView. The upstream test only runs CREATE MATERIALIZED VIEW, so the
REFRESH case is pinned by the unit test and the probe, not by the port.
