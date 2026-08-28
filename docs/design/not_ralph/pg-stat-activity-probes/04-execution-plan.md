# 04 — Execution plan

Worktree: `.claude/waitevent-impl`, branch `waitevent-impl`
(base `977a487e0`). All server/benchmark runs go through the cgroup cap
(`scripts/goopg-test-run.sh`, unique `GOOPG_CG_UNIT`); `GOMEMLIMIT=15GiB` is
exported per session. Throwaway cluster on port 5533 (no bench clusters in a
fresh worktree).

## Phase A — design docs (this commit)

1. Write the bundle (README, 01-04, TODO).
2. Sub-agent review: verify every code claim against the tree, check PG
   citations against `./postgres/`, flag unclear policies. Incorporate.
3. Commit with `git commit -n` (hook bypass explicitly authorized for this
   docs-only change), push branch to origin.

Gate: review findings resolved; no code touched.

## Phase B — implementation

| step | files | content |
|---|---|---|
| B1 | `internal/utils/activity/registry.go` (+activity.go if needed) | add any missing interned event names; add registry nil-safe accessors used by Manager |
| B2 | `internal/access/transam/manager.go` | optional activity/procNum registration; defer-balanced `Lock:transactionid` window around WaitForXID's cond loop |
| B3 | `internal/executor/expr.go` | `Timeout:PgSleep` around evalPgSleep's select |
| B4 | `internal/executor/advisory.go` | wire `Lock:advisory` iff a real park exists (record finding otherwise) |
| B5 | tests | AllocsPerRun zero-alloc test; transam unit test forcing an XID block and asserting Snapshot shows Lock/transactionid; executor test for PgSleep; advisory test or documented skip |

Gates: `go build ./...`; `go test ./internal/utils/activity/...
./internal/access/transam/... ./internal/executor/ -run 'Activity|WaitForXID|PgSleep|Advisory'`;
full `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` from the
MAIN tree toolchain is not available here — run `go test ./...` scoped to the
packages above plus `go vet ./...`. Commit(s) with `-n` per user instruction,
push.

Gate compensation (required because B3/B4 touch `internal/executor`, which
normally triggers `scripts/tpch-spotcheck.sh` + the TPC-DS SF0.5 gate, and
because every commit here bypasses the hook's pgbench smoke): after merging
this branch into the main tree, run
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` and
`scripts/tpch-spotcheck.sh` from the main tree as a follow-up gate.

## Phase C — live verification harness

1. Init throwaway cluster:
   `GOOPG_CG_UNIT=psa-probes scripts/goopg-test-run.sh ./bin/goopg init -D tmp/psa/data`
   then start on 127.0.0.1:5533; create pgbench tables with
   `postgres/local_install/bin/pgbench -i -s 10`.
2. New script `scripts/pgbench-wait-sample.sh`:
   - args: host/port/db, clients (-c 50), jobs (-j 8), duration (-T 60),
     scale (-s 10), sample interval (default 500 ms);
   - starts `pgbench -N` in background;
   - sampling loop: `psql -c "SELECT pid, state, wait_event_type, wait_event
     FROM pg_stat_activity WHERE application_name = 'pgbench'"` → append CSV
     rows stamped with sample number;
   - concurrently fetches a CPU profile (`/debug/pprof/profile?seconds=<T>`,
     endpoint 127.0.0.1:6060);
   - after pgbench exits: aggregate counts by
     `(state, wait_event_type, wait_event)` and print the distribution table.
3. Expected distribution (documented pass criteria):
   - idle backends dominate samples with ClientRead;
   - active samples mostly have NULL wait event (on-CPU);
   - IO/Lock rows present but rare at `-s 10`;
   - optional contention mode (`--scale 1`) makes `Lock:transactionid`
    visible, proving G1 wiring end-to-end.
4. pprof correlation: `go tool pprof -top` on the captured profile; confirm
   hot leaves are client-read/executor work and that probe functions
   (`WaitEventStart/End`, `packWaitStrings`) are absent or <0.1% of samples,
   demonstrating recording overhead is invisible.

## Phase D — wrap-up

Update TODO.md checkboxes, final commit(s), push, report summary with the
observed wait-event distribution and pprof evidence.
