# M0137-0007 — give each TPC-H gate/measurement lane a private clone and a private port

Status: accepted (landed 2026-09-15)

## Context

AGENT.md §"Plan-parity harness" and `04-forward-plan.md` §2.3 item 6 both name
this task's job in one sentence: shared-resource contention on the goopg
TPC-H bench cluster (`bench/tpch/runtime_goopg/data`, port **:65433**) "is the
stated reason for most deferred gates" in the previous phase, and cite one
concrete instance — `scripts/tpch-spotcheck.sh` was deferred four rounds
running while being the only gate that caught the Q13 33-vs-34 wrong-rows
bug. "This is an infrastructure problem with an infrastructure fix."

Reading the scripts confirmed the problem is wider than the cited example.
**Four** loop-facing scripts independently `stop -D`/`start -D` the exact
same shared `PGDATA`/port:

| script | when it runs |
|---|---|
| `scripts/tpch-spotcheck.sh` | the mandatory gate every planner/executor commit pays |
| `scripts/tpch-relsize-arm.sh` | manual, per M0125-0003's four-arm measurement |
| `scripts/tpch-estimate-audit-arm.sh` | manual, per the `todo_all_gate_protocol` per-commit recipe |
| `scripts/tpch-acceptance-arm.sh` | manual, same recipe |

Any two of these running concurrently — or one of them running while an
M0137 baseline capture (`estimate-audit`/`capture-tpch.sh`, per
`m0137-0003-baseline-capture-procedure.md`) is mid-session against the live
cluster — fight over the shared server: whichever starts second stops the
first's server out from under it, corrupting whatever in-flight measurement
it held. `bench/tpch/env_goopg.sh`'s own comment already documents the
sibling half of this defect (a spotcheck build clobbering the nightly's
`GOOPG_BIN` mid-run) and its ad-hoc fix (build to a private path); this task
is the equivalent fix for the PGDATA/port half, generalised to all four
scripts.

**Prior art already exists and already works**:
`ci/batch/stages/stage-tpch.sh` solved exactly this problem for the nightly
CI batch lane — it never touches a server on the canonical dir/port, and
instead waits for :65433 to go quiet, takes a `cp -a` snapshot onto
`tmp/goopg-nightly-tpch-data`, and runs its whole multi-hour stage against
that clone on its own reserved port (`:65434`), documented in
`ci/design/03-resources-and-parallelism.md` §D and `ci/design/05-tpch-stage.md`.
This task generalises that exact pattern to the four loop-facing scripts.

## Decision

### A reusable library, not four copies of the same logic

New `scripts/lib/tpch-private-clone.sh`, sourced by all four scripts,
providing:

- `tpch_wait_port_free <host> <port> <timeout-s>` — poll `pg_isready` until
  the port answers nothing, or return 1 after the timeout. An independent
  re-implementation of `ci/batch/lib/common.sh`'s `wait_port_free` —
  deliberately not sourced from `ci/batch/lib/`, so a loop-facing gate never
  depends on the nightly-batch tree being present or working.
- `tpch_private_clone_snapshot <src-data> <dst-data> <host> <src-port> [wait-timeout-s]` —
  up to 3 attempts: wait for `<src-port>` to go quiet, `rm -rf`+`cp -a` the
  snapshot, verify the port is STILL quiet (a server could have started in
  the gap — copying while one writes risks a torn/inconsistent data dir),
  retry on interference. On success, strips a copied stale `postmaster.pid`.
  Never stops or starts anything on `<src-data>`/`<src-port>` — the wait step
  is passive observation only.

### Applied to all four scripts, each landing on its own port

| script | private port (env override) | private clone dir |
|---|---|---|
| `tpch-spotcheck.sh` | 5580 (`TPCH_SPOTCHECK_PORT`) | `tmp/goopg-spotcheck-tpch-data` |
| `tpch-relsize-arm.sh` | 5581 (`TPCH_RELSIZE_ARM_PORT`) | `tmp/goopg-relsize-arm-tpch-data` |
| `tpch-estimate-audit-arm.sh` | 5582 (`TPCH_AUDIT_ARM_PORT`) | `tmp/goopg-audit-arm-tpch-data` |
| `tpch-acceptance-arm.sh` | 5583 (`TPCH_ACCEPTANCE_ARM_PORT`) | `tmp/goopg-acceptance-arm-tpch-data` |

Distinct both from the shared `:6543x` block and from ci/batch's reserved
`65434`/`65435` nightly clone lanes (checked against every `PORT=` literal in
`scripts/*.sh`, `bench/*/*.sh`, `ci/batch/**/*.sh` and the `CLAUDE.md`/
`AGENT.md` port tables before picking the range — the closest neighbours are
`5533`/`5534` "throwaway experiment" ports and `5535` (`ralph-precommit-test.sh`'s
fixed port); `5580`-`5583` collide with none of them).

Each script's edit is mechanical and the same shape:

1. Capture the shared dir/port as `SRC_DATA`/`SRC_PORT` (read from
   `bench/tpch/runtime_goopg/data` / `65433` directly, or via
   `bench/tpch/env_goopg.sh` for `tpch-spotcheck.sh`, which already sourced
   it) — named "lane-external; NEVER stop/start a server on it" in a comment
   at the point of capture, mirroring `stage-tpch.sh`'s own header comment.
2. Reassign `PGDATA`/`PG_PORT` to the lane's private clone dir/port.
3. Replace "stop whatever is on the shared dir" with "stop any stale
   instance left on OUR OWN private clone by a previous crashed run" (same
   goopg-control-socket mechanism, just retargeted) followed by
   `tpch_private_clone_snapshot`.
4. Retarget the "is the shared cluster loaded" preflight check
   (`[[ -s "${PGDATA}/PG_VERSION" ]]`) at `SRC_DATA` (the private clone does
   not exist yet at that point).
5. Retarget the "refuse if something else is on our port" preflight check
   (originally guarding 65433 against a foreign server) at the lane's own
   private port — it is no longer a shared-cluster guard, just "did our own
   private port somehow end up already bound".

`tpch-spotcheck.sh` additionally defaults `GOOPG_BIN` to a private path
(`tmp/goopg-spotcheck-bin`, unless the caller explicitly set `GOOPG_BIN`
before invoking it) rather than inheriting `env_goopg.sh`'s shared
`tmp/goopg-bench-bin` default — closing the sibling defect
`env_goopg.sh`'s own comment already names (a spotcheck build clobbering the
nightly's binary mid-run) as the natural conclusion of the same "this lane
is private end-to-end" fix, not a new discovered scope. The three arm
scripts already defaulted `GOOPG_BIN` to their own private paths
(`tmp/goopg-relsize-bin`, `tmp/goopg-acceptance-bin`) and needed no change
there.

`tpch-relsize-arm.sh` restarts its server once PER QUERY (up to 22 times per
arm invocation) reusing the same data — the clone therefore happens exactly
**once**, up front, before the query loop; every restart after that touches
only the already-cloned private dir. The other three scripts start a server
once per invocation, so the clone and the (single) start are adjacent.

### What did not change

- `ci/batch/stages/stage-tpch.sh` itself — untouched. Its own clone-onto-65434
  pattern already worked correctly and needed no fix; it is the reference
  implementation this task generalised, not a defect site.
- The nightly-CI-batch-running refusal (`pgrep -f ci/batch/run-nightly.sh`)
  in all three arm scripts — untouched. That refusal protects TIMING
  measurement validity (a concurrent nightly stresses CPU for hours), which
  is orthogonal to which port/dir a script binds and stays valid regardless.
- TPC-DS captures (`scripts/capture-tpcds.sh` against `:65436`/`:65437`) —
  out of scope. They never stop/start a server (read-only `EXPLAIN` queries
  against an already-running cluster), so the acute hazard here (one lane
  killing another lane's server) does not apply to them the same way; `:65437`
  is already a dedicated, non-shared lane for the SF0.25 regression gate per
  `CLAUDE.md`'s port table. Not ledgered — this is a scope boundary
  (the concrete evidence this task acted on was the 65433/server-restart
  class), not a discovered PG-behaviour gap.
- `docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md`'s port
  table — unchanged; `estimate-audit -plan-only`/`capture-tpch.sh` baseline
  captures still target the shared `:65433` cluster directly, by design
  (M0137-0003 §2: "one connection, opened once… the result never depends on
  whether some other session already warmed the cluster" — the tool IS the
  measurement of that cluster's state, unlike the four gate/arm scripts
  above, which only need A copy of the data and gain nothing from being the
  canonical cluster itself).

Two `ci/design/` docs (`03-resources-and-parallelism.md` §D,
`05-tpch-stage.md` §A/§B) stated "canonical 65433 stays the loop's spotcheck
lane" as a load-bearing assumption at the time `ci/batch`'s isolation
strategy was designed. Both got a short "Update (M0137-0007)" note: the
assumption is no longer true (spotcheck no longer touches 65433 at all), but
neither doc's own conclusion changes — `stage-tpch.sh`'s snapshot-copy
strategy is unaffected either way, and remains correct on its own terms.

## Verification

Live-validated end-to-end against the real `:65433` bench cluster (2.0 GB,
loaded TPC-H SF=1 data), from a throwaway PostgreSQL environment (bootstrap
PATH per AGENT.md §0):

- **`tpch-spotcheck.sh`, normal run**: PASS (`Q12=2, Q13=34`), server on
  `127.0.0.1:5580`, wall clock 16.9 s including the 2 GB clone (well inside
  the ~1 min this gate has historically cost) — `:65433` never answered
  `pg_isready` before or after.
- **`tpch-spotcheck.sh`, re-run**: PASS again, confirming the clone dir is
  correctly overwritten on a second invocation rather than accreting stale
  state.
- **Contention scenario (the defect this task fixes)**: started a real goopg
  server directly on the shared `:65433`/`bench/tpch/runtime_goopg/data`
  (simulating a concurrent lane's in-flight session), then ran
  `TPCH_SPOTCHECK_CLONE_WAIT=5 scripts/tpch-spotcheck.sh`. Result: the
  snapshot step correctly reported "still busy" for 3 attempts and exited
  `FATAL` — **and the shared server was still `pg_isready`-live throughout
  and after** (confirmed by a direct `pg_isready -p 65433` check post-run).
  This is the exact scenario the old code would have destroyed via an
  unconditional `stop -D` on the shared dir.
- **`tpch-relsize-arm.sh probe-analyze`**: PASS, server on
  `127.0.0.1:5581`, ANALYZE + cross-session `reltuples` check both correct,
  11.1 s wall clock, `:65433` untouched, private port cleaned up on exit.
- **`tpch-estimate-audit-arm.sh` (`PLAN_ONLY=1`)**: server on
  `127.0.0.1:5582` came up, connected as `tpch@tpch`, ANALYZEd, and planned
  all 22 TPC-H queries successfully (visible per-query output); the run's
  only failure was a missing REFERENCE file
  (`analysis/leftdeep-joins/2026-08-05-p56giii-parity.pg.plans.txt`, deleted
  by an unrelated prior "analysis cleanup" commit `56b24d571`) — a
  pre-existing condition unrelated to this task and out of scope to restore.
  `:65433` untouched.
- **`tpch-acceptance-arm.sh` (`QUERIES=1`)**: PASS (`Q1: OK … rows=4`),
  server on `127.0.0.1:5583`, digest recorded, `:65433` untouched, private
  port cleaned up.

`bash -n` clean on all five changed/new files (no `shellcheck` binary
available in this environment). No Go code touched — `go test`/`go build`
unaffected; only shell scripts changed.

## Consequences

- The mandatory pre-commit-adjacent gate (`tpch-spotcheck.sh`) can now run at
  any time — including concurrently with an M0137 baseline capture or one of
  the three arm scripts — without risking killing whatever the shared
  cluster is doing. This directly addresses the cited defect class ("deferred
  four rounds running").
- Each of the four scripts leaves its ~2 GB private clone on disk in `tmp/`
  between runs (git-ignored, 564 GB free on this host at time of writing) —
  deliberate: `tpch_private_clone_snapshot` always `rm -rf`s and re-clones
  fresh at the start of a run regardless, so there is no correctness reason
  to clean up between runs, and leaving it avoids a needless 2 GB
  delete+recopy on back-to-back invocations.
- No ledger row: this is instrument/harness completeness work (the same
  class M0137-0004/-0005/-0006 judged as not needing one), not a discovered
  PG-behaviour gap — goopg's own engine semantics are untouched.

## Update (M0139 follow-up, 2026-09-15) — the private lane no longer requires the shared server to be DOWN

### What was wrong

The task above gave each lane a private port and a private data dir, but left
exactly ONE way to obtain the clone: `tpch_wait_port_free` + `cp -a`. That
precondition is not "the shared cluster is momentarily idle" — it is **"no
server is listening on :65433 at all"**, because `pg_isready` answering is
the only thing the wait loop tests. And per `CLAUDE.md`'s port table, the
:65433 TPC-H bench cluster is a **persistent** cluster that stays up for
days at a time.

So the snapshot step kept a hard dependency on stopping the very server this
task existed to protect, and the "Consequences" claim above — "the mandatory
gate can now run at any time" — was false in the normal case. The
contention-scenario verification bullet even *recorded* the failure mode as a
success: the gate refusing while the shared server stayed alive is only half
the requirement; the other half is that the gate must still produce its
answer.

`M0139-S1` paid the bill (`m0139-s1-join-leg-hook.md` §"Gates run"): three
retries across ~15 minutes, every attempt dying with
`127.0.0.1:65433 still busy after 60s`, and **two production planner changes
landed with the TPC-H values gate unverified**.

### The fix

Stop treating "a server is up" as un-snapshottable. PostgreSQL's answer for
cloning a *running* cluster is the online base backup, and goopg implements
the server side of it (`internal/backup/basebackup.go`, M0102-0001/-0007,
M0095-0003 — `pg_basebackup` against a live goopg is already a landed,
tested capability). `scripts/lib/tpch-private-clone.sh` now resolves:

| source port | path taken |
|---|---|
| answers nothing | `cp -a` (unchanged — the dir is genuinely at rest, and this is cheapest) |
| answers | `pg_basebackup -h … -X fetch --no-manifest --no-sync` — **no stop, no wait** |
| online path failed | falls back to the old quiesce-then-`cp -a` loop, which still REFUSES rather than copy mid-write |

`TPCH_CLONE_MODE=auto|online|copy` forces a path (`copy` restores the exact
pre-fix behaviour). Helper functions `tpch_private_clone_online` and
`tpch_private_clone_copy` are split out of `tpch_private_clone_snapshot`;
`tpch_wait_port_free` is untouched. All four lane scripts call the same
unchanged `tpch_private_clone_snapshot` signature, so none of them needed a
code change — only their comments and their `*_CLONE_WAIT` tunable docs
(that timeout is now fallback-path-only).

### Why the online path is consistent (not "we hope nothing was written")

It is PG's standard online-backup contract, and every piece of it is
implemented on the goopg side:

1. `BASE_BACKUP` forces a **synchronous IMMEDIATE checkpoint** before
   streaming and reports that checkpoint's REDO LSN as the start LSN
   (`basebackup.go` "Force a synchronous IMMEDIATE checkpoint…").
2. `-X fetch` (`INCLUDE_WAL`) makes the server append **every WAL segment
   from that redo point to the stop LSN** into the same archive, so the
   clone carries the WAL that reconciles pages copied at different instants.
3. `global/pg_control` is written **last** and is patched to name that
   checkpoint, so the clone's first start does
   `database system was not properly shut down; automatic recovery in
   progress` and replays to a consistent state before accepting connections.

Nothing is ever written into the shared data dir, and no server on it is
stopped or started. The one new effect on the shared cluster is load — one
immediate checkpoint plus a ~2 GB sequential read — which is a *timing*
perturbation, not a correctness one, and strictly less invasive than the
`stop -D` this library replaced. A lane that must not perturb it at all can
still pass `TPCH_CLONE_MODE=copy`.

### Verification (2026-09-15, against the live shared cluster)

- `pg_basebackup -X fetch` against the **running** `:65433` (pid 1221143,
  up ~3 h): 1.9 GB clone in 8-25 s; `:65433` `pg_isready`-live before,
  during and after, same pid throughout.
- A goopg server started on that clone recovered cleanly and returned
  data **identical to the live source**: `lineitem`=6 001 255,
  `orders`=1 500 000, `customer`=150 000, `part`=200 000,
  `sum(l_extendedprice)`=229 455 983 170.26 on both.
- **`scripts/tpch-spotcheck.sh`: RESULT=PASS** (`Q12=2`, `Q13=34` — the
  canonical anchors) in **39.9 s wall clock total**, with `:65433` up the
  whole time. This is the exact invocation that could not run at all during
  M0139-S1.
- **Concurrent-writer consistency test** (on a private copy — the shared
  cluster is never written): a source cluster taking 200 000 committed
  inserts was cloned online mid-stream; the clone started, replayed WAL
  (`redo=3988174424 → checkpoint=3989827400`), and showed a **committed
  prefix** of 105 000 rows with every row intact
  (`sum(length(pad))`=21 000 000 = 105 000 × 200) alongside unchanged
  `lineitem`/`orders` counts and sums. That is precisely the
  "consistent as of the backup's stop LSN" guarantee, demonstrated under
  writes rather than assumed.
- Fallback/error paths exercised directly: `TPCH_CLONE_MODE=copy` against
  live `:65433` still refuses (rc=5) and leaves it alive; `copy` against a
  quiescent dir on a dead port still succeeds (rc=0, no stale
  `postmaster.pid`); unknown mode → rc=2; missing source → rc=3.
- `bash -n` clean on all five changed files. No Go code touched.
