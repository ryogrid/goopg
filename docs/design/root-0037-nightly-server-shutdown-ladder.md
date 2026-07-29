# root-0037 — The nightly held a 16-core host for 6h45m after its sweep had finished

status: accepted
date: 2026-07-29
area: ci/batch (nightly stage lifecycle)
related: [root-0029](root-0029-nightly-regress-wedge-cascade.md) (same orphaned-backend
family, seen from the regress suite), `ci/design/05-tpch-stage.md`,
`ci/design/03-resources-and-parallelism.md`

## What happened

Nightly run `20260729-002344` fired at 00:23. Its TPC-H stage finished its sweep at
**02:07:15** and immediately ran its `cleanup` EXIT trap. At **08:50** — 6h45m later —
`stage-tpch.sh` and its goopg server were still alive, the run had never reached its
TPC-DS or summary stages, and the host was carrying an idle 7.5 GiB resident server.

The stage's own log ends where you would expect success:

```
02:07:15 [S2] tpch: sweep done in 3857s (baseline 1469s)
```

and the server's log ends one line into a *successful* shutdown:

```
02:07:15 control: stop requested
02:07:15 shutdown checkpoint start
02:07:16 checkpoint complete type=requested lsn=2225547416 elapsed_ms=179
02:07:16 shutdown checkpoint complete      <-- last line ever written
```

Nothing looks broken in either log, which is precisely why it burned a night: the
failure has no error message, only an absence.

## Root cause: one leaked backend, then an untimed `wait`

Two of the sweep's queries hit the 1200 s per-query cap. The harness kills the psql
client, which — per this project's standing hazard — kills only the client; the goopg
backend keeps executing. Q21's backend recovered (closed 9 s after its cancel request).
Q13's did not:

```
01:38:55 [S2] tpch q21 TIMEOUT after 1209s (per-query cap 1200s)
01:59:55 [S2] tpch q13 TIMEOUT after 1260s (per-query cap 1200s)
01:59:55 client connection lost mid-query; cancelling statement  remote=…:58452 pid=40
```

Backend `pid=40` never logged `connection closed`. Differencing the log's
`connection established` pids against its `connection closed` pids yields **exactly one**
survivor — `40`, the Q13 backend. `startClientEOFWatch`
(`internal/server/eof_watch.go:113`) detected the dead peer and called `cancel()`, and
the backend did not finish.

From there the wedge is mechanical:

1. `cleanup` runs `goopg stop`. `Server.startControlPlane`'s `cl.OnStop`
   (`internal/server/server.go:602`) takes the shutdown checkpoint — which **succeeds**,
   hence the last log line — then calls `runCancel()`.
2. The accept loop drains; the process stops listening on 65434 (confirmed: the port was
   gone from `ss` while the process still lived). But the process does not exit, because
   graceful shutdown does not return until every backend has finished, and `pid=40` never
   will.
3. `stage-tpch.sh:121` then did an **untimed** `wait "${server_pid}"`. That is the
   `do_wait` both the stage and `run-nightly.sh` were parked in when this was found.

So a single leaked backend escalated into: the run never finishing, the TPC-DS stage
never running, ~7.5 GiB and a run-lock held indefinitely, and — because the host-quiet
guards added on 2026-07-29 refuse to start while `ci/batch/stages/` is alive — the *next*
day's M0124 measurements blocked as well. Two of that day's M0124 readings had already
been voided by co-load from this same run.

The server was idle throughout (0.4 % CPU, all 23 threads sleeping), so the leaked
backend is **blocked, not spinning**. Where it is blocked is not established here — see
"What is deliberately not fixed".

## The fix: a bounded escalation ladder, and evidence capture

`stop_goopg_server` in `ci/batch/lib/common.sh` replaces the untimed wait in both
`stage-tpch.sh` and `stage-tpcds.sh` (which carried the identical two lines;
`stage-pgbench.sh` already hard-killed and needed no change). It sets `STOP_RUNG` to the
rung that worked:

| rung | action | budget |
|---|---|---|
| `already-exited` | none — pid gone | — |
| `graceful` | `goopg stop` (checkpoint, `DB_SHUTDOWNED`) | 120 s |
| `immediate` | `goopg stop -mode immediate` (no checkpoint, WAL replay next start) | 60 s |
| `sigterm` | `kill -TERM` | 30 s |
| `sigkill` | `kill -KILL` | — |

Worst case 210 s instead of unbounded. Each stage's data dir is a disposable clone that
is `rm -rf`'d on the next line, so nothing of value rides on the final checkpoint.

Two details that are easy to get wrong and are load-bearing here:

- **Liveness is tested by process *state*, not `kill -0`.** The server is a background
  child of the stage shell; between exit and reap it is a zombie, and a zombie still
  answers `kill -0` happily. `proc_alive` reads `ps -o stat=` and treats `Z*` as gone.
  With `kill -0` the graceful rung would have falsely timed out on every healthy run.
- **`STOP_RUNG` is a global, not an echoed value.** Calling the helper as `$(...)` would
  run it in a subshell, where `wait` cannot reap the *caller's* background child.

**Evidence capture.** The moment the graceful rung misses its budget is the last moment
the wedged server can be inspected — every rung below it destroys the evidence. The
helper now pulls `/debug/pprof/goroutine?debug=2` into the stage's log dir first. This
loop could not do that for the live process: goopg's pprof endpoint is unconditional but
binds `127.0.0.1:6060`, and losing that race to another instance is logged only at
`Debug` (`cmd/goopg/main.go:343`) — the nightly server's log has no `pprof endpoint`
line at all, so it had silently skipped. Both stages now pass a private
`GOOPG_PPROF_ADDR` (6160 TPC-H, 6161 TPC-DS, overridable). The next occurrence therefore
leaves `<stage>/server-goroutines.txt` behind automatically, and the leaked backend's
stack costs one file read instead of a 20-minute query to reproduce.

An escalation past `graceful` is reported via `progress` into the run log, so this failure
mode can never again present as silence.

## Verification

`ci/batch/lib/test_stop_ladder.sh` — standalone (`bash ci/batch/lib/test_stop_ladder.sh`,
~50 s, no goopg build: the "server" is a `sleep`, the "goopg binary" is a stub). It
asserts every rung fires *and* stays inside its budget:

```
ok[graceful]: rung=graceful in 0s
ok[immediate]: rung=immediate in 3s
ok[sigterm]: rung=sigterm in 6s
ok[sigkill]: rung=sigkill in 40s
ok[already-exited]: rung=already-exited in 0s
ok[dump-capture]: goroutine dump saved before escalation
PASS: all stop-ladder rungs bounded and correct
```

The `sigkill` case (a child with `trap "" TERM`) is the one that actually encodes the
guarantee: bounded exit no matter how wedged the server is. `already-exited` is the
zombie regression guard.

## What is deliberately not fixed

**The engine leak itself.** A backend that ignores statement cancellation after its
client disappears, and thereby blocks graceful shutdown indefinitely, is a real
divergence from PostgreSQL: upstream's postmaster reaps backends and a
`pg_ctl stop -m fast` sends `SIGTERM` to each, which `ProcessInterrupts` honours at the
next `CHECK_FOR_INTERRUPTS()`. goopg's graceful path has no equivalent deadline or
force-terminate step, so one unresponsive backend is enough to hang the process forever.
This change bounds the *harness* cost; it does not make the server exit.

That is filed as an open M-NIGHTLY task and a deferral-ledger row (2026-07-29). It is the
same family as root-0029's still-open "orphaned backend vs GOMEMLIMIT saturation" row —
this is a second, sharper instance of it, with the survivor identified down to a single
backend pid. The concrete resume point is the auto-captured goroutine dump the next
occurrence will leave behind, or a deliberate reproduction (run Q13 at SF=1 under a
1200 s cap and kill the client).
