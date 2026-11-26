# M0149\-0052 — Port subscription/t/024\_add\_drop\_pub.pl

Status: done. TestPort\_Subscription024AddDropPub passes 3/3 in all three pairings \(all\-goopg; PG 18.3 publisher →
goopg subscriber; goopg publisher → PG 18.3 subscriber\).

024 checks that ALTER SUBSCRIPTION … DROP / ADD PUBLICATION refresh the subscription's table list and copy the data
of newly added tables. It also checks that ALTER SUBSCRIPTION … SET PUBLICATION naming a publication that does not
exist yet keeps replication alive: the publisher's walsender restarts \(a new pid in pg\_stat\_replication\), logs
pgoutput's `WARNING: skipped loading publication "tap_pub_3"` on the next change, and streams again once the
publication is created and the subscription refreshed. The goopg subscriber already handled all of it, so the
PG 18.3 publisher pairing passed as written. The goopg\-publisher pairings passed the three ADD/DROP checks and
stopped at the missing\-publication part.

## Landed — M0149\-0052a, the goopg publisher

- **pg\_stat\_replication.pid.** PG reports the walsender's backend pid \(pg\_stat\_get\_wal\_senders, `walsnd->pid`\).
  goopg's physical and logical walsenders registered PID 0, and the virtual view typed every column as text, so 024's
  `SELECT pid != <old pid> …` failed with 42804. HandleCommand's backend pid now goes through
  replyStartReplication into both walsenders' `SenderState.PID`, and the view's pid column is int4. The other
  columns are still text \(M0149\-0052b\).
- **LoadPublications' warning.** PG's pgoutput loads the stream's publications lazily: get\_rel\_sync\_entry calls
  LoadPublications when `publications_valid` is false, which is the case at startup and after any publication
  invalidation. LoadPublications looks up each name with missing\_ok and logs WARNING "skipped loading publication
  \"%s\"" \(errdetail "The publication does not exist at this point in the WAL.", errhint "Create the publication if it
  does not exist."\) for one that is absent. goopg's `livePublicationFilter` used to build its filter eagerly and
  skipped unknown names silently. It now loads on the first Allows / ColumnList / PublishesStoredGenerated call
  after construction or after a registry generation change, and warns once per load for each missing name. The
  detail and hint are slog attributes.

Test: TestLivePublicationFilterWarnsMissingPublication.

## Open residuals

- M0149\-0052b: the other pg\_stat\_replication columns are text \(PG: oid, inet, int4, timestamptz, xid, pg\_lsn,
  interval\); usesysid, usename and client\_port are empty.
- PG resolves the publications against the historic catalog snapshot \("at this point in the WAL"\). goopg's live
  filter reads the current registry, so a publication created after a change was written but before it was decoded
  already applies to that change. This is a known approximation of M0149\-0043b's live filter.
