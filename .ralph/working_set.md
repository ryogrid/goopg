(idle — nothing in flight)

Last loop: **root-0037 COMPLETE and committed** — the nightly's server-shutdown
ladder. Worked under the banner's *second carve-out*, not as ordinary M-NIGHTLY:
the wedge broke the bench clusters and blocked M0124-0002/-0004 from running.

## ⚠️ ACTION REQUIRED BY THE USER (I could not do this — classifier denies kills)

Nightly run `20260729-002344` is **STILL WEDGED**. Its sweep finished 02:07:15;
at 08:50 it still held ~7.5 GiB and the run lock. Until it is gone the host is
NOT quiet and **M0124-0002 / M0124-0004 must not be selected**. To clear it:

```
kill -TERM 2511542                      # run-nightly.sh — stops before stage-tpcds
kill -QUIT 2621153                      # goopg server — dumps goroutines to
                                        # ci/logs/20260729-002344/tpch/server.log
```
Do **QUIT, not KILL**, on 2621153: SIGQUIT is untrapped (main.go notifies only
INT/TERM) so it prints the leaked backend's stack — the one artifact the open
engine task needs. PIDs are from this loop; re-check with `pgrep -af ci/batch`.

## Facts the next loop should NOT re-derive

- **The wedge is diagnosed, don't re-diagnose it.** Q13 hit the 1200 s cap ->
  killed psql -> backend `pid=40` logged `client connection lost mid-query;
  cancelling statement` and never closed (the ONLY pid with `established` and no
  `closed`). Graceful shutdown waits on it forever; `stage-tpch.sh`'s untimed
  `wait` inherited the hang. Server was IDLE (0.4 % CPU) — blocked, not spinning.
- **Two open items now, not one**: the harness fix is `[x]`; the *engine* leak
  (no deadline / force-terminate in `cl.OnStop`, `server.go:602`) is a new
  unchecked M-NIGHTLY task + ledger row. It stays PARKED under the banner.
- **M0124's only open tasks are still -0002 and -0004**, both needing a quiet
  host. `M0124-0004` is value work so `FORCE=1` is legitimate there; `-0002` is a
  timed A/B and is not.
- Nightly triage: `ci/logs/action-items.md` unchanged since 2026-07-25; all 26
  items already filed as ID RANGES `-008..-026` — a per-ID grep FALSE-NEGATIVES,
  grep loosely (`grep 20260725 .ralph/fix_plan.md` -> 6 hits).
- Future wedges self-document: both stages now pass a private `GOOPG_PPROF_ADDR`
  (6160/6161) and auto-save `<stage>/server-goroutines.txt` before escalating.

Gates run: `ci/batch/lib/test_stop_ladder.sh` PASS (6 cases, all rungs bounded);
`bash -n` on all 4 shell files; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (all cached — correct, zero Go files
changed); pgbench smoke PASS via the commit hook; ledger render verified via
`gh api --method POST /markdown` (1 table, 7 cells, 1 body row);
`make ralph-state-guard` (auto-repaired a stale completed marker, then OK).
In-flight: none.
