# M0149\-0048 — Port subscription/t/011\_generated.pl

Status: done. TestPort\_Subscription011Generated passes 3/3 in all three pairings \(all\-goopg, PG 18.3 publisher →
goopg subscriber, goopg publisher → PG 18.3 subscriber\).

## Landed — M0149\-0048a, a goopg publisher publishes only the columns PG publishes

PG's pgoutput writes a column only when logicalrep\_should\_publish\_column admits it \(proto.c\). A dropped column is
never published. With a column list, exactly the listed columns are published. Otherwise ordinary columns are
published, a STORED generated column only under publish\_generated\_columns = stored, and a VIRTUAL one never. goopg
wrote every column into the Relation message and every tuple. A PG subscriber therefore rejected the relation \("has
incompatible generated columns: "b", "c""\), and a goopg subscriber stored the publisher's generated values.

- Publications model pubgencols \(`Publication.PublishGeneratedColumns`, 'n' / 's'\). `publish_generated_columns`
  parses as defGetGeneratedColsOption does \(none / stored, else 42601 with PG's message and detail\). ALTER
  PUBLICATION SET copies it. The pg\_publication heap row now carries the tenth attribute \(pubgencols, char\), as
  FormData\_pg\_publication does, and the reload reads it \(an older 9\\-attribute row reads 'n'\). The pg\_publication
  views show it.
- `ColumnDef.Generated` carries attgenerated into the decoding snapshot.
- `PgOutput.publishedColumns` is the column rule \(no column lists yet\). It applies to the Relation message, the new
  tuple and the old key / old row. The tuple walk still decodes every stored column so offsets stay right.
- The walsender's publication filter implements `xlog.GenColsFilter`: a covering publication with
  publish\_generated\_columns = stored publishes stored generated columns. PG errors when a table's publications
  disagree; goopg takes their union \(ledgered\).

Test: TestPgOutputGeneratedColumns. In TAP 011 all\\-goopg, the publish\_generated\_columns = none incremental
assertion now passes, and stored\\-mode incremental rows carry the generated value.

## Landed — M0149\-0048b, pg\_get\_publication\_tables returns PG 18's columns

A PG 18 subscriber's tablesync \(fetch\_remote\_table\_info\) takes the set of columns to copy from
`CASE WHEN array_length(gpt.attrs, 1) = c.relnatts THEN NULL ELSE gpt.attrs END`. It no longer filters on
attgenerated for an 18 publisher. goopg returned \(relid, attrs text, qual text\) with attrs always NULL, so the PG
subscriber copied every column, generated ones included, and rejected the table.

- The SRF now has PG's shape \(pubid oid, relid oid, attrs int2vector, qual pg\_node\_tree\) in all four schema sites:
  the FROM\-clause plan, the ProjectSet composite schema, the analyzer's base columns and its composite columns.
- attrs is pg\_publication.c's list without a column list \(`publicationPublishedAttnums`\): every non\-dropped
  column except virtual generated ones, plus stored generated ones only under publish\_generated\_columns = stored. It is
  carried as int2vector text, as the virtual pg\_index.indkey cell is, and array\_length works on it.

Tests: TestPublicationPublishedAttnums; the pg\_get\_publication\_tables shape tests now expect four columns. In TAP
011 goopg\-publisher\-pg18\-subscriber, every assertion before the column\-list DDL passes.

Found while probing: generated columns stop being generated after a goopg restart \(M0149\-0100, S2, filed\).

## Landed — M0149\-0048c, a goopg subscriber owns its generated columns

- **Apply.** applyInsert computes the table's generated columns after the BEFORE ROW triggers \(ExecComputeStoredGenerated
  in ExecSimpleRelationInsert\). applyUpdateByKey recomputes them from the merged new row before the heap write, which
  covers the trigger branch too. Before this the rows read `4|||`.
- **Incompatible targets.** A replicated column that maps onto a generated subscriber column is refused, as in
  logicalrep\_report\_missing\_or\_gen\_attrs, with 55000 and PG's text \(`IncompatibleGeneratedColumnsError`\). The
  check runs in the apply worker's column map and in goopg's tablesync \(copyColumnIndex, after the missing\-column check
  PG prioritises\).
- **Tablesync column set.** goopg's `FetchRemoteColumns` was fetch\_remote\_table\_info for a pre\-18 publisher
  \(attgenerated = '' filter, no column lists\). It now follows the 18 path: the column set from
  `pg_get_publication_tables(...).attrs` over the subscription's publications \(refused when two publications give
  different lists\), every live attribute in attnum order kept when the set includes it, and gencol\_published when one
  of them is generated. copy\_table's `COPY (SELECT cols FROM rel)` form is used when gencol\_published. With a PG
  publisher this also makes the stored\-generated\-column and column\-list initial syncs right \(0048d's and 0048e's
  subscriber halves\).

Tests: TestApplyColumnMapRejectsGeneratedTarget, TestComputeGeneratedColumnsOverwritesReplicatedRow. TAP 011
pg18\-publisher\-goopg\-subscriber passes 11/12; the last assertion waits for the t1 error after CREATE SUBSCRIPTION
returns, which needs a retried tablesync \(M0149\-0048f\).

## Landed — M0149\-0048e, publication column lists

- **Parser.** `FOR TABLE t (a, b)` is gram.y's `TABLE relation_expr opt_column_list`
  \(`CreatePublicationStmt.TableColumns`, parallel to Tables\). This is the hand\-written parser; the goldens are
  unchanged.
- **DDL.** `publicationColumnList` is pub\_collist\_validate: an unknown column is 42703, a system or virtual generated
  column 42P10, a repeated one 42710. The list is stored in table order, as prattrs is sorted.
- **Catalog.** `Publication.Columns` maps each member table to its list. The map is replaced, never mutated, and is
  pruned when ALTER PUBLICATION changes the members. pg\_publication\_rel.prattrs is written as an int2vector of the
  attnums and decoded on reload \(`int2VectorDatumAttnums`: the heap decode yields the array body without its varlena
  header\). The virtual pg\_publication\_rel view shows it.
- **Publishing.** pg\_get\_publication\_tables' attrs is the list when there is one. `PgOutput.publishedColumns` gains
  the rule's first arm: a column list wins over publish\_generated\_columns \(`xlog.ColumnListFilter`, implemented
  by the walsender filter; a covering publication without a list publishes the whole table\).

Tests: TestCreatePublicationColumnList, TestPublicationColumnList, TestPgOutputColumnList, TestInt2VectorDatumAttnums.
TAP 011 goopg\-publisher\-pg18\-subscriber passes 12/12. all\-goopg and pg18\-publisher\-goopg\-subscriber pass 11/12 and
wait only on the t1 error log \(M0149\-0048f\).

## Landed — M0149\-0048f, a failed table sync is retried

PG's tablesync worker exits on an ERROR, and the apply worker relaunches it after wal\_retrieve\_retry\_interval
\(process\_syncing\_tables\_for\_apply → logicalrep\_worker\_launch\), so the ERROR repeats in the subscriber log until
the cause is fixed. goopg runs the initial sync inside CREATE / ALTER SUBSCRIPTION, logged a failure once and left the
rel at i / d until the next refresh.

- The sync step is `runSubscriptionTableSyncs` \(run the manager, log each failed rel, promote the completed ones\).
- When a rel fails, `scheduleTableSyncRetry` arms a per\-subscription timer for wal\_retrieve\_retry\_interval \(read
  from the GUC registry, default 5s\). It re\-runs the sync for the rels still waiting, and re\-arms on failure.
- It stops when the subscription is dropped or disabled, or when nothing is pending; a newer refresh replaces a pending
  timer.

TestPort\_Subscription011Generated is ungated and passes 3/3 in all three pairings.
