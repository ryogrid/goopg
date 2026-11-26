# M0149\-0057 — Port subscription/t/027\_nosuperuser.pl

Status: done. TestPort\_Subscription027Nosuperuser passes 3/3 in all three pairings \(all\-goopg; PG 18.3
publisher → goopg subscriber; goopg publisher → PG 18.3 subscriber\).

027's subscription is owned by a role whose superuser bit and role memberships the test changes. Replication
must stop with PG's errors when the owner cannot act as the table owner, when the table forces row\-level
security, or when the table owner lacks INSERT / UPDATE / DELETE / SELECT. It must then resume from exactly where
it stopped once the problem is fixed. A hash\-partitioned table is published with publish\_via\_partition\_root,
and the two nodes use different hash remainders. The password\_required block needs Unix\-domain sockets and is
skipped, as upstream does without them; tapnode connects over TCP.

## Landed — M0149\-0057c, the goopg publisher re\-sends what the subscriber has not confirmed

The test makes the PG subscriber fail and reconnect many times. A goopg publisher sometimes lost the failed
transaction for good. Three defects combined:

1. **The slot moved on decode.** SlotDecoder.Run advanced confirmed\_flush\_lsn, and with it restart\_lsn, at every
   decoded commit. PG moves confirmed\_flush only on the subscriber's flush feedback
   \(LogicalConfirmReceivedLocation\). The logical walsender now sets `SetFeedbackOnly`.
2. **Every message was stamped with the commit end.** goopg decodes a transaction at its commit record and stamped
   the CopyData walEnd of B, the changes and C with that record's end. The PG apply worker raises last\_received
   from each message header and, when it has drained the socket with no locally pending transaction, confirms
   flush = last\_received. So it confirmed a transaction as soon as its BEGIN had arrived. PG's write\_location is
   `txn->first_lsn` for BEGIN, the change's LSN for a change, and `txn->end_lsn` for COMMIT
   \(begin/change/commit\_cb\_wrapper\). `Decoder.SetWriteLocationHook` now reports those positions, and the
   walsender stamps each message with them.
3. **restart\_lsn followed the flush position into an unfinished transaction.** PG keeps restart\_lsn at the oldest
   running transaction \(candidate\_restart\_lsn from xl\_running\_xacts\). goopg now records, for every transaction,
   the start of its first record \(ReorderBuffer `restartLSN`, from `Decoder.SetRecordStart`\).
   `SlotDecoder.RestartCapFor(flush)` caps restart\_lsn at the earliest restart point of any transaction that was
   sent but commits after flush, and of any still being decoded. `Slots.AdvanceLogicalFeedback` applies it.

On reconnect the decoder skips what the subscriber already has: start\_decoding\_at = max\(client start point,
confirmed\_flush\), and a commit or non\-transactional message ending at or before it is dropped
\(SnapBuildXactNeedsSkip; `Decoder.SetSkipThrough`\).

## Landed — M0149\-0057d, publish\_via\_partition\_root

- `Publication.PublishViaRoot` covers the option, ALTER … SET, the pg\_publication.pubviaroot journal and reload,
  and the views.
- With it set, the table lists \(pg\_get\_publication\_tables, pg\_publication\_tables\) keep a partitioned member as
  itself. Schema publications list partitioned tables and drop partitions \(PUBLICATION\_PART\_ROOT,
  `catalog.PublicationSchemaTablesMode`\).
- The walsender filter's `PublishAsAncestor` is GetTopMostAncestorInPublication over the viaroot publications.
  PgOutput then sends the partition's change as the root's \(Relation message and OID\) when the two column
  layouts match \(`sameTupleLayout`\). PG converts other layouts through a tuple conversion map; goopg sends those
  as the leaf \(ledgered\).

Tests: TestDecoderWriteLocationsAndSkip, TestRestartCapFor.

## Landed — M0149\-0057a, the apply worker acts as the table owner

PG \(worker.c, usercontext.c\): with run\_as\_owner = false, every change runs as the table's owner.
SwitchToUntrustedUser requires that the subscription owner may SET ROLE to the table owner, else
`role "X" cannot SET ROLE to "Y"`; it stays the subscription owner when the table owner may SET ROLE back.
TargetPrivilegesCheck requires the action's privilege — INSERT, UPDATE or DELETE, plus SELECT for the row lookup —
else aclcheck\_error's `permission denied for table t`. It refuses a table whose row\-level security applies to that
user, `user "Y" cannot replicate into relation with row-level security enabled: "t"`. maybe\_reread\_subscription
LOGs `… will restart because the subscription owner's superuser privileges have been revoked` and restarts the
worker.

goopg \(applyworker\_privilege.go\):

- `applyTableAccess` implements the SET ROLE check \(superuser, identity or membership\), the privilege checks
  \(through `dmlPrivilegePermittedAs` as the acting role\) and check\_enable\_rls \(applies when FORCE ROW LEVEL
  SECURITY is set for the owner, or for a non\-owner; never for BYPASSRLS\). It runs for INSERT, UPDATE and DELETE,
  routed or not.
- `applyCheckOwnerSuperuser` records the owner's superuser status at worker start and re\-checks it at each BEGIN
  and on every keepalive between transactions \(`CheckSubscription`; PG also re\-reads while idle\).
- Every apply `ExecError` is now logged in PG's server\-log form \(errmsg, then DETAIL\), not only as an escaped
  attribute.

## Landed — M0149\-0057b, table sync of a partitioned remote table

`FetchRemoteColumns` reads the remote relkind \(fetch\_remote\_table\_info\). For anything but an ordinary table, it
copies with `COPY (SELECT cols FROM rel)` \(copy\_table\), which a PG publisher accepts for a partitioned root
published via the root.

## Landed — M0149\-0057e, UPDATE / DELETE routing into partitions

`applyDeletePartitioned` / `applyUpdatePartitioned` \(apply\_handle\_tuple\_routing\) route the remote old tuple to
its leaf and look the row up there. An UPDATE stays in that leaf when the new tuple routes to it, and otherwise
becomes a delete there plus an insert into the new leaf. Conflict checks and privilege checks run against the
leaves.

## Landed — M0149\-0057f, a partial REVOKE from a table's owner

goopg keeps the owner's aclitem under the bookkeeping key "postgres". `IsOwnerACLRevoked` used to be true only when
that entry was fully emptied, so after `REVOKE SELECT, INSERT … FROM owner` the owner still had every privilege.
`HasOwnerPrivilege` also went through HasTablePrivilege's indirect step, where the bootstrap superuser "has
privileges of" every grantee, so a GRANT to anyone reached the owner. Now a materialized owner entry is
authoritative, `HasOwnerPrivilege` reads only it and PUBLIC, and the owner's real role adds what it reaches through
its memberships.

Tests: TestOwnerACLPartialRevoke, TestPort\_Subscription027Nosuperuser.

## Open residuals

- run\_as\_owner = true is not modelled; there is no subscription option.
- member\_can\_set\_role ignores the membership's SET option \(RoleIsMemberOf\).
- The owner reverts to the bootstrap superuser after a restart \(existing ledger row M0134\-0004\), and relacl
  renders the owner entry as "postgres".
- Triggers do not fire on routed UPDATE / DELETE.
- The superuser re\-check runs on keepalives, not on a pg\_authid invalidation.
