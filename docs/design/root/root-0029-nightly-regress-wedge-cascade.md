# root-0029 — nightly regress "wedge cascade": one hung case, 19 phantom regressions

Status: IMPLEMENTED (harness + summarizer), root cause of the wedge itself
still open (see §5).
Scope: `internal/testport/regress_suite_test.go`,
`internal/testport/framework/regress.go`, `ci/batch/lib/summarize.py`.
Trigger: nightly run `20260725-011243` filed 26 action items, 19 of which were
`regress/<case> (baseline pass) diverged: output mismatch; normalization rules
need extension`.

## 1. What actually happened

`TestPort_RegressSuite` runs all ~232 pg_regress cases sequentially against a
single shared goopg cluster. Each case gets a 120 s budget. In run
`20260725-011243`, 36 cases consumed exactly `120.0Xs` and every one of them was
reported as an output mismatch. They were not mismatches: **psql was killed by
the timeout, and the truncated transcript it had produced so far was then diffed
against the full expected `.out` file.** A diff against a truncated file always
differs, so the harness confidently blamed the normalization rules.

Ten of those 36 cases (`select`, `select_distinct`, `tid`, `time`, `truncate`,
`union`, `varchar`, …) sit in `docs/test-port/regress-diff-baseline.csv` with
`status=pass`, so `ci/batch/lib/summarize.py` filed one `[regression]` action
item per case. Together with 9 genuine sub-120 s divergences that is the 19.
A Ralph loop then has to re-run and dismiss each one individually.

Two properties made this self-sustaining:

- **The wedge outlives the case.** Killing psql kills only the client; the goopg
  backend keeps executing (the standing benchmark-hygiene trap, memory
  `benchmark_orphan_and_ab_confounds`). Whatever the orphan holds — row locks,
  a catalog lock, or just heap and GC pressure — is still held when the next
  case connects. The failures therefore arrive in unbroken runs: 21 consecutive
  cases from `tid` onward in this run.
- **The existing recovery hook could not see it.** The suite restarts the
  cluster when `exec.isAlive()` fails, but `isAlive()` is a `SELECT 1` with its
  own 5 s budget. A server that is merely *saturated* (or blocked on locks an
  orphan holds) answers `SELECT 1` instantly while every real case times out.
  `server not responding` appears **0** times in the whole nightly log.

## 2. Fix — report layer

`framework.ErrExecTimeout` + `framework.RationaleExecTimeout` ("execution
timeout"). `ClusterRegressExecutor.ExecuteSQL` now honours the caller's context
deadline (it previously ignored the `context.Context` argument entirely and used
its own hard-coded 120 s) and, when `util.CommandResult.TimedOut` is set,
returns the partial output *with* a wrapped `ErrExecTimeout`.
`RunRegressSubset` short-circuits on that error before the diff, so the case
reports

```
deferred: execution timeout: psql killed after 2m0s (cluster wedged or
overloaded; output is truncated, not a diff)
```

Nothing about the normalization rules is claimed, because nothing about them was
observed.

## 3. Fix — cascade layer

The suite carries a `clusterPoisoned` flag. A case that times out sets it; the
next case restarts the cluster (`Kill` → `Start` → `runRegressSetup`) instead of
inheriting the orphan. This reuses the crash-recovery path that already existed
for the GC-crash case; only the trigger is new. Subtests run sequentially (no
`t.Parallel`), so a plain `bool` is sufficient.

Cost: one cluster restart per timeout. That is strictly cheaper than the 120 s
each subsequent case was burning.

## 4. Fix — nightly classifier

`summarize.py` no longer treats a timeout as a divergence. All timeout cases
collapse into a single `regress/suite-wedge` regression item that reports the
count, how many were baseline-pass, the longest unbroken run and its first case,
and points the repro at that case. Policy-excluded cases are dropped from the
contiguity scan (they never touch the cluster, so they must not break a run).
Genuine sub-timeout divergences are still filed per case, unchanged.

Replayed against the real `20260725-011243` log with the timeout rationale
substituted in: **26 action items → 17**, the 10 timeout-driven phantoms
replaced by one wedge item pointing at `tid`. Replayed against the log
unmodified (old rationale, i.e. a nightly predating the harness fix): 26 items,
0 wedge items — the classifier change is inert on old logs.

## 5. What is NOT fixed

The wedge itself. Two candidate mechanisms, neither confirmed:

1. **Orphaned backend.** goopg keeps executing after its client disconnects, so
   a case killed at 120 s can leave a backend holding locks. PG's
   `ProcessInterrupts`/`ClientLostCheck` path (`postgres/src/backend/tcop/postgres.c`)
   aborts a backend whose client has gone; goopg has no equivalent
   client-disconnect detection inside a long-running statement.
2. **Saturation / GC thrash.** The suite's own comment records that the server
   can die of GC memory pressure after heavy DDL; a server sitting at
   `GOMEMLIMIT` degrades every case while still answering `SELECT 1`.

`SET statement_timeout = '5s'` is issued per case and goopg *does* implement
statement_timeout (M0097-0059, `internal/server/dispatch.go`), so a single
statement should not be able to run for 120 s — which points at either many
statements each taking seconds (saturation) or a wait path that the deadline
does not cover. Resolving this needs an instrumented repro: run the suite from
`tid` with server-side `log_min_duration_statement=0` and dump `pg_stat_activity`
at the moment a case crosses 60 s. Ledger row filed 2026-07-28.

Until then the wedge is *observable* rather than silent, which is the point of
this change: a wedge now reports as one wedge.

## 6. Verification

- `TestRunRegressSubsetTimeoutIsNotOutputMismatch` (new) pins that a wrapped
  `ErrExecTimeout` never reaches the diff and never yields an "output mismatch"
  rationale.
- `TestRunRegressSubsetReportsStatuses` (existing) still passes — the
  port/defer/excluded classification is untouched.
- `summarize.py` replayed against the real nightly log, both with and without
  the new rationale (numbers in §4).
