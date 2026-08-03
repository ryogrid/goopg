(idle — nothing in flight)

M0127-P0.3 is CLOSED (loop #44, 2026-08-03). **P0 is now fully closed
(P0.1 + P0.2 + P0.3).**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P1.1` (legacy-path slot chaining, the
M0126-0004 deferral un-deferred): probe child slot as `lazyVirtualOut`
source, rebind on pointer change + copy fallback on type change, delete
`slotRow(probeSlot)` at operators_join_agg.go:~1254 and the vestigial
`lazyKeyRow`, env kill-switch `GOOPG_JOIN_SLOT_CHAIN=off`. Bar: full
REGRESS + DS05 + SPOT + seam microbench 0 allocs.**

Carry-over facts a next loop should not re-derive:

- **P0.3 shape:** `planner.Join.HashKeysAreInt64()` lives in the new
  `internal/planner/join_hashkey.go`; `buildLazyHashTable` sets
  `o.lazyHashIsInt` from it BEFORE the build loops. `lazyHashFinalize` and
  `lazyBuildAllInt64` are DELETED. `demoteIntHash` is the mid-build fallback.
  The CTID lane (`buildHashRightWithCTID`) forces `lazyHashIsInt = false`.
- **P1.1's probe site moved**: `slotRow(probeSlot)` is no longer at :1254 —
  the P0.2/P0.3 edits shifted line numbers; grep, don't trust the plan's
  numbers.
- **DS05 gate hazard (hit this loop):** after a goopg TIMEOUT the script
  restarts the server, and the systemd transient scope
  (`goopg-tpcds-sf05.scope`) can still be loaded → `systemd-run` fails,
  readiness times out at 180 s and the sweep dies mid-run. Recovery that
  worked: re-run the tail with `QUERIES="$(seq 73 99)"` (stamped SUBSET
  PROBE). Logs: `analysis/m0127-p03/ds05-sweep{,-73-99}.log`.
- **Q47/Q72 are the known DS05 boundary pair** — 263-308 s against the 300 s
  cap, flipping PASS/TIMEOUT across runs at unchanged code. Do not triage
  them as a regression from a join change.
- **RACE gate is RED at clean HEAD** — `buildEnvInFlight` (`executor.go:35-41`,
  M0126-0006) package global vs concurrent `BuildWorker`. Filed under
  M-NIGHTLY (fix_plan ~L1148). Every race frame is `buildWithEnv`.
- **Do NOT `git stash`** in this tree — 9 unrelated stash entries; compare
  against HEAD with `git worktree add /tmp/... HEAD` instead.
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified.
  Tracking = `docs/design/0127-pg-shaped-join-search.md` §6 + fix_plan
  checkbox + README index status.

Gates run this loop: UNITS precommit PASS; DS05 MISMATCH=0/CKMISMATCH=0/
ERROR=0 across all 99 (two halves, see above); `go test ./internal/executor/
./internal/planner/` PASS; pgbench smoke via the commit hook;
`make ralph-state-guard` OK.

In-flight: none.
