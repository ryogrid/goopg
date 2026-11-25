# M0149\-0043 — Port subscription/t/007\_ddl.pl

Status: done. TestPort\_Subscription007DDL is ungated and passes 3 of 3 runs in all three pairings \(M0149\-0043a–b\).

## Landed — M0149\-0043a, missing\-publication warnings

PG's `check_publications` \(subscriptioncmds.c\) asks the publisher which of the named publications exist. It reports
the missing ones in a single WARNING with SQLSTATE 42704 \(ERRCODE\_UNDEFINED\_OBJECT\):

- one missing: "publication "x" does not exist on the publisher";
- several: "publications "x", "y" do not exist on the publisher".

It runs from CREATE SUBSCRIPTION with connect = true, before the slot is created, and from ALTER SUBSCRIPTION … SET
/ ADD PUBLICATION with refresh. goopg reported these at NOTICE level from ALTER only. `ddlOp.warnMissingPublications`
now raises the WARNING, and CREATE SUBSCRIPTION calls `CheckPublicationsOnPublisher` first.

## Landed — M0149\-0043b, publication renames

Two gaps on a goopg publisher:

- **The rename itself.** ALTER PUBLICATION … RENAME TO was a silent no\-op: the legacy DDL parser consumed every
  unmodelled ALTER PUBLICATION form as a compatibility no\-op. `AlterPublicationRenameStmt` now reaches
  `PubSub.RenamePublication` \(AlterObjectRename\_internal\). The publication keeps its OID under the new name; a
  missing publication is 42704, a taken name 42710. The `pg_publication` row is rewritten under the same OID.
- **The walsender's filter.** A goopg walsender resolved its `publication_names` once, at START\_REPLICATION. PG's
  pgoutput reloads them by name whenever a publication changes \(publication\_invalidation\_cb → LoadPublications\).
  `PubSub.PublicationGeneration` now counts every publication change, and `livePublicationFilter` rebuilds the
  name → tables filter when the counter moves.

The filter is rebuilt against the current registry, not the catalog as of the decoded change \(deferral ledger\).
