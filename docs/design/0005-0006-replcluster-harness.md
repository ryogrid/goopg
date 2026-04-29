# 0005-0006 — replcluster Harness + E2E Acceptance Test (M0005)

Status: accepted (2026-04-29)

Milestone: [`0005-streaming-replication-support.md`](../milestones/0005-streaming-replication-support.md)

Predecessors:
- [`0005-0001-streaming-replication-architecture.md`](0005-0001-streaming-replication-architecture.md)
  declared the acceptance test shape this loop ships.
- [`0005-0005-promotion.md`](0005-0005-promotion.md) — owns the
  PROMOTE flow this harness exercises.
- [`0005-0003-replication-observability.md`](0005-0003-replication-observability.md) —
  the test reads through both stat views to verify streaming.
- [`0004-0001-go-test-utility-library.md`](0004-0001-go-test-utility-library.md)
  — the single-cluster `internal/testutil/cluster` API this
  harness composes.

## Why

Until this loop the M0005 acceptance criterion (DoD #4 of the
milestone) had no executable cover: every wire-level mechanic was
unit-tested intra-process, but the proof that two `goopg start`
binaries can negotiate streaming replication out-of-process didn't
exist. A harness that orchestrates a primary+standby pair and
exercises the wire path end-to-end closes that gap and gives future
loops (logical replication, base backup, multi-timeline) a place to
hang their integration tests.

This loop also surfaces and fixes a quieter gap: `goopg start`
required an explicit `-config` flag to consume `postgresql.conf`,
so a standby with a `primary_conninfo` line in its conf file was
silently ignored. Auto-discovering `<datadir>/postgresql.conf`
matches upstream pg_ctl behaviour and is the right default.

## Components

### `internal/testutil/replcluster/replcluster.go` — the harness

Wraps a pair of `*cluster.Cluster` handles plus the orchestration
needed to bring up a primary→standby pair from scratch.

`Setup()` runs the v0 bootstrap sequence (no `pg_basebackup` yet):

1. Init the primary's data dir.
2. Pre-create a physical replication slot on the primary's data
   dir while it's offline. We write the slot state file directly
   via `wal.OpenSlots(...).Create(...)`. The on-disk format is
   identical to one created via the wire-level
   `CREATE_REPLICATION_SLOT` command; no special-case
   reconstitution path is needed when the primary boots.
3. Start the primary so the initial WAL segment + pg_replslot
   tree are durable.
4. Stop the primary cleanly, init the standby's data dir, then
   `cloneDataDir` from primary→standby. The clone walker copies
   regular files + makes empty dirs but skips `postmaster.pid`
   and `.goopg.ctl.sock` (those are owned by the running primary
   and would race the standby on boot).
5. Stamp the standby with `standby.signal` and append
   `primary_conninfo = 'host=… port=…'` + `primary_slot_name = '…'`
   to its `postgresql.conf`. The auto-discover change in
   `cmd/goopg/main.go` ensures the standby actually reads them.
6. Restart the primary, then start the standby. The auto-spawned
   walreceiver dials the primary, the slot becomes active, and
   WAL streams.

`Stop()` gracefully tears both clusters down (errors joined so a
transient standby-side failure doesn't hide a primary-side one).

`Promote()` shells out to `goopg promote -D <standby data dir>`,
which sends PROMOTE over the standby's control socket. Returns once
the promotion is durable (drain complete, `standby.signal` removed).

### `internal/testutil/replcluster/replcluster_test.go` — the e2e test

`TestReplicationEndToEnd` is the M0005 acceptance test. It:

1. Calls `Setup()` to bootstrap primary+standby.
2. Polls the standby's `pg_stat_wal_receiver` until status reaches
   `streaming` (proves the wire connection is up).
3. Cross-checks the primary's `pg_stat_replication`: a row for
   the standby's slot must exist with state=`streaming`.
4. Snapshots the standby's `written_lsn`, drives a `CHECKPOINT`
   on the primary, and verifies the standby's value advances
   (proves WAL bytes are flowing through the wire).
5. Calls `Promote()` and verifies `standby.signal` is gone.

The test is gated on `-short` (each go-run-driven cluster takes
seconds to bring up).

`TestReplClusterNewValidates` is the cheap argument-validation
guard.

### `cmd/goopg/main.go` — auto-discover postgresql.conf

When `runStart` sees `-D <dir>` and `-config` is empty, it now
checks for `<dir>/postgresql.conf` and uses it if present. This is
a one-line behaviour change that makes the harness work without
plumbing `-config` through `cluster.Options`. Mirrors upstream
pg_ctl, which always reads `<datadir>/postgresql.conf`.

## Why this acceptance shape (not row-level visibility)

The milestone DoD originally said "write to primary, observe row
visibility on standby." v0 doesn't yet persist its in-memory
catalog across processes, so a `CREATE TABLE` on the primary is
invisible to the standby's executor — even though the WAL records
that mutated the underlying pages flow through. The test instead
verifies:

- **Wire connectivity** via `pg_stat_wal_receiver`.
- **WAL byte flow** via `pg_stat_replication.sent_lsn` and
  `pg_stat_wal_receiver.written_lsn`.
- **Promotion** via `standby.signal` removal.

These are the strongest end-to-end checks possible at this
milestone. Once catalog persistence lands (planned alongside the
on-disk `pg_class` work in milestone 7+), this test gains a row-
visibility step without needing a new harness shape.

## Out of scope

- **`pg_basebackup`-equivalent online clone.** v0 ships an
  offline copy in `Setup()` so the harness works without a
  base-backup wire command. Once
  `BASE_BACKUP` lands, `Setup()` will gain a `BaseBackup()`
  alternative path.
- **Test-driven kill-the-primary scenario.** The test does a clean
  Promote, not a killed-primary failover. Real failover testing
  needs orphan-process handling and pidfile cleanup that's a
  separate slice of work; the explicit-promote case proves the
  wire path is sound.
- **Multi-standby cascade.** Single-pair only. Cascading
  replication (standby→standby) is documented out-of-scope in the
  architecture doc.

## Cross-references

- Milestone:
  [`docs/milestones/0005-streaming-replication-support.md`](../milestones/0005-streaming-replication-support.md).
- Single-cluster harness:
  [`0004-0001-go-test-utility-library.md`](0004-0001-go-test-utility-library.md).
- Promotion path:
  [`0005-0005-promotion.md`](0005-0005-promotion.md).
- Observability views:
  [`0005-0003-replication-observability.md`](0005-0003-replication-observability.md).
