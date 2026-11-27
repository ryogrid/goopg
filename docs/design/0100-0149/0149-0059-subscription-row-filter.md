# M0149\-0059 — Port subscription/t/028\_row\_filter.pl

Status: in progress. TestPort\_Subscription028RowFilter is written and gated by `GOOPG_TAP_028`. The PG 18.3
publisher → goopg subscriber pairing passes after 0059a/b. Both goopg\-publisher pairings stop at the first
`CREATE PUBLICATION … FOR TABLE t WHERE (…)`, which is 0059c.

028 publishes tables with row filters \(`FOR TABLE t WHERE (expr)`\) and checks:

- the initial copy;
- INSERT / UPDATE / DELETE filtering, including UPDATE's old/new transformation \(the old row matches and the new
  one doesn't → DELETE; the reverse → INSERT\);
- filters OR\-combined across publications;
- a filter ignored when the table is also published by FOR ALL TABLES or TABLES IN SCHEMA;
- `publish_via_partition_root` picking the root's filter;
- inheritance children, TOAST values and virtual generated columns.

## Landed — M0149\-0059a \(subscriber table sync\)

- `FetchRemoteTableInfo` returns the remote column list, whether generated columns are published, and the relkind
  \(fetch\_remote\_table\_info's three queries\). `FetchRemoteColumns` is now a wrapper over it.
- `FetchRemoteRowFilters` runs PG's
  `SELECT DISTINCT pg_get_expr(gpt.qual, gpt.relid) FROM pg_publication p, LATERAL pg_get_publication_tables(p.pubname) gpt WHERE gpt.relid = … AND p.pubname IN (…)`.
  A NULL row means some publication has no filter, so no filter applies.
- copy\_table: when there are filters, the COPY is `COPY (SELECT cols FROM [ONLY ]rel WHERE q1 OR q2) TO STDOUT`.
  ONLY is used for relkind `r` only, so a partitioned root's COPY reads its partitions.

## Landed — M0149\-0059b \(COPY FROM tuple routing, S2\)

`COPY parted FROM …` wrote every row to the partitioned parent's own relfilenode. A partitioned table has no
storage a scan reads, so the command reported `COPY n` and the data was lost. This hit SQL COPY and the
subscriber's table sync alike \(028's REFRESH into the partitioned root\).

`CopyFromExecutor.storeRoutedRow` is copyfrom.c's tuple routing:

1. `routeToPartition` picks the leaf; a row with no leaf gets `no partition of relation … found for row`.
2. The routing path is locked.
3. The row is remapped to the leaf's column order.
4. The leaf's default\-partition, NOT NULL, CHECK and unique checks run, naming the leaf.
5. The row is written to the leaf heap and its indexes.

The rest is unchanged: parent\-level defaults, BEFORE ROW triggers, CHECK constraints and the after\-row queue.

Deviation: PG's CIM\_MULTI\_CONDITIONAL keeps one multi\-insert buffer per leaf. goopg routes row by row, because
`copyUsesMultiInsert` declines the buffer for a partitioned target. This changes page placement only, not the
data.

Test: TestCopyFromRoutesIntoPartitions.

## Open — M0149\-0059c \(publisher row filters\)

- grammar: `FOR TABLE t WHERE (expr)` in CREATE PUBLICATION and ALTER PUBLICATION ADD / SET, with PG's
  validation errors \(e.g. a non\-immutable function, a user\-defined operator, a column not in the replica
  identity for UPDATE/DELETE publications\);
- storage: pg\_publication\_rel.prqual;
- `pg_get_publication_tables`' qual column and `pg_get_expr` over it;
- pgoutput: evaluate the filter per change, with the UPDATE transformation, and the ALL TABLES / SCHEMA override;
- `publish_via_partition_root` \(the root's filter\) and inheritance children.
