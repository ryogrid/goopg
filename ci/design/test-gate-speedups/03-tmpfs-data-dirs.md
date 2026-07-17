# 03 — tmpfs Data Directories for Test Clusters

Status: draft. Part of [test-gate-speedups](README.md).

Placing throwaway test data dirs on tmpfs (`/dev/shm`) makes every write a
memory write and every fsync a no-op. It composes with
[02](02-durability-off-for-test-servers.md) (which removes the *calls*; tmpfs
removes the *cost* of the ones that remain) and with the template cache of
[04](04-parallelism-and-bootstrap-caching.md) (copying a template within
tmpfs is a memcpy).

## 1. Feasibility: the O_DIRECT blocker no longer exists

tmpfs rejects `O_DIRECT` opens (`EINVAL`), which historically would have made
this a non-starter. goopg **removed O_DIRECT entirely** in the buffered-I/O
migration (design `docs/design/0042-0002-buffered-io-migration.md`, landed
2026-05-04): the storage manager opens data files with plain
`O_RDWR|O_CREATE` (`internal/storage/smgr.go:571-572`) and the WAL writer
likewise; durability comes from fsync/fdatasync boundaries. Verification
(2026-07-17):

```bash
$ grep -rn O_DIRECT internal --include='*.go' | grep -v _test
internal/initdb/wal_io_views.go:7:// O_DIRECT columns (direct_io_active, ...
# ^ a comment about removed observability columns — no open site remains
```

Empirically confirmed: `./bin/goopg init -D /dev/shm/...` succeeds and a full
initdb-test subset runs green on tmpfs (§5). Executor spill files already go
to `os.TempDir()` (`internal/executor/spill.go:322`), so a `TMPDIR` redirect
covers those too.

## 2. The cgroup-v2 interaction — this is the design's load-bearing wall

**tmpfs pages are charged to the memory cgroup of the process that faults
them in.** Every gate server runs inside a memory-capped transient scope
(`scripts/goopg-test-run.sh`: `GOOPG_MEM_HIGH=20G` throttle,
`GOOPG_MEM_MAX=24G` kill, `memory.swap.max=0`), and the nightly stages run in
much tighter scopes (6G/8G). A goopg server writing its data dir on tmpfs
consumes its own scope's budget with **page-cache-like pages that cannot be
reclaimed** (swap is off and tmpfs pages have nowhere to go). Under-sizing
converts an I/O speedup into OOM-kill flakiness — strictly worse than slow.

Sizing rule for any scope that opts in:

```
scope_mem_max  >=  process peak RSS  +  max expected data-dir size  +  25% headroom
```

Data-dir sizes measured/known:

| workload | data dir size |
|----------|---------------|
| fresh `goopg init` | ~9 MB |
| pgbench scale 1 (smoke gate) | tables ~15–25 MB, **but transient `pg_wal` up to ~1 GB**: `max_wal_size` BootVal is 1024 MB (`internal/config/defaults.go`, PG-matching) and this repo measured ~33 KB WAL/txn, so three 30 s write windows approach the checkpoint-recycling plateau |
| typical testport test | tens of MB |
| nightly pgbench SF 50 | GB-class |
| TPC-H bench dir | 2.2 GB (persistent — not throwaway at all) |

Hence the decision table:

| gate | tmpfs? | rationale |
|------|--------|-----------|
| `internal/initdb` / `internal/executor` / `internal/catalog` unit tests (via `TMPDIR`) | **yes** | dirs ≤ tens of MB, uncapped or generous scope — EXCEPT tests on the durability allowlist, which use the disk-backed helper of §3.4 |
| `internal/testport` (via `TMPDIR`) | **yes** | per-test dirs small; nightly testport scope 6G/8G has room, but see §4 preflight; allowlisted families use §3.4 |
| pgbench smoke (`ralph-precommit-test.sh`) | **yes, opt-in flag** | tables small; transient pg_wal ~1 GB fits the roomy 20G/24G default scope and the ≥2 GB preflight below |
| `scripts/pg-regress-runner.sh` | **yes** | same shape as smoke |
| **`make race-gate`** | **no (initially)** | the race detector's most valuable schedules arise when real fsync latency (~ms) piles concurrent committers onto shared WAL-flush waits; near-zero-latency tmpfs sync makes those interleavings rare, quietly narrowing schedule coverage exactly where this project has historically had races. Revisit only after a dedicated fast/slow A/B of race-gate results ([06 B.2](06-prompt-changes-and-rollout.md)) |
| nightly pgbench SF 50 | **no** | GB-class dir inside a 6G/8G scope — budget math fails |
| TPC-H (spotcheck + nightly sweep) | **no** | persistent 2.2 GB dir, CPU-bound anyway — little win, large budget cost |

`/dev/shm` on this host: 16 GiB (half of RAM, the tmpfs default), ~4 MB used.
Note `/dev/shm`'s 16 GiB is a *filesystem* limit; the per-scope cgroup limit
binds first for capped workloads.

## 3. Mechanism

Two independent seams, both opt-in:

### 3.1 Unit-test packages: `TMPDIR`

Go's `t.TempDir()` → `os.MkdirTemp("", ...)` → honors `$TMPDIR`. No code
change at all:

```bash
mkdir -p /dev/shm/goopg-test-tmp
TMPDIR=/dev/shm/goopg-test-tmp go test ./internal/initdb/...
```

Gate adoption = exporting `TMPDIR` inside the gate scripts (diff in §3.3),
so interactive `go test` runs are untouched until the operator opts in.
`t.TempDir()` cleans up per-test; a `trap`/stage-end `rm -rf` of the parent
dir catches crash leftovers.

### 3.2 Smoke gate: `RALPH_PRECOMMIT_TMPFS=1`

```diff
 RUN_ID="$$"
 DATADIR="tmp/ralph-precommit-goopg-data-$RUN_ID"
+# Opt-in tmpfs data dir (RALPH_PRECOMMIT_TMPFS=1): all writes become memory
+# writes and fsyncs become no-ops. Falls back to the on-disk default when
+# /dev/shm is missing or lacks headroom, so the gate never gets a new
+# failure mode from this knob. Headroom is >=2 GB because the transient
+# pg_wal under the three write workloads approaches ~1 GB before
+# max_wal_size-triggered recycling plateaus it — the scale-1 TABLES are
+# small, the WAL churn is not. NOTE: tmpfs pages are charged to the server's
+# cgroup scope; the default GOOPG_MEM_HIGH/MAX (20G/24G) absorb ~1 GB
+# easily, but do not copy this pattern into tighter scopes without redoing
+# the sizing math (ci/design/test-gate-speedups/03 §2).
+TMPFS_MIN_AVAIL_KB=$((2 * 1024 * 1024))   # refuse below 2 GB free
+if [ "${RALPH_PRECOMMIT_TMPFS:-0}" = "1" ]; then
+  # Sweep leaked dirs from SIGKILLed prior runs first (the EXIT trap cannot
+  # cover SIGKILL, and tmpfs leaks hold RAM until reboot). RUN_ID is the
+  # owning PID, so liveness-keyed deletion is safe for concurrent runs,
+  # unlike an age-based sweep.
+  for d in /dev/shm/ralph-precommit-goopg-data-*; do
+    [ -e "$d" ] || continue
+    pid="${d##*-}"
+    kill -0 "$pid" 2>/dev/null || rm -rf "$d"
+  done
+  # `|| true` is load-bearing: without it a missing /dev/shm makes the
+  # pipeline fail under `set -euo pipefail` and kills the gate instead of
+  # falling back.
+  avail_kb="$(df --output=avail /dev/shm 2>/dev/null | tail -1 | tr -d ' ' || true)"
+  if [ -n "$avail_kb" ] && [ "$avail_kb" -ge "$TMPFS_MIN_AVAIL_KB" ]; then
+    DATADIR="/dev/shm/ralph-precommit-goopg-data-$RUN_ID"
+  else
+    echo "ralph-precommit-test.sh: RALPH_PRECOMMIT_TMPFS=1 but /dev/shm unavailable or <2GB free — using disk" >&2
+  fi
+fi
 LOGFILE="tmp/ralph-precommit-goopg-$RUN_ID.log"
 CG_UNIT="ralph-precommit-goopg-$RUN_ID"
```

(Current assignment at `scripts/ralph-precommit-test.sh:150`. The existing
EXIT trap already `rm -rf "$DATADIR"` and is registered after this block, so
it works unchanged for the tmpfs path; the PID-liveness sweep above covers
the SIGKILL case the trap cannot. Stage-2 acceptance
([06 B.1](06-prompt-changes-and-rollout.md)) must record end-of-smoke
`du -s "$DATADIR"` once to validate the ~1 GB pg_wal estimate empirically.)

### 3.3 Gate-side `TMPDIR` export (units part of the same script)

```diff
-go test -timeout 10m $pkgs
+# Opt-in tmpfs for t.TempDir()-backed test data dirs (same preflight/fallback
+# contract as RALPH_PRECOMMIT_TMPFS in Part 2, including the >=headroom
+# check — a near-full /dev/shm under co-load must fall back to disk, not
+# ENOSPC whole packages). The path is deliberately FIXED, not per-run:
+# TMPDIR is part of the go-test result-cache key for any test that calls
+# t.TempDir(), so a changing value would invalidate the cache on every run
+# (05 §1 rule 3). t.TempDir() creates unique per-test subdirs, so concurrent
+# gate runs sharing the parent are safe, and it removes them on test
+# completion; crash leftovers are swept by an AGE-based sweep, never an
+# EXIT trap (which could delete a live concurrent run's dirs). Sweep age is
+# 12 h, comfortably past the worst observed cold co-loaded stage (~3.1 h) —
+# per-TEST dirs live minutes, but template dirs (doc 04 §1) live for the
+# whole test process and must not be swept mid-run.
+if [ "${RALPH_PRECOMMIT_TMPFS:-0}" = "1" ]; then
+  avail_kb="$(df --output=avail /dev/shm 2>/dev/null | tail -1 | tr -d ' ' || true)"
+  if [ -n "$avail_kb" ] && [ "$avail_kb" -ge "$TMPFS_MIN_AVAIL_KB" ]; then
+    export TMPDIR="/dev/shm/goopg-gate-tmp"
+    mkdir -p "$TMPDIR"
+    find "$TMPDIR" -mindepth 1 -maxdepth 1 -mmin +720 -exec rm -rf {} + 2>/dev/null || true
+  else
+    echo "ralph-precommit-test.sh: /dev/shm unavailable or low — t.TempDir stays on disk" >&2
+  fi
+fi
+go test -timeout 10m $pkgs
```

(`TMPFS_MIN_AVAIL_KB` is defined once near the top of the script so Part 1
and Part 2 share it — the diffs above show the two use sites.)

### 3.4 The durability carve-out mechanism (required by [02 §4](02-durability-off-for-test-servers.md))

A package-wide `TMPDIR` export cannot exempt individual tests — `t.TempDir()`
has no opt-out — so the allowlist promise needs an explicit mechanism, landed
in stage 1 alongside `SyncInit`:

```go
// internal/testutil (sketch) — a disk-backed temp dir for durability tests.
// Ignores TMPDIR by construction: allowlisted crash-recovery / WAL-replay /
// sync tests call this instead of t.TempDir(), so a gate-level tmpfs
// redirect can never silently move them onto a filesystem where fsync is a
// no-op. Falls back to t.TempDir() semantics (cleanup registered on t).
func DurableTempDir(t *testing.T) string {
	dir, err := os.MkdirTemp("/var/tmp", "goopg-durable-*") // never /dev/shm, never $TMPDIR
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
```

Until every allowlisted test is converted to `DurableTempDir` (stage 1 does
the conversion together with resolving the allowlist), the `TMPDIR` export
must not be enabled for the packages containing allowlisted families
(`internal/initdb`, `internal/wal`, `internal/testport`) — i.e. the seam
ships dark and lights up package-by-package as the conversion lands. This is
the mechanism behind the decision-table exemptions in §2; without it the
allowlist is a statement of intent, not a property of the system.

## 4. Quality risk and mitigations

| risk | severity | mitigation |
|------|----------|------------|
| cgroup OOM flakes from tmpfs charging | medium | sizing rule + decision table (§2); `df` headroom preflight with disk fallback (§3.2/3.3); opt-in flags so nothing changes until enabled |
| "real disk" semantics not exercised: assertions (ENOSPC, sync error paths) and **schedule coverage** (fsync latency piling committers onto shared flush waits) | medium | assertion-side: these gates never asserted disk semantics, and the allowlisted families run on disk via the §3.4 helper. Schedule-side is real, not dismissible: `make race-gate` stays off tmpfs (§2 table) and the nightly A/B protocol ([06 B](06-prompt-changes-and-rollout.md)) is the empirical backstop |
| **the host mem_guard is blind to tmpfs**: `~/.ralph/mem_guard.py` sums VmRSS+VmSwap of descendant processes, but buffered-write tmpfs file pages are shmem charged to no process's RSS — GBs in `/dev/shm` are invisible to its pressure sum, so the guard fires *later* than designed, and after it SIGKILLs a test process the orphaned tmpfs files keep holding RAM, so subsequent kills don't relieve pressure either (a fruitless kill-loop against innocent heavy processes) | medium | the PID-liveness sweep (§3.2) reclaims orphans at every gate start, unconditionally; the ≥2 GB preflight bounds live usage; follow-up (recorded for the rollout loop): teach `mem_guard.py` to add `du -s /dev/shm/goopg-*` into its pressure accounting |
| tmpfs leak on SIGKILL of the gate itself | low | PID-liveness-keyed start-of-run sweep (§3.2) for smoke data dirs; 12 h age sweep for the shared `TMPDIR` parent (§3.3) |
| WSL2: `/dev/shm` counts against the same 32 GiB VM RAM the workloads use | medium | same sizing rule; steady-state usage is tens of MB, transient smoke peak ~1 GB — well below the 16 GiB mount, and bounded by the preflight |

## 5. Probe evidence (2026-07-17, this host)

End-to-end through real tests (cache defeated with `-count=1` so the tests
actually run):

```bash
$ go test -count=1 -run '^TestInit' ./internal/initdb/            # default /tmp (ext4)
ok  github.com/goopg/goopg/internal/initdb  18.014s   # repeat: 17.818s

$ mkdir -p /dev/shm/goopg-testtmp
$ TMPDIR=/dev/shm/goopg-testtmp go test -count=1 -run '^TestInit' ./internal/initdb/
ok  github.com/goopg/goopg/internal/initdb  1.600s
```

**11× on this subset**, entirely from making the per-test recursive-fsync
and catalog writes memory-speed. The subset (~30 `TestInit*` functions) is
init-heavy and therefore near the best case; packages with more compute per
test (executor) will see proportionally less. Single-run samples on an idle
host; the disk number was reproduced twice (18.0 s / 17.8 s).
