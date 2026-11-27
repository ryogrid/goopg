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

## Open

- **0061b.** Table sync must record a sync LSN so the apply worker skips the changes the COPY already holds.
  Today a refresh double\-applies them, and sub2 loops on insert\_exists. PG's design is the tablesync slot's
  consistent point plus SYNCDONE statelsn.
- **0061c.** Subscriber AFTER ROW triggers must fire on the partitions that routed UPDATEs land in.
- **0061d.** A replicated TRUNCATE of a partitioned target must reach its sub\-partitions.
- **0061e.** update\_missing / delete\_missing conflict log lines for routed changes.
