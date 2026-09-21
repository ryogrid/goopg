(idle — nothing in flight)

# Loop #66 result — PgoutputInterop x10: made SELF-DIAGNOSING, task stays open

Banner: items 0-9 unchanged. Last open item-10 group was the ten pgoutput
interop cases (AI-20260922-004850-006..-015).

## It does NOT reproduce at HEAD — tried three ways
- named single-case repro: PASS (2.7s)
- all ten together: PASS (62s)
- **full `./internal/testport/` package** (what the nightly actually runs —
  a subset run does not recreate that condition): PASS except the
  already-known `partition_aggregate`.
Local reproduction has a demonstrated ZERO hit rate. Do not keep re-running.

## What the nightly log DOES establish (read from ci/logs/, not guessed)
- Cases run SEQUENTIALLY -> a concurrent port race between these ten is NOT
  the mechanism.
- Failures INTERLEAVE with passes (UnchangedToast, MultiDMLXact,
  SavepointXact, MultiTable, ReplicaIdentityUsingIndex, KillAndReconnect,
  PgbenchKillAsync all passed in the same run) -> not a monotonic
  degradation after some point in the suite.
- Failures are consistently FASTER (2.60-2.74s) than passes (2.78-2.94s) ->
  consistent with dying at startup.

## Why neither night's cause is recoverable — and what landed
The error named a `cluster.log` under `tmp/nightly-src-<run>/`, the
nightly's THROWAWAY worktree, deleted when the run finishes. Both nights'
logs are gone; the only evidence was behind a dead path.
`cluster.Start` now INLINES the last 4 KiB of cluster.log into both
start-failure errors, so the next occurrence carries its own cause in
`go-test.log`. Best-effort: an unreadable log degrades to a note and never
replaces the start failure being reported.

## Next step (explicitly NOT more local re-running)
Wait for the next nightly and read the inlined tail. It will distinguish
the hypotheses still open: ephemeral-port collision (both `freeTCPPort` and
`cluster.freePort` use the racy bind-0/close/rebind pattern), a leftover
datadir/socket in a fixed `tmp/` path, or host resource exhaustion in a
~19-min stage. If the next nightly is GREEN, record that as evidence before
closing.

## Gates (all green)
units; full testport package (the reproduction attempt doubled as the
gate); tpch-spotcheck Q12=2/Q13=33; tpcds-sf025 `PLAN-SHAPE same=99
changed=0`; acceptance arm 24/24; pgbench smoke via hook. Stamps WERE
required — `internal/testutil/cluster/cluster.go` is non-test code under
`internal/`. New test verified non-vacuous: reverting to bare-path fails
all three subtests.

## Next loop
Item 10 has no other open task. Next in banner order is the "Manually
discovered" group, first: **setop output type is the FIRST member's, not
`select_common_type`'s** (found by an M0145-0004 discovery probe).

## Owner escalations OPEN — three
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted (blocks all
of item 3). 3. partition_aggregate's inventory row marks a never-passing
case must-pass.
