# M0149\-0048 — Port subscription/t/011\_generated.pl

Status: in progress \(0048a, 0048b landed\). TestPort\_Subscription011Generated is written \(three pairings\) and gated by GOOPG\_TAP\_011. The
first run's failures split into five children, each named by a failing assertion.

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

## Open
- **M0149\-0048c**: a goopg subscriber computes its own generated columns on apply \(INSERT / UPDATE\). When a published
  column maps to a local generated column, it raises PG's "logical replication target relation "%s" has incompatible
  generated columns: %s" \(logicalrep\_rel\_open\).
- **M0149\-0048d**: goopg's tablesync copies a publisher's stored generated columns when they are published \(PG 18
  copy\_table's COPY \(SELECT …\) form when gencol\_published\).
- **M0149\-0048e**: publication column lists — FOR TABLE t \(cols\) grammar, pg\_publication\_rel.prattrs, the pgoutput
  column rule, pg\_get\_publication\_tables attrs, and goopg tablesync honouring a remote column list.
