# M-NIGHTLY AI-20261020-013101-003/-004 — a timed-out cluster start says where the server is stuck

Status: diagnostic landed 2026-10-21; root cause pending the next occurrence.

## 1. Failure

The 2026-10-20 nightly testport lane failed two isolation specs before their
first step. Both failed on `cluster.Start`:

- `TestPort_IsolationInsertConflictDoUpdate3` at 01:45;
- `TestPort_IsolationClusterConflict` at 01:53.

Both reported `start timeout after 20s`, with a cluster.log ending at the same
line:

```
msg="GC tuning applied" GOGC=200
msg="GOMEMLIMIT applied" bytes=5368709120
msg="loaded postgresql.conf" … entries=1
```

The next line on a healthy start is `aio engine attached`, which is logged
right after `initdb.Open` returns (cmd/goopg/main.go). Both servers therefore
spent at least 20 s inside `initdb.Open`. The specs around them started in
seconds.

## 2. What was measured

| run | loaded conf → aio engine attached |
|---|---|
| first start after initdb | ~1.1 s |
| warm restart, 3 runs | 105–110 ms |
| 24 concurrent cold starts of copies of one cluster, under the cgroup cap | every server attached by 1.76 s |
| the two specs alone (`go test -run`) | PASS, 2.3 s each |

- A CPU profile of a cold start shows about 110 ms of CPU: xlog.ReadAll,
  CreatePerDatabaseScaffolding and catalog decode.
- `initdb.Open` has no sleep, cross-process lock or retry loop that could
  account for 20 s. Its only `/tmp` use is the control socket fallback.

The stall does not reproduce on demand. The cluster log, the only evidence,
lives in a `t.TempDir()` inside a nightly worktree that is deleted with the
run.

## 3. The open hypothesis

The testport lane runs in one systemd unit with `memory.high=6G`
(ci/batch, `unit=goopg-nightly-testport high=6G max=8G`). Every server it
starts applies `GOMEMLIMIT=5GiB`. A unit held at `memory.high` throttles
every allocation of every process in it. A starting server would then stall
anywhere in `initdb.Open` and show no CPU-bound frame (see the memory note
"cgroup high below GOMEMLIMIT = throttle trap").

The 2026-10-20 logs record neither the unit's memory state nor the server's
stacks, so this is untested.

## 4. Change

`cluster.Start` (internal/testutil/cluster) now adds two things to a start
timeout, after the existing log tail.

**The cgroup's memory state** (`cgroupMemoryNote`), read from the server's
own `/proc/<pid>/cgroup` path:

- `memory.current`, `memory.high` and `memory.max`;
- memory.events' `high` count: each crossing of `memory.high` throttles the
  group;
- memory.pressure, the PSI stall lines.

A rising high count with high `some`/`full` stall averages confirms §3. A
quiet group refutes it.

**The stacks of the stuck server** (`stuckStartStacks`):

- goopg leaves SIGQUIT to the Go runtime, which writes every goroutine's stack
  to stderr (cluster.log) and exits with status 2. The pid's process group is
  signalled, as `Kill` does, so a `go run` wrapper's child dumps too.
- After at most 5 s, the bytes appended to the log are filtered by
  `startupGoroutines`. It keeps goroutine 1, which runs `runStart` →
  `initdb.Open`, and every goroutine with an `internal/initdb` frame.
- The result is capped at 12 KiB.

Both are best-effort. Missing cgroup v2 drops the memory note. An absent dump
becomes a one-line note, never an error that replaces the timeout.

The successful path is unchanged.

## 5. Pins

- `TestStartTimeoutCarriesStuckGoroutines` uses the test binary itself
  (`TestMain`, `GOOPG_CLUSTER_FAKE_STUCK_START`) as a goopg whose start
  sleeps in `fakeStuckOpen`. A 1 s start timeout must inline:
  - the log tail;
  - the cgroup note, under cgroup v2;
  - goroutine 1's stack, naming `cluster.fakeStuckOpen`.

  Sending signal 0 instead of SIGQUIT fails it.
- `TestStartupGoroutinesSelectsMainAndInitdb` checks the filter on a synthetic
  dump: it keeps goroutine 1 and an `initdb.Open.func71` goroutine, and drops
  goroutine 0, the SIGQUIT header and an `http.Serve` goroutine.

Go 1.26 writes the block header as `goroutine 1 gp=… m=nil [sleep]:`, so the
filter matches `goroutine 1 ` and not `goroutine 1 [`.

## 6. Next

The next nightly that times out a start carries both notes:

- memory.high crossings and PSI stalls → the nightly lanes' memory budget
  (ci/batch unit sizes against the servers' GOMEMLIMIT) is the fix;
- a quiet group plus an `initdb.Open` frame parked on a lock or channel → an
  engine startup wait to fix.

AI-005 (`TestPort_IsolationMultipleCic`) is a separate failure: steps timed out
in the runner's 5 s drain window, then `driver: bad connection`. It stays open
on its own.

## 7. The recurrence, and the fix (2026-10-23)

The 2026-10-23 nightly (run 20261023-005128, `c6a780f7e`) timed out a start in
`TestPort_ExtendedStatisticsSurvivesRestart` (AI-20261023-005128-001). The notes
gave a third answer, neither of the two §6 expected:

- the cgroup was quiet: memory.current 0.6 GB against memory.high 6 GB, PSI
  some/full avg10/60/300 all 0.00, so this was not a memory throttle;
- goroutine 1 was **runnable**, not parked, inside the catalog-heap reload
  (`initdb.reloadUserAggregatesFromHeap` → `scanCatalogHeapRows` →
  `DecodeRowIntoMctxPGTuple`), so this was not a stuck wait either.

The start was progressing, slowly. It fired at 01:01:56, while a Ralph loop's
S9 census (a clone copy plus 221 EXPLAINs) ran beside the nightly testport lane.
On an idle host the same test passes in 2.1–2.2 s (3 of 3), so CPU contention
stretched a ~1 s start past the test's 20 s budget. The earlier -003/-004
timeouts stalled in the same initdb phase, under the same nightly co-load.

**Fix.** PG's own start wait is `pg_ctl`'s 60 s `DEFAULT_WAIT`, which the
`PGCTLTIMEOUT` environment variable overrides (src/bin/pg_ctl/pg_ctl.c). PG's TAP
suites rely on that override when a host runs slow. The harness now honours it:

- `cluster.New` passes the start budget through `startupWaitFromEnv`, which
  raises a normal budget, at least the 20 s `defaultStartupWait`, to
  PGCTLTIMEOUT seconds when that is longer. It never lowers one, ignores a
  malformed or non-positive value, and leaves a deliberately short budget alone
  (the 1 s budget of `TestStartTimeoutCarriesStuckGoroutines`).
- The nightly testport stage (`ci/batch/stages/stage-testport.sh`) exports
  `PGCTLTIMEOUT=60`. Interactive and loop runs keep their budgets. A real
  `pg_ctl` sees its own default.

Pins: `TestStartupWaitFromEnv` (raise, never lower, malformed, short budget) and
`TestNewHonoursPGCTLTIMEOUT` (default and explicit 20 s budgets both reach 60 s).

The per-start catalog-heap reload, which decodes every catalog row on each
start, is what makes a start slow under contention. It remains a possible
start-time lever, but it is not a correctness issue.
