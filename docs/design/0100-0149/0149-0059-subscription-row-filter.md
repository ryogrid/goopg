# M0149\-0059 — Port subscription/t/028\_row\_filter.pl

Status: done. TestPort\_Subscription028RowFilter passes 3/3 in all three pairings \(all\-goopg; PG 18.3
publisher → goopg subscriber; goopg publisher → PG 18.3 subscriber\).

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

## Landed — M0149\-0059c \(publisher row filters\)

- **Grammar.** `TABLE [ONLY] t [*] [\(cols\)] WHERE \(expr\)` in CREATE PUBLICATION and ALTER PUBLICATION ADD / SET
  \(gram.y OptWhereClause; the parentheses are required\). `ONLY` is now kept \(`TableOnly`\). A bare
  `WITH \(publish_via_partition_root\)` option is def\_elem's NULL argument, which defGetBoolean reads as true.
- **Storage.** `Publication.RowFilters` maps a member to its filter, held as the deparsed SQL text
  \(`defaultExprToSQL`, the ruleutils\-style renderer CHECK constraints use\). The filter is validated by planning
  `SELECT \(expr\)::bool FROM ONLY t`, so an unknown column or function fails the statement. It is journaled as
  pg\_publication\_rel.prqual \(text, as goopg stores other pg\_node\_tree columns\) and reloaded at startup.
  ALTER … SET re\-journals a member whose filter changed; DROP with a WHERE is PG's 42601.
- **Inheritance.** `FOR TABLE parent` without ONLY adds every inheritance descendant with the same filter and
  column list \(OpenTableList's recursion\). A partitioned table is not expanded: its partitions are published
  through it.
- **Catalog surface.** `catalog.PublicationRowFilter` is pg\_get\_publication\_tables' qual rule: no filter under FOR
  ALL TABLES or when the publication also covers the table's schema. pg\_publication\_rel.prqual and
  pg\_publication\_tables.rowfilter show the text. pg\_get\_publication\_tables applies filter\_partitions once any
  listed publication publishes via its root, so a partition whose ancestor is listed is not synchronized twice.
- **pgoutput.** `xlog.RowFilterFilter` / `PgOutput.applyRowFilter` is pgoutput\_row\_filter. The walsender's
  `publicationFilter.RowFilter` is pgoutput\_row\_filter\_init:
  - the filters of the subscribed publications that publish the change's action and cover the publish\-as
    relation are OR'ed;
  - one such publication with no filter cancels filtering for that action.

  Outcomes:
  - INSERT tests the new row, and DELETE the old \(replica\-identity\) row;
  - an UPDATE without an old row tests the new row;
  - an UPDATE with an old row becomes an INSERT \(only the new row matches\) or a DELETE \(only the old row
    matches\), or is dropped when neither matches.

  The filter runs before the Relation message, so a filtered\-out change sends nothing.
- **Evaluation.** `executor.EvalPublicationRowFilter` binds the row's text values as typed literals in a VALUES
  list and evaluates the OR of the quals. A virtual generated column is computed from its expression in an outer
  select. A NULL result does not publish.

Deviations \(ledgered\):

- check\_simple\_rowfilter\_expr's restrictions and the replica\-identity check for UPDATE / DELETE
  publications are not enforced.
- The stored text omits PG's implicit casts \(`'x'` where PG prints `'x'::text`\).
- Each evaluated row parses and plans the filter query.
- The current catalog is used, not the historic snapshot.

Tests: TestParsePublicationRowFilter, TestPublicationRowFilterDDL, TestEvalPublicationRowFilter.
