# M0149\-0043 — Port subscription/t/007\_ddl.pl

Status: in progress. TestPort\_Subscription007DDL is written with the three\-pairing `tapnode` harness and gated
\(`GOOPG_TAP_007`\). The PG 18.3 publisher → goopg subscriber pairing passes.

## Landed — M0149\-0043a, missing\-publication warnings

PG's `check_publications` \(subscriptioncmds.c\) asks the publisher which of the named publications exist. It reports
the missing ones in a single WARNING with SQLSTATE 42704 \(ERRCODE\_UNDEFINED\_OBJECT\):

- one missing: "publication "x" does not exist on the publisher";
- several: "publications "x", "y" do not exist on the publisher".

It runs from CREATE SUBSCRIPTION with connect = true, before the slot is created, and from ALTER SUBSCRIPTION … SET
/ ADD PUBLICATION with refresh. goopg reported these at NOTICE level from ALTER only. `ddlOp.warnMissingPublications`
now raises the WARNING, and CREATE SUBSCRIPTION calls `CheckPublicationsOnPublisher` first.

## Remaining — M0149\-0043b

A goopg walsender resolves its `publication_names` once, at START\_REPLICATION \(`buildPublicationFilter`\). After the
test renames `pub_for_tab` away and renames the empty publication to `pub_for_tab`, the stream still carries test1.
PG's pgoutput reloads its publications by name on every publication invalidation \(publication\_invalidation\_cb →
LoadPublications\), using the catalog as of the decoded change.
