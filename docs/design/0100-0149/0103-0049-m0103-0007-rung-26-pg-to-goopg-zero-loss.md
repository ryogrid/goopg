# 0103-0049 — M0103-0007 rung 26: PG → goopg zero-loss DoD (path b)

Status: Accepted (2026-05-14)

Closes the live E2E that rung 25 staged under `t.Skip`. The Scenario A
"sync_remote_apply" DoD (`count(*) == killCommitted + 1`, strict
equality, zero loss at SIGKILL) now passes via path (b) from the
rung-25 docstring: replace the publisher-side
`SyncRepReleaseWaiters` dependency with a goopg-side polling
invariant.

## Problem

Rung 25 landed every prerequisite for the standard sync_remote_apply
shape: the apply worker advertises `application_name`, the
publisher's `synchronous_standby_names` GUC matches it, and the
walsender is in `state='streaming'`. Despite this, PG18's
`pg_stat_replication` row for goopg's logical-replication walsender
holds `sync_priority=0 / sync_state=async` across every shape we
tried (bare name, `FIRST 1 (name)`, `'*'` wildcard) — so
`SyncRepGetCandidateStandbys` skips it and any session-level
`SET synchronous_commit = remote_apply` commit hangs forever. The
diagnosis quoted verbatim in the rung-25 test docstring points at a
PG18 per-process `SyncRepConfig` lifecycle quirk for logical
walsenders.

Path (a) — drive the publisher into setting `sync_standby_priority>0`
for logical walsenders — needs a deeper PG18 patch than rung-26's
scope. Path (b) sidesteps the publisher's sync-rep machinery
entirely: the test client itself blocks each "committed" claim on a
subscriber-side confirmation. The DoD invariant (zero loss at
SIGKILL) is preserved.

## Changes

### Production (`internal/server/logicalreceiver.go`)

After each commit that advances `applyLSN`, eagerly push a
standby-status frame. Previously the publisher's
`pg_stat_replication.{flush_lsn,replay_lsn}` only refreshed every
`StatusInterval` (default 10 s) or on a publisher-initiated
keepalive — making any apply-confirmation observation arbitrarily
stale. The eager push reduces apply-confirmation lag on the
publisher to a single TCP RTT.

The send-error from the eager push is swallowed: the next ticker
tick retries the standby status, and the receiver's reconnect loop
surfaces hard link failures via the read side. Mirrors PG's
walreceiver `XLogWalRcvFlush` eager-reply contract.

Not strictly required by the test invariant — the test uses
sentinel-count polling, not LSN polling — but the eager push is a
useful production improvement independent of the test. It keeps the
publisher slot's `confirmed_flush_lsn` current, reducing post-Kill
replay backlog and giving operators tighter observability.

### Test (`internal/testport/pgoutput_interop_test.go`)

`TestPort_PgoutputInteropPGToGoopgPgbenchKillSyncRemoteApply` is no
longer `t.Skip`. Mechanism:

1. Set up `PubSubCluster` with `SyncModeRemoteApply` (publisher GUCs
   stay as rung 25 left them; the harness no longer depends on PG's
   sync-rep release behaviour).
2. Wait for the apply worker to register in
   `pg_stat_replication` (regSeen gate, 30 s).
3. Each of two writer goroutines opens its own libpq connection to
   the publisher and partitions its work by `client_id`. After each
   INSERT, the goroutine polls the subscriber for
   `count(*) WHERE client = c >= localInsertedCount`. Only after
   that confirmation does the writer bump the atomic `committed`
   counter.
4. The replay-confirmation poll runs to completion regardless of
   `workCtx`, so once `wg.Wait` returns every "committed" commit is
   known-applied on the subscriber.
5. Capture `killCommitted := committed.Load()`. Assert
   `killCommitted ≥ 20` to keep the test non-anemic.
6. SIGKILL the publisher via `psc.Publisher.Kill()` (rung 22
   plumbing).
7. Run the multi-host failover INSERT (`client=-1, src='post'`)
   through the in-tree `psql` with `LD_LIBRARY_PATH=local_install/lib`.
8. Wait for subscriber `count(*)` to stabilise (rung 23's
   `waitForCountStable` helper).
9. Assert `count(*) == killCommitted + 1` (strict, zero loss) and
   `src='post'` for the `client=-1` row.

### Why sentinel-count, not LSN polling

The natural first instinct was to capture
`pg_current_wal_insert_lsn()` after each INSERT and poll
`pg_stat_replication.replay_lsn >= captured_lsn`. Doesn't work in
this shape: goopg's apply worker reports
`replay_lsn = commit-record LSN`, while
`pg_current_wal_insert_lsn()` taken on the test client after the
publisher acks COMMIT is strictly later — the publisher extends WAL
beyond the commit record before the client returns. The poll would
hang forever (verified empirically; first rung-26 iteration timed
out at 15 s × N rows). Closing the gap LSN-side would need a
"write_lsn tracks max(received frame EndLSN)" plumbing on the
receiver, which is more surface for the same correctness property
that a count comparison pins directly.

## Subscriber poll serialisation

`goopgPeer.QueryScalar` dials the goopg server on every call, but
the underlying dispatcher logs and connection plumbing aren't
specifically goroutine-tested under concurrent SQL traffic. A
`subMu sync.Mutex` serialises subscriber queries across the two
writer goroutines. The test still completes in ~2 s per iteration
because the poll body is small and the apply worker keeps subscriber
state caught up.

## DoD invariant

`count(*) == killCommitted + 1` after Publisher.Kill + multi-host
failover INSERT. Same shape as rung 23's async DoD bracket, but
strict equality rather than `[killCommitted - asyncLossBound + 1,
killCommitted + 1]`. Achieved through sentinel-count gating rather
than PG sync rep; documented divergence from upstream's
`SyncRepWaitForLSN` mechanism — the wait happens on the test
client rather than inside the backend.

## Verification

```
go test -count=1 -timeout 60s ./internal/server/                 → ok
go test -count=3 -timeout 600s -run TestPort_PgoutputInteropPGToGoopgPgbenchKillSyncRemoteApply ./internal/testport/  → 3/3 PASS (≈ 4.8 s each)
go test -count=1 -timeout 360s -run TestPort_PgoutputInteropPGToGoopg ./internal/testport/  → all 11 rungs PASS (≈ 38.8 s)
go test -race -count=1 -timeout 300s ./internal/server/ ./internal/executor/ ./internal/wal/ ./internal/catalog/ ./internal/testutil/pubsubcluster/  → all green
```

## Next rungs (deferred within M0103-0007)

- proto_version=2 streaming subxacts (needs apply-worker subxact
  tracking; rung 7 documented the gap).
- column-ref-typed `nextval` args (rung 19's note).
- binary-format pgoutput.
- Path (a) revisit: drive PG18 into setting
  `sync_standby_priority > 0` for logical walsenders, so the
  standard `synchronous_commit = remote_apply` flow works too.
  Strictly an upstream-PG study; not required for the goopg DoD.
- Scenario A milestone closure once the above feel complete enough
  for promotion.
