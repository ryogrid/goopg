# 03 — Resources & Parallelism

The batch shares a 32 GiB WSL2 host (plus 64 GiB swap file) with a live Ralph
loop. This doc fixes the memory budget, the parallelism policy, and the
isolation rules. Constraint source:
`analysis/tests-overview-260706/03-execution-constraints.md`.

## A. Memory budget

Working assumption: the Ralph loop (claude + MCP helpers + its own foreground
gates, which may themselves run a capped server) can occupy **up to
~12–15 GiB** at any moment. The batch budget: **every batch stage runs inside
its own `goopg-test-run.sh` cgroup scope** (server-less stages included), so
the batch's steady-state footprint is ≈ 12 GiB (sum of `MemoryHigh` soft caps
per phase) with a hard ceiling of 16 GiB during S1 / 12 GiB during S2 (sum of
`MemoryMax`). Against a 15 GiB loop that keeps typical usage near
12 + 15 ≈ 27 GiB on a 31 GiB-RAM host; the hard ceilings bound the worst case,
and `MemoryHigh` reclaim/throttling engages well before `MemoryMax`.

| Stage | Mechanism | Defaults (env-overridable) |
|-------|-----------|----------------------------|
| Lane L units | `scripts/goopg-test-run.sh` (capping works for server-less commands too) | `GOOPG_CG_UNIT=goopg-nightly-units`, `GOOPG_MEM_HIGH=6G`, `GOOPG_MEM_MAX=8G`, `GOOPG_MEM_SWAP_MAX=0`, `GOMEMLIMIT=5GiB`, `GOFLAGS=-p=4` |
| Lane L race | `scripts/goopg-test-run.sh` | same caps, `GOOPG_CG_UNIT=goopg-nightly-race` |
| Lane H testport | `scripts/goopg-test-run.sh` | `GOOPG_CG_UNIT=goopg-nightly-testport`, `GOOPG_MEM_HIGH=6G`, `GOOPG_MEM_MAX=8G`, `GOOPG_MEM_SWAP_MAX=0`, `GOMEMLIMIT=5GiB` |
| Lane H pgbench nightly (s=50 c=100 j=20 T=180×3) | `scripts/goopg-test-run.sh` | `GOOPG_CG_UNIT=goopg-nightly-pgbench`, `GOOPG_MEM_HIGH=6G`, `GOOPG_MEM_MAX=8G`, `GOOPG_MEM_SWAP_MAX=0`, `GOMEMLIMIT=5GiB`; free-port probe from 5555; throwaway per-run data dir |
| S2 TPC-H server + runner | `scripts/goopg-test-run.sh` | `GOOPG_CG_UNIT=goopg-nightly-tpch`, `GOOPG_MEM_HIGH=10G`, `GOOPG_MEM_MAX=12G`, `GOOPG_MEM_SWAP_MAX=0`, `GOMEMLIMIT=9GiB` |

Notes:

- All nightly caps are **deliberately below** the repo defaults (20G/24G):
  those defaults assume the capped run is the only heavy consumer; nightly it
  is not. testport tests never need TPC-H-scale intermediates. Lane L's caps
  are per-scope (the whole `go test` process tree), which is a real aggregate
  bound — unlike `GOMEMLIMIT`, which is per-process and soft.
- Within a lane only one stage runs at a time, so the S1 hard ceiling is one
  Lane L scope (8G) + one Lane H scope (8G) = **16 GiB `MemoryMax`**;
  steady-state is bounded by the 6G+6G `MemoryHigh` pair ≈ 12 GiB.
- S2 gets the double-digit cap **only because it runs solo** (post-barrier,
  both lanes finished): hard ceiling 12 GiB.
- `GOMEMLIMIT` is a Go *soft* target; the cgroup `MemoryMax` is the actual
  containment. Both are set, GOMEMLIMIT ~1–3 GiB under `MemoryHigh` so GC
  engages before throttling.
- **Every concurrent capped run needs a distinct `GOOPG_CG_UNIT`** — systemd
  refuses duplicate scope names. The `goopg-nightly-*` namespace guarantees no
  collision with the loop's units (`goopg-test`, `goopg-server`,
  `ralph-precommit-goopg-$$`, …). Stage cleanup runs
  `systemctl --user stop <unit>.scope` + `reset-failed` as needed.
- Where systemd delegation is unavailable the wrapper degrades to uncapped
  with a WARNING (existing behavior); the batch records that warning in the
  stage log and proceeds — same graceful-fallback contract as CI.

## B. Parallelism policy

Requirement: conservative, but parallel where possible.

1. **Exactly two concurrent lanes in S1** (Lane L server-less / Lane H
   server-based), chosen for complementary resource profiles (doc 01). No
   third lane: each additional lane is another multi-GiB worst case and
   another cgroup/port to manage, for shrinking wall-clock returns.
2. **Within-lane is strictly sequential.** In particular the testport suite is
   one `go test` invocation — Go's own per-package/test parallelism inside it
   is left at defaults; the suite boots servers and is the reason
   `internal/testport` is excluded from CI's parallel unit set.
3. **CPU fan-out caps:** Lane L uses the unit set exactly as CI does
   (`go test -timeout 10m $(go list …)`) — Go schedules package builds/tests
   across cores. To keep two lanes from oversubscribing the box, Lane L
   exports `GOFLAGS="-p=4"` (package-level parallelism cap) for the nightly
   context; Lane H's single-package run is unaffected. Value env-overridable
   (`NIGHTLY_GO_P`, default 4).
4. **S2 is solo by construction** (barrier before it). Never run TPC-H
   concurrently with anything else from the batch.
5. **No unbounded `go test ./...` anywhere** — the `.ralph/PROMPT.md` memory
   guard rule applies to the batch too.

## C. `mem_guard.py` NON-interaction (the loop's watchdog) and kill forensics

`~/.ralph/mem_guard.py` (one instance per `ralph_loop.sh`) watches only the
**descendants of its own loop PID**: pressure = the sum of unprotected
*loop-descendant* RSS+swap vs 70 % of total RAM+swap, and its SIGKILL victim
is chosen from that same descendant set. The nightly batch is spawned
`setsid`-detached from the scheduler (doc 06), i.e. **outside the loop's
process tree** — so batch processes are neither counted toward the guard's
trigger nor eligible as its victims, and the batch gets no guard protection
either. The batch's memory containment is entirely its own cgroup caps (§A).

Consequences for the design:

- **Primary defense is the budget in §A.** The caps are the containment; the
  guard is irrelevant to the batch by construction.
- **Kill-forensics rule:** a batch process that dies with signal 9 /
  "Killed" and no panic/assertion was killed by one of:
  1. **its own scope's `MemoryMax`** — confirm via
     `journalctl --user -u <goopg-nightly-*>.scope` or `dmesg | grep -i
     'memory cgroup out of memory'` naming the scope;
  2. **the host-level Linux OOM killer** (global pressure, e.g. the loop
     spiking while the batch is near its caps) — `dmesg` OOM report without
     a nightly scope name.
  Either way the run is classified `resource-kill` → **inconclusive**
  (doc 02 §B), not a product regression — and repeated occurrences are the
  signal to lower the batch caps or move the fire hour (doc 06 §E).
  (`~/.ralph/logs/mem_guard.log` `PRESSURE` lines remain useful *context* —
  they show the loop side was under pressure at the time — but the guard
  cannot have been the batch's killer.)
- The batch does **not** attempt to join the loop's process tree to gain
  guard "protection", and must never be given caps so high that host OOM
  becomes the effective limit: on a pressured host, a dead nightly run is
  the correct outcome; the loop's work has priority.

## D. Port & data-dir isolation

Reserved lanes already in use on this host (survey doc 03): 5432 (make
start/CI), 5433/5434 (pgbench-compare), 5533/5534 (perf), 5535+ (precommit
probes upward), 65433 (TPC-H bench / loop spotcheck), 65434 (nightly TPC-H
clone), 65435 (nightly TPC-DS clone), 65436 (TPC-DS goopg SF=1,
`bench/tpcds/runtime_goopg/data`), 65437 (TPC-DS goopg SF=0.5 regression
gate), 65438 (TPC-DS PostgreSQL reference, `bench/tpcds/runtime/pgdata`),
15435 (regress runner). Full cross-benchmark map: repo-root `CLAUDE.md`.
(2026-07-27: TPC-DS moved to `bench/tpcds/`; the SF0.5 gate previously
defaulted to 65434 and collided with the nightly TPC-H lane — fixed.)

Batch policy:

| Batch stage | Port | Data dir | Rationale |
|-------------|------|----------|-----------|
| testport suite | framework-chosen ephemeral ports | per-test temp dirs | the harness already isolates itself |
| pgbench nightly | 5555-upward probe | `tmp/goopg-nightly-pgbench-data-$$` | out of the precommit tool's 5535+ probe range; probe-upward tolerates any holder |
| TPC-H | **65434** (batch-reserved; `NIGHTLY_TPCH_PORT`) | snapshot copy `tmp/goopg-nightly-tpch-data` (cloned from `bench/tpch/runtime_goopg/data`) | canonical 65433 stays the loop's spotcheck lane; see below |

**Busy-resource rule:** the batch touches the canonical 65433 lane ONLY
during the snapshot-copy window (doc 05 §A.2): wait up to `NIGHTLY_PORT_WAIT`
(default 900 s, polling every 15 s) for 65433 to be server-free, `cp -a` the
data dir, verify 65433 stayed free (retry ×3 on interference); never
obtainable ⇒ S2 = `skip(port-busy)` with a note. Never kill a holder, never
`pkill -f goopg` (self-match hazard, and it would hit the loop's servers).
Server shutdown is always `goopg stop -D <datadir>` (or kill of the exact PID
from `postmaster.pid`) plus scope stop — the pattern
`ralph-precommit-test.sh` already uses.

**Concurrency with the loop's spotcheck (requirement):** `tpch-spotcheck.sh`
unconditionally stops whatever server runs on the canonical dir, and it is
light (~3–4 min). By running S2 on a clone at 65434, a spotcheck fired by the
loop at ANY point during the batch's multi-hour TPC-H stage proceeds
undisturbed on 65433, and vice versa — the two are fully concurrent except
for the copy window, which fits between spotcheck runs.

## E. Disk

- Each run dir holds full `go test -v` logs + 22 EXPLAIN plans + pgbench
  output; estimate ≤ ~200 MB/night uncompressed. Retention (keep 14, doc 04)
  bounds `ci/logs/` to a few GB.
- Preflight's ≥ 10 GB free-disk check protects the WAL/data dirs, which are
  the actual big consumers.
