# M0149\-0053 — Port subscription/t/025\_rep\_changes\_for\_schema.pl

Status: done. TestPort\_Subscription025RepChangesForSchema passes 3/3 in all three pairings \(all\-goopg; PG 18.3
publisher → goopg subscriber; goopg publisher → PG 18.3 subscriber\).

025 publishes a schema with `FOR TABLES IN SCHEMA sch1`, which covers two plain tables and a partitioned table whose
partitions live in `public`. It then checks that:

- a table created in the schema later joins on REFRESH PUBLICATION;
- a table moved out with ALTER TABLE … SET SCHEMA stops replicating and leaves pg\_subscription\_rel on the next
  refresh;
- a dropped table leaves on the next refresh;
- `ALTER PUBLICATION … DROP TABLES IN SCHEMA` stops the stream between two inserts.

The goopg subscriber already handled the subscriber side, so the PG 18.3 publisher pairing passed as written. The
goopg publisher needed schema publications \(0053a\) and a working SET SCHEMA \(0053b\).

## Landed — M0149\-0053a, FOR TABLES IN SCHEMA publications

- **Grammar.** gram.y pub\_obj\_list: `TABLE …` and `TABLES IN SCHEMA { name | CURRENT_SCHEMA }` items in any mix. A
  bare item after a comma continues the previous kind \(preprocess\_pubobj\_list\). This holds for CREATE PUBLICATION
  and for ALTER PUBLICATION ADD / SET / DROP; CURRENT\_SCHEMA is carried as `parser.PubCurrentSchema`. Column lists and
  row filters on ALTER remain the compat no\-op.
- **Catalog.** `Publication.Schemas` holds schema names and is replaced, never mutated; `PubSub.SetPublicationSchemas`
  sets it. `catalog.PublicationSchemaTables` is GetSchemaPublicationRelations with PUBLICATION\_PART\_LEAF: the
  schema's publishable tables, with a partitioned table replaced by its leaf partitions, which can live in other
  schemas. `catalog.PartitionAncestorSchemas` gives get\_partition\_ancestors' schemas.
- **DDL** \(publicationcmds.c\). Items resolve the way ObjectsInPublicationToOids does:
  - CURRENT\_SCHEMA is the first existing search\_path schema; none is 3F000 "no schema has been selected for
    CURRENT\_SCHEMA";
  - a missing schema is 3F000;
  - pg\_catalog, information\_schema, pg\_toast or a temporary schema is 22023 "cannot add schema … to publication";
  - duplicates collapse.

  AlterPublicationSchemas: ADD of a member is 42710 "schema … is already member of publication …", DROP of a
  non\-member is 42704 "tables from schema … are not part of the publication", and SET replaces the list. SET with
  only tables therefore clears the schemas, as in PG. A FOR ALL TABLES publication gets 55000 with the "Schemas
  cannot be added to or dropped from …" detail.
- **Durability.** One pg\_publication\_namespace heap row \(oid, pnpubid, pnnspid\) per member, plus entries in both
  indexes \(6238 oid, 6239 pnnspid\+pnpubid; their split key metadata is registered\). DROP / SET stamp the rows, and
  DROP PUBLICATION stamps all of them. The startup reload maps pnnspid back to the schema name.
- **Readers.** pg\_get\_publication\_tables and the pg\_publication\_tables view add the schema tables, deduplicated
  against FOR TABLE members. pg\_publication\_namespace is no longer an empty stub.
- **Walsender.** The filter's `bySchema` publishes a relation whose own schema, or a partition ancestor's schema, is a
  member \(get\_rel\_sync\_entry: GetSchemaPublications of the namespace, then of each ancestor\). Such a relation is
  published whole, without a column list. `RelationDef.AncestorSchemas` carries the ancestors. The snapshot's
  live\-definition check \(M0149\-0042b\) already refreshes a relation whose schema changed.

## Landed — M0149\-0053b, ALTER TABLE … SET SCHEMA

The SET SCHEMA arm only assigned `tbl.Schema`. The catalog map stayed keyed by the old qualified name, so
`newschema.t` did not resolve, while pg\_class already showed the new schema. `catalog.SetTableSchema` re\-keys the
table and its indexes together. A name already taken in the target schema is 42P07 "relation … already exists in
schema …" and changes nothing. Sequences keep their existing registry\-rename path.

Tests: TestParsePublicationTablesInSchema, TestPublicationSchemaTables, TestPublicationFilterTablesInSchema.

## Behaviour that matched without a change

025 creates `sch1.tab3` with CREATE TABLE AS on the publisher before the subscriber has the table. Both PG and goopg
decode the CTAS rows, because the schema publication already covers the new table. The subscriber's apply worker
errors once \(PG: "logical replication target relation … does not exist"\), restarts, and then skips the relation,
which is not READY in pg\_subscription\_rel. The test tolerates the transient error.

## Open residuals

- M0149\-0053c: SET SCHEMA is in\-memory only and reverts on restart \(verified\). The row type, owned sequences and
  constraints do not move.
- Publications still resolve against the current registry rather than the historic snapshot \(M0149\-0052a's
  ledger row\). 025's `INSERT 21; ALTER PUBLICATION … DROP TABLES IN SCHEMA; INSERT 22` passed 3/3, but it depends on
  the walsender decoding the first insert before the ALTER commits.
- No superuser check for FOR TABLES IN SCHEMA \(PG requires superuser\); publish\_via\_partition\_root is not modelled.
