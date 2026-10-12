# M0146-0152 — a transaction that crashed before the first commit stays aborted

Status: landed 2026-10-12.
Parent: M0141-S2a-fix2r-c (S2, placed into banner item 2a by the owner on the
evening of 2026-10-12).

## Problem

Open chooses how to treat the commit log at startup. When the on-disk
commit log is empty and the WAL holds no xact record, it assumes an old
cluster created before the commit log existed (M0030-0007) and calls
`clog.InitializeAsCommitted(NextXID)`, which stamps every xid below NextXID
Committed.

A cluster this binary initialised meets the same condition after a crash
that precedes its first commit. For example, `BEGIN; CREATE TABLE ghost …`
followed by a crash: the crashed xid was stamped Committed, and the
uncommitted table came back.

- **Exposure.** The embedded `Open` path reproduces it
  (`TestCrashBeforeFirstCommitTableNotVisible`). A live server usually
  commits its own startup work before any client transaction, which puts an
  xact record in the WAL and masks the defect.
- **Why it hid.** Before the M0114 JSON catalog cache was retired, the
  second start registered tables from that cache and hid it there too.

PostgreSQL treats every xid below nextXid that never committed as aborted
after a restart (`TransactionIdDidCommit` is false; the tuple is invisible).

## Design

- **A new capability.** `clog-complete-history` is written by initdb into
  `global/pg_goopg_features`. Every xid such a cluster ever assigned is
  recorded in the commit log or in the WAL, or never committed.
- **Open honours it.** Open takes the upgrade branch only when the commit log
  is empty, the WAL has no xact record, **and** the capability is absent.
  Otherwise it runs the existing crash-recovery path: replay commit and abort
  records from the WAL, then the implicit-abort sweep, which stamps Aborted
  every catalog xid still Unknown.
- **Two kinds of cluster keep the upgrade branch:**
  - clusters initialised before the capability existed;
  - PostgreSQL base backups attached as goopg standbys. Their goopg commit
    log starts empty, and the upstream xids must read Committed.

## Verification

- **New test.** `TestCrashBeforeFirstCommitTableNotVisible` (initdb) crashes
  before the first commit, then restarts twice. The table must stay absent,
  and a table committed after the crash must survive. It fails on HEAD and
  passes now.
  - `TestCrashMidTransactionTableNotVisibleAfterRestart` and
    `TestCommittedTableSurvivesCrashRestart` still pass.
- **Live server.** An immediate stop during `BEGIN; CREATE TABLE ghost …`,
  then two restarts. The table is absent, and re-creating it commits and
  persists.
- **Marker test.** `TestGoopgFeaturesMarker` is updated for the fourth
  capability.
- **Gates.**
  - Units and the initdb / postmaster / cluster packages.
  - The `TestE2E_*` family, including `TestE2E_PGStandbyFullCycle`, which
    exercises the base-backup upgrade path.
  - The crash, recovery, restart and pg\_dump testport families.
  - tpch-spotcheck, the acceptance arm and sf025 PASS=99.

## Residuals (ledgered)

- **Unmarked goopg clusters.** A cluster initialised before this change has
  no marker, so it can still hit the upgrade branch on a crash before its
  first commit. Its first commit puts an xact record in the WAL, which
  closes the window from then on.
- **The base-backup path is still coarse.** It still stamps every xid below
  goopg's NextXID Committed. PostgreSQL's own standby reads the primary's
  pg\_xact instead.
