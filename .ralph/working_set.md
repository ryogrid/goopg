(idle — nothing in flight)

M0131-S28.0 LANDED (loop #153). `pgcluster` now has a *true* SIGKILL:
`Start()` sets `SysProcAttr{Setpgid: true}` and `KillHard()` does
`syscall.Kill(-pgid, SIGKILL)` + reap; `Stop()`'s 20 s escape hatch upgraded
from `Process.Kill()` to the same group kill; `Kill()`'s doc comment corrected
(it is `pg_ctl -m immediate`, i.e. SIGQUIT — the postmaster still reaches
`on_proc_exit(UnlinkLockFiles)` and removes `postmaster.pid`). Guard
`internal/testutil/pgcluster/kill_hard_test.go` is a paired probe: `killhard`
asserts the lock file survives, the pinned `pg_backend_pid()` dies with the
group, and PG replays its own WAL with all 500 committed rows;
`pg_ctl_immediate` asserts the lock file is removed, so an upstream change that
made `Kill()` sufficient fails loudly instead of leaving dead code.

Discovery (ledger row filed): goopg has NO stale-lock-file check on start —
`internal/server/server.go:677` calls `control.WritePIDFile` unconditionally,
where upstream `CreateLockFile` probes the recorded PID + shmem key and refuses
("Is another postmaster running?"). Two goopg servers on one dir silently
coexist. Deferred: a start-refusal breaks every harness that restarts over a
crashed directory until each learns stale-vs-live.

Gates: `go test ./internal/testutil/...` (all ok, incl. the new test),
units precommit (all green/cached), pgbench smoke via the commit hook.
Design `docs/design/0131-0017` updated with the S28.0 LANDED section.

Nightly triage: `ci/logs/action-items.md` run `20260812-005501`'s 4 items were
all already filed under M-NIGHTLY (2 new + 2 recurrences); parked per banner.

Next loop: banner = M-NIGHTLY (filing only) then M0131. Continue **M0131-S28**
proper — write `internal/testport/e2e_goopg_crashstart_on_pgdata_test.go`:
`pgcluster.New`/`Start` → the S21/S22 opcode workload (COPY, VACUUM, FOR
UPDATE, TRUNCATE, SAVEPOINT, index-heavy INSERT) → capture answers →
`KillHard()` → `cluster.New(… DataDir: pgDir …)` + `Start()` WITHOUT `Init()`
(idiom: `e2e_goopg_coldstart_on_pgdata_test.go:165-182`) → compare. Then the
GIN-refusal variant and the `..._concurrent` S24 re-arm variant carrying
`t.Skip("re-arm trigger for M0131-S24")`. Design `docs/design/0131-0017`.

In-flight: none.
