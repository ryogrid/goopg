# M0149\-0061 — Port subscription/t/013\_partition.pl

Status: in progress. TestPort\_Subscription013Partition is written and gated by `GOOPG_TAP_013`. It uses one
publisher and two subscribers in three pairings \(all\-goopg; PG 18.3 publisher → goopg subscribers; goopg
publisher → PG 18.3 subscribers\).

013 replicates partitioned tables:

- from leaves and through roots \(publish\_via\_partition\_root\);
- into differently shaped subscriber partitions, sub\-partitioned with reordered columns;
- with subscriber triggers, TRUNCATE, ATTACH / DETACH and conflict logging.

## Landed — M0149\-0061a \(S2\)

UPDATE and DELETE through a partitioned parent evaluated the parent's WHERE / SET / RETURNING against a leaf's raw
column order. A partition attached with its columns in another order \(`tab1_1 \(b text, a int\)`\) therefore
matched nothing: `UPDATE tab1 SET a = 2 WHERE a = 1` reported UPDATE 0 on a row that exists, and DELETE did the
same. A cross\-partition move also wrote the row in the source partition's layout.

PG maps child tuples to root attributes \(ExecInitPartitionInfo's attribute maps\) and converts a moved tuple
\(ExecCrossPartitionUpdate\). goopg now does the same:

- updateOp and deleteOp give a leaf whose layout differs from the parent's \(`partitionColOrderMatches`\) the
  inheritance child's remap, so expressions are evaluated in parent space and the new row is mapped back to the
  leaf.
- The pending update keeps `childColMap`, so the row is routed on its parent\-ordered form.
- A moved row is written in the destination's own column order.

Same\-layout partitions are unchanged.

The remapped path skips Phase\-1 EPQ, as inheritance children already do \(ledgered\).

Test: TestPartitionDMLMismatchedColumnOrder.

## Landed — M0149\-0061b \(sync LSN, and what it uncovered on the publisher\)

- **Sync LSN.** `RunTableSync` \(CaptureSyncLSN\) copies inside `BEGIN READ ONLY ISOLATION LEVEL REPEATABLE READ`. The
  transaction's first statement reads `pg_current_wal_lsn\(\)`, and that value is recorded as the SYNCDONE statelsn.
  The refresh no longer promotes such a rel to READY. The apply worker's `shouldApplyChangesForRel` is
  should\_apply\_changes\_for\_rel: READY, or SYNCDONE with statelsn <= remote\_final\_lsn \(the Begin's final\_lsn\).
  The commit's end\_lsn \(process\_syncing\_tables\) and idle keepalives \(`PromoteSyncedRels`\) promote the rel.
  PG instead copies in the snapshot of the tablesync worker's own slot \(USE\_SNAPSHOT\); goopg's
  snapshot\-then\-LSN statement leaves a small window for a commit concurrent with the copy \(ledgered\).
- **pgoutput commit LSNs.** Begin final\_lsn and Commit commit\_lsn now carry the commit record's start
  \(decode.c DecodeCommit buf\->origptr\), and end\_lsn its end \(`CommitStartAware`, `Decoder.ApplyCommitAt`\). goopg
  used to send the end in all three fields, which made a PG\-rule sync\-LSN comparison wrong for the transaction
  ending at the sync point. goopg's subscriber now reports end\_lsn as its applied position, as PG's does.
- **FOR ALL TABLES with publish\_via\_partition\_root.**
  - The walsender's filter now publishes a partition's change as its topmost ancestor's \(viaRootAll\).
  - `catalog.AllTablesPublicationRelations` is GetAllTablesPublicationRelations: plain tables and partitions without
    viaroot, plain tables and partitioned roots with it. Partitioned parents had been listed in every mode, so a
    subscriber copied a parent and its leaves.
- **Attribute map.** A leaf whose columns are in another order from the root is published through the root's
  layout \(`tupleConv`, build\_attrmap\_by\_name\). It used to be published as itself, and a subscriber without that
  leaf could not apply it.

Tests: TestApplyWorkerSkipsChangesBeforeSyncLSN; TestApplyWorkerPromotesSyncDoneToReadyOnCommit, updated to PG's
rule \(a commit past the sync LSN applies\).

## Open

- **0061c.** Subscriber AFTER ROW triggers must fire on the partitions that routed UPDATEs land in.
- **0061d.** A replicated TRUNCATE of a partitioned target must reach its sub\-partitions.
- **0061e.** update\_missing / delete\_missing conflict log lines for routed changes.
- **0061f.** A leaf TRUNCATE must not be published when the leaf is published via its root.
- **0061g.** The leaf\-to\-root map must survive dropped and re\-added columns.
