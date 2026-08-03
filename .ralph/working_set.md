(idle — nothing in flight)

M0127-P1.1 is CLOSED (loop #45, 2026-08-03). P0 + P1.1 done.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P1.2` (worker-path exercise): an
integration test of the P1.1 seam under `BuildWorker` (`inWorker=true`),
because fusion's decline-in-worker precedent says this path diverges
silently. Bar: RACE.**

Carry-over facts a next loop should not re-derive:

- **P1.1 shape:** `joinOp.bindProbe` binds the probe child's slot into
  `lazyVirtualOut.sources[lazyProbeSrcIdx]` on EVERY pull;
  `outerOnlyEmit` composes Semi/Anti through the new `lazyOuterOnlyOut`.
  `lazyRow` and `lazyKeyRow` are DELETED. Kill switch
  `joinSlotChainEnabled()` / `GOOPG_JOIN_SLOT_CHAIN=off`, read ONCE per
  `ensureLazyVirtual` — a mid-life `SetJoinSlotChainEnabled` does not
  apply to an already-opened joinOp (tests must set it before the
  fixture is built).
- **P1.2 note:** the RACE gate it names is RED at clean HEAD for the
  unrelated `buildEnvInFlight` global (`executor.go:35-41`, M0126-0006,
  filed under M-NIGHTLY ~L1148). Every race frame is `buildWithEnv`.
  Budget for separating new frames from that baseline.
- **`pgrep -f <pattern>` SELF-MATCHES an `until` wait loop** whose own
  command line contains the pattern — it never exits, and a finished
  gate looks like a running one (burned ~1 h this loop on the regress
  suite, which had actually passed in 659 s). Poll the LOG, not pgrep.
- **DS05 gate hazard (hit again, 2nd loop running):** after a goopg
  TIMEOUT the script restarts and the transient `goopg-tpcds-sf05.scope`
  may still be loaded → `systemd-run` fails → 180 s readiness timeout
  kills the sweep mid-run. Recovery that works:
  `QUERIES="$(seq <n> 99)" scripts/tpcds-sf05-regression.sh sweep`.
- **Q47/Q72 are the known DS05 boundary pair** (263-312 s vs the 300 s
  cap, flipping across runs at unchanged code). Not a regression.
- **`TestPort_RegressSuite` leaks a ppid=1 goopg server** whose data dir
  `t.TempDir` already removed, so `goopg stop -D` cannot reach it —
  reap by PID before any timed gate.
- **Do NOT `git stash`** in this tree (9 unrelated entries); diff against
  `git worktree add /tmp/... HEAD` instead.
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER
  modified. Tracking = `docs/design/0127-pg-shaped-join-search.md` §6 +
  fix_plan checkbox + README index status.

Gates run this loop: REGRESS full `TestPort_RegressSuite` PASS (659.4 s);
UNITS precommit PASS; SPOT PASS (Q12=2 / Q13=35, 17.8 s); DS05
MISMATCH=0/CKMISMATCH=0/ERROR=0 across all 99 (two halves, see above);
BENCH seam 0 B / 0 allocs; `go test ./internal/executor/ ./internal/planner/`
PASS; pgbench smoke via the commit hook; `make ralph-state-guard` OK.

In-flight: none.
