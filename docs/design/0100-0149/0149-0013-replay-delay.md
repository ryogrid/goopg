# 0149-0013 — Port recovery/t/005\_replay\_delay.pl

Status: done.

Upstream: `postgres/src/test/recovery/t/005_replay_delay.pl`; xlogrecovery.c `recoveryApplyDelay`,
`recoveryPausesHere`, `pg_wal_replay_pause` / `pg_wal_replay_resume` / `pg_get_wal_replay_pause_state`.

## What the test checks

1. A standby with `recovery_min_apply_delay = '3s'` replays a primary commit no sooner than 3 s after it.
2. On a second standby, `pg_wal_replay_pause()` moves the state to "pause requested" and then "paused"; no new
   rows replay while paused; `pg_wal_replay_resume()` lets them through.
3. Promotion of a paused standby completes; afterwards pause/resume fail with "standby promotion is ongoing".

## Harness

`TestPort_Recovery005ReplayDelay` in `internal/testport/recovery_005_replay_delay_test.go` runs three pairings:
all\-goopg, PG 18.3 primary → goopg standby, goopg primary → PG 18.3 standby.

## Fixes

- **`recovery_min_apply_delay` was not a registered GUC**, so a goopg standby rejected its postgresql.conf. It is
  now registered as PG has it \(integer, ms, 0..INT\_MAX, sighup\), with a postgresql.conf.sample line.
- **Apply delay.** Before applying a COMMIT or COMMIT PREPARED record the stream replayer reads the record's
  xact\_time \(µs since 2000\-01\-01\) and waits until xact\_time + delay. Like PG, it re-reads the delay while
  waiting, and a promotion trigger ends the wait.
- **Recovery pause.** The replayer keeps PG's three states \(not paused, pause requested, paused\).
  `recoveryPausesHere` runs before and after the apply delay, turns a request into "paused", and waits for resume
  or a promotion trigger. The SQL functions go through a postmaster hook into the standby controller. They raise
  55000 "recovery is not in progress" on a primary and "standby promotion is ongoing" once promotion has started,
  with PG's hints.

## Not covered

- Pause state does not survive a standby restart, and `recovery_target_action = pause` is not wired to it.
- PG applies the delay only when ArchiveRecoveryRequested and checks the consistency point first; goopg applies it
  to every streamed commit.
- Function names are matched lower\-case only, as the planner passes them.
