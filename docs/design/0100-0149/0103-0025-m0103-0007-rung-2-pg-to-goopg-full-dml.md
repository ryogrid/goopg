# M0103-0007 rung 2 — PG-publisher → goopg-subscriber full DML round-trip

## Status

accepted

## Context

M0103-0007 is the Scenario A E2E milestone: a PG primary publishes via
logical replication to a goopg subscriber, the PG primary dies (`kill -9`),
and a libpq multi-host client reconnects to the goopg subscriber as the new
read-write target. The full failover wiring (`pgbench`, `kill -9`, multi-host
reconnect) is the principal remaining work.

Rung 1 (`0103-0024-apply-worker-index-maintenance.md`) closed the
fresh-session-visibility caveat carried over from M0103-0006:
`ApplyWorker.applyInsert` and `applyUpdateByKey` now call
`maintainUniqueIndexesForInsert` so PK IndexScan probes find rows the apply
worker wrote. The note in that rung flagged two deferred follow-ups inside
M0103-0007 scope:

  - UPDATE old-tuple / DELETE index-entry deletion
  - Non-unique secondary indexes

Both follow-ups were marked deferred-with-caveat: "goopg's IndexScan tolerates
orphaned entries via heap re-fetch + visibility re-check, so a Scenario A test
only needs to close these if a false-positive surfaces." Rung 2 verifies that
caveat under a real end-to-end DML stream.

The symmetric Scenario B milestone (M0103-0008) was closed by extending
`TestPort_PgoutputInteropGoopgToPG` to drive a full
`INSERT(1) / INSERT(2) / UPDATE / DELETE` round-trip through the pubsubcluster
harness and verifying the subscriber's final state matches expectations
(`id=2 v='updated'` visible; `id=1` deleted). The same shape is the natural
next rung for Scenario A.

## Decision

Add `TestPort_PgoutputInteropPGToGoopgFullDML` in
`internal/testport/pgoutput_interop_test.go`. Mirrors the structure of
`TestPort_PgoutputInteropGoopgToPG` but with the direction inverted:

  - `pubsubcluster.NewMixed` with `PublisherKind=ClusterKindPG` and
    `SubscriberKind=ClusterKindGoopg`.
  - Manually issue `CREATE TABLE public.t (id int PRIMARY KEY, v text)` on
    both ends so the subscriber has a matching local table for the apply
    worker (goopg's apply path is no-COPY).
  - Pre-create the logical slot on the PG publisher via
    `pg_create_logical_replication_slot`. goopg's `CREATE SUBSCRIPTION`
    does not yet dial the publisher to issue `CREATE_REPLICATION_SLOT`, so
    the slot must exist before the subscription starts (mirror the
    `TestPubSubClusterSmokePGToGoopg*` tests).
  - `CREATE SUBSCRIPTION` on goopg with
    `(enabled=true, copy_data=false, slot_name=<pre>, create_slot=false)`.
  - Drive the same four DML statements on PG:
    `INSERT(1,'hello')`, `INSERT(2,'world')`,
    `UPDATE … SET v='updated' WHERE id=2`, `DELETE WHERE id=1`.
  - Verify subscriber state from fresh database/sql sessions (each
    `psc.WaitForRow` opens a new connection, so the assertion exercises
    the same path that the rung-1 caveat surfaced):
    - `WHERE id = 2 AND v = 'updated'` returns 1 — exercises UPDATE apply
      + PK btree maintenance after UPDATE.
    - `WHERE id = 1` returns 0 — exercises DELETE apply + the
      orphan-index-entry tolerance path: the PK btree still has an entry
      pointing at slot N after DELETE, IndexScan re-fetches the tuple and
      MVCC visibility marks it dead (xmax set by `applyDeleteByKey`).
    - `count(*)` returns 1 — exercises SeqScan + visibility against the
      final state.

If any of those assertions fail, the failure is a genuine rung-2 gap and
demands a follow-up rung with its own design doc and pin (the same protocol
M0103-0008 followed across rungs 1–17).

## Alternatives considered

  - **Extend the existing `TestPort_PgoutputInteropPGToGoopg`**. Rejected:
    that test is a byte-level pgoutput-decode test (consumes
    `pg_logical_slot_get_binary_changes` output via `wal.DecodeMessage`).
    It explicitly does *not* run a goopg subscriber. Mixing live-apply
    assertions into a decode-only test conflates two distinct verification
    paths.

  - **Add the case to `TestPubSubClusterSmokePGToGoopgFreshSessionVisibility`
    in `internal/testutil/pubsubcluster/cluster_test.go`**. Rejected: that
    file is the harness's own smoke suite. The Scenario-A closure test
    belongs in `internal/testport/` next to its symmetric Scenario-B
    counterpart, where the docs/test-port CSV ports rows will eventually
    land.

  - **Wire pgbench + kill -9 immediately**. Rejected: rung-2 is the
    minimum increment that surfaces the rung-1 deferred follow-ups under
    a real subscribed stream. pgbench + kill -9 + libpq multi-host
    reconnect is at least three more rungs on top, each likely surfacing
    its own gap. Land the DML round-trip first, then layer failover.

## Consequences

  - If `TestPort_PgoutputInteropPGToGoopgFullDML` passes on first lift,
    the rung-1 caveat ("IndexScan tolerates orphaned entries") is
    confirmed against a real apply stream, and rung 3 can move directly
    to failover infrastructure.

  - If it fails, the failure pinpoints which DML path needs explicit
    index maintenance, and the next rung lands the targeted fix (e.g.,
    `applyDeleteByKey` removing the matching btree entry, or
    `applyUpdateByKey` removing the old key when key columns change).

  - The new test depends on the same `postgres/local_install/bin`
    binaries the existing `TestPort_PgoutputInteropGoopgToPG` skips on
    when absent. Skips with the same `pgcluster.Available` gate so a
    fresh checkout without `make local-install` doesn't fail.

## Pin

`TestPort_PgoutputInteropPGToGoopgFullDML` in
`internal/testport/pgoutput_interop_test.go`.
