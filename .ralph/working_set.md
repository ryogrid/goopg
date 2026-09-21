# Working set — inter-loop baton

Task: **M-NIGHTLY AI-20260921-000212-001** (`TestCounter_PerShardWriteDistribution`)
— **RESOLVED `[x]`**. Scheduling-sensitive flake; the skip guard was testing
the wrong thing.

## Banner — item 9 is exhausted, we are in item 10

M0143-0007b closed last loop and **no open `[ ]` M0143 tasks remain**, so item
9 is done. Item 10 is "M-NIGHTLY open items, then the pre-existing milestones
(M0119 → M0122 → M0131 → M0134 → M0135/M0136 → M0095/M0110)".

**Next selectable: the next open M-NIGHTLY item**, in document order under
`### Nightly run 20260921-000212` (fix_plan ~line 1355):

1. ~~AI-…-001 units/activity/stats~~ ← done this loop
2. **AI-…-002/-003 — `TestPort_Isolation*` output diffs** ← NEXT
   EvalPlanQual: expected `1|newTableAValue|…` got `1|tableAValue|…` (a real
   value-content diff, and a RE-regression — it was closed in Loop #23 on a
   different signature); ReceiptReport: "expected 4215 lines, got 4216".
3. AI-…-004…-007 — `TestPort_PgAmcheck003*` re-CREATE EXTENSION after restart
4. AI-…-008…-017 — `TestPort_PgoutputInterop*` publisher/subscriber start

## Two escalations STILL unanswered

1. **M0145-0018 NO-GO** (loop 51) — blocker is the COST MODEL.
2. **The loop-48 ordering question** — M0145-0003 is strictly the first `[ ]`
   in item 3. Still the banner's call.

## What this loop established

The failure does not reproduce: 20 runs each at GOMAXPROCS 2/3/4/16, plus
`taskset -c 0` with GOMAXPROCS 16 — all pass.

**The guard tested `runtime.GOMAXPROCS(0) >= 2`, which is the configured P
COUNT, not the parallelism actually received.** Under a CPU quota a 16-P
process can run every goroutine on one P: the skip does not fire and the
assertion fails on a healthy `Counter`. The nightly runs the suite under
exactly that pressure.

The property was also stated too strongly — "more than one shard always
accumulates" is a claim about the Go scheduler. The test now OBSERVES the
parallelism it got and asserts only when ≥2 distinct Ps were seen.

**Both branches verified, not assumed**: pinned to one CPU it passes; with
`shardFor` deliberately collapsed to `shards[0]` it FAILS with "workload ran on
16 distinct Ps but only 1 shard received Adds" — so it still catches the
regression it exists for. Probe reverted immediately.

## Traps carried forward

- A skip guard that reads a *setting* rather than an *observation* produces
  false failures under CPU pressure. This is likely the shape of other
  nightly-only flakes.
- When fixing a flaky test, prove BOTH branches: that it skips/passes in the
  benign case AND still fails on a deliberately injected regression.
- A private PG oracle is cheap (`initdb` into /tmp on a 55xx port).
- Port 5560 is held by a PEER's server; do not touch it.

## Gates run

units PASS. Test-only change, so the corpus gates were not re-run.

## In-flight

none
