# M0149\-0060 — Port subscription/t/031\_column\_list.pl

Status: done. TestPort\_Subscription031ColumnList passes 3/3 in all three pairings \(all\-goopg; PG 18.3
publisher → goopg subscriber; goopg publisher → PG 18.3 subscriber\).

031 publishes tables with column lists \(`FOR TABLE t \(a, b\)`\) and checks:

- the initial copy and streamed changes carry only the listed columns;
- partitioned tables, with and without publish\_via\_partition\_root;
- replica\-identity changes;
- dropped and generated columns;
- the old\-tuple column count in the binary pgoutput stream;
- the two "different column lists" errors: at CREATE SUBSCRIPTION, and from the walsender.

goopg already modelled column lists on CREATE PUBLICATION and in pgoutput's masking. The gaps were around them.

## goopg subscriber

- **DROP SCHEMA … CASCADE** now drops the partitions of the schema's tables even when they live in another schema.
  In PG a partition is an AUTO dependency of its parent \(findDependentObjects\), so it gets no "drop cascades
  to" line. Before this change, `public.t_1`, a partition of `s1.t`, survived, and the test's re\-creation failed.
- **Conflicting column lists.** `FetchPublicationTables` raises `ColumnListConflictError` when the DISTINCT
  \(schema, name, attrs\) list repeats a table: fetch\_table\_list's list\_member check. The error is raised:
  - in the pre\-slot publication check \(CheckPublicationsOnPublisher\), so a conflicting CREATE SUBSCRIPTION creates
    neither the slot nor the subscription, as in PG, where fetch\_table\_list precedes walrcv\_create\_slot;
  - in the refresh.

  Both report PG's 0A000 "cannot use different column lists for table "s.t" in different publications".

## goopg publisher

- **Grammar.** `parsePubColumnList` is shared by CREATE and ALTER PUBLICATION and accepts quoted names. ALTER …
  ADD / SET now carry the list; it used to be a compat no\-op.
  - SET gives each listed table the list it is listed with, or none, and re\-journals a member whose list changed.
  - DROP with a list is PG's 42601.
- **COPY TO** writes an enum's label \(enum\_out\). The KindEnum arm was missing, so a publisher's table sync of
  an enum column failed.
- **SQL slot functions.** `pg_create_logical_replication_slot` creates a pgoutput slot at the end of the last
  record, like CREATE\_REPLICATION\_SLOT … LOGICAL. In FROM, it and `pg_create_physical_replication_slot` scan as
  one \(slot\_name, lsn\) record. `int2send` / `int4send` / `int8send` return the value in network byte order.
- **pgoutput\_column\_list\_init.** `xlog.ColumnListChecker` / `publicationFilter.CheckColumnLists` compare the
  covering publications' column sets for the publish\-as relation. No list, FOR ALL TABLES or TABLES IN SCHEMA
  means every published column. A difference is the ERROR: the walsender logs it and ends the stream with 0A000.
  The publication filter still takes the union for the columns it sends; only the check is PG's.

Deviations \(ledgered\):

- pg\_create\_logical\_replication\_slot refuses temporary slots and accepts only pgoutput \(PG loads any output
  plugin\).
- The CREATE SUBSCRIPTION conflict check runs before the missing\-publication WARNINGs are emitted.

Tests: TestDropSchemaCascadeDropsForeignSchemaPartitions, TestCopyTextEnumAndIntSend,
TestAlterPublicationColumnLists, TestPublicationFilterColumnListConflict, TestParseAlterPublicationColumnList.
