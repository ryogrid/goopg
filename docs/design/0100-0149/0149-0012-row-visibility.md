# 0149-0012 — Port recovery/t/021\_row\_visibility.pl

Status: done.

Upstream: `postgres/src/test/recovery/t/021_row_visibility.pl`; procarray.c KnownAssignedXids, xact.c `xact_redo`
\(PREPARE / COMMIT PREPARED / ABORT PREPARED\).

## What the test checks

One long\-lived psql session on the primary and one on a hot standby. The standby must not see an uncommitted
UPDATE, must see it once committed, and must see a prepared transaction's insert only after COMMIT PREPARED \(and
never after ROLLBACK PREPARED\).

## Harness

`tapnode.PumpedPSQL` is the `.pl`'s IPC::Run psql: `psql --no-psqlrc --no-align --file -` with stdin fed query by
query and stdout accumulated; `SendQueryAndWait` is `send_query_and_wait`. The port runs three pairings: all\-goopg,
PG 18.3 primary → goopg standby, goopg primary → PG 18.3 standby.

## Fixes

- **A goopg hot standby saw uncommitted primary changes.** The primary's running xid is not in the standby
  process's own in\-progress list, and its clog status is still unknown, so the snapshot's window test called it
  committed. PG's standby snapshot holds it in KnownAssignedXids until its commit record replays. `Manager` now has
  an in\-recovery flag, set when the standby starts and cleared at promotion; its snapshots consider a normal xid
  committed only when clog says so. Replayed commits stamp clog \(M0149\-0010a\), so that is the signal.
- **Two\-phase records from a PG primary were refused.** XLOG\_XACT\_PREPARE changes no page — the prepared
  transaction's heap changes are their own, already replayed records — and now replays as a no\-op. COMMIT PREPARED
  and ABORT PREPARED do the commit/abort page work and finish the record's twophase xid \(xl\_xact\_twophase, now
  parsed by `ParseXactRecord` after the dropped\-stats and invalidation chunks\): the stream replayer and the
  crash\-recovery clog pass stamp that xid, not the header xid.

## Not covered

- No pg\_twophase state is kept for a primary's prepared transaction on a goopg standby: after promotion or a crash
  restart of the standby, a still\-prepared transaction is swept to aborted instead of being restored
  \(PrepareRedoAdd / restoreTwoPhaseData\).
- The standby snapshot does not model KnownAssignedXids' xmin horizon; a hinted\-committed tuple
  \(SeesCommittedXIDHinted\) skips the clog check.
