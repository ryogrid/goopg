# 02 — Durability Off for Throwaway Test Servers

Status: draft. Part of [test-gate-speedups](README.md).

Throwaway test clusters — the smoke gate's per-commit instance, every
`testutil/cluster` instance in testport, every `initdb.Init` in the initdb
package tests — pay durability fsyncs whose only purpose is surviving a host
crash. A test data dir that is deleted seconds later cannot benefit from crash
durability; the fsyncs are pure waste there. Upstream PostgreSQL's harness has
always acted on this:

- `pg_regress` initializes its temp instance with `initdb --no-clean
  --no-sync` (`postgres/src/test/regress/pg_regress.c:2340`).
- The TAP harness runs `initdb --no-sync`
  (`postgres/src/test/perl/PostgreSQL/Test/Cluster.pm:644`) **and** writes
  `fsync = off` into every test instance's `postgresql.conf`
  (`Cluster.pm:685`).

goopg has the first half of the machinery already (`initdb.Options.NoSync`,
`--no-sync`) but no gate uses it, and the second half (a runtime `fsync` GUC)
does not exist yet. This doc proposes both, in two independently-landable
parts.

## Part A — adopt `--no-sync` everywhere a throwaway cluster is inited (zero prod code)

Measured saving (probe protocol in §5): **0.77 s → 0.14 s per init** on this
host. Multiplied out: ≈100 s of `internal/initdb`'s ~223 s; ≈0.6 s × 351
testport boots ≈ 3.5 min per full testport run; ~0.6 s per commit in the smoke
hook.

### A.1 Smoke gate (`scripts/ralph-precommit-test.sh`)

```diff
-./bin/goopg init -D "$DATADIR"
+# Throwaway per-commit instance — skip the recursive durability fsync, exactly
+# as upstream pg_regress does for its temp instance (pg_regress.c: initdb
+# --no-clean --no-sync). The data dir is deleted in the EXIT trap.
+./bin/goopg init -D "$DATADIR" --no-sync
```

(Current plain init at `scripts/ralph-precommit-test.sh:208`.)

### A.2 `internal/testutil/cluster` — default `--no-sync`, opt-out for durability tests

`Options.InitArgs` (`cluster.go:46`) already appends extra args to
`goopg init`, so a *caller-side* fix is possible today with zero harness
change. But 351 call sites shouldn't each repeat it; make the harness default
match upstream `Cluster.pm` and let durability tests opt out:

```go
// Options gains:
//
//	// SyncInit forces the durability fsync pass of `goopg init` (i.e. does
//	// NOT pass --no-sync). Default false: test clusters are throwaway, so
//	// init skips the recursive fsync, matching upstream pg_regress /
//	// PostgreSQL::Test::Cluster behavior. Set true in crash-recovery /
//	// restart-durability tests that assert on-disk state across a kill.
//	SyncInit bool

// (*Cluster).Init() becomes:
func (c *Cluster) Init() error {
	args := []string{"init", "-D", c.dataDir}
	if !c.syncInit {
		args = append(args, "--no-sync")
	}
	args = append(args, c.initArgs...)
	// ... unchanged
}
```

Note the polarity: the new field must default to the FAST path (Go
zero-value `false` ⇒ `--no-sync`), so the flag is named for the exceptional
slow case (`SyncInit`), not the common one.

### A.3 `internal/initdb` package tests

Mechanical sweep: add `NoSync: true` to the ~165 `Init(Options{...})` test
call sites that don't set it (9 of 178 already do) — **except** the
sync-behavior tests themselves (`sync_test.go` asserts both polarities) and
every test on the §4 **resolved** allowlist (the concrete-name list the
implementing loop records, not the raw greps — §4's greps must be re-run
against `internal/initdb` as part of resolving it).
Alternatively (smaller diff, one helper): introduce a package-test helper
`mustInit(t, opts)` that defaults `NoSync: true`, and convert call sites
gradually. The sweep interacts with the template cache of
[04 §1](04-parallelism-and-bootstrap-caching.md) — if the template cache
lands first, most of these sites stop calling `Init` directly and the sweep
shrinks to the helper.

### A.4 Why Part A's risk is Low

`--no-sync` only skips the *final recursive fsync pass* of init
(`internal/initdb/initdb.go:1285-1288`); every byte is still written, and the
OS flushes them lazily. The only behavior lost is surviving a **host** (not
process) crash between init and test end — no gate or test asserts that for a
throwaway dir. Upstream has shipped this default in its own harness for a
decade-plus. The one family that must keep syncing is the init-sync behavior
tests themselves (`internal/initdb/sync_test.go`), which are already explicit
about both polarities.

## Part B — a PG-compatible `fsync` GUC (production feature, own design loop)

Part A removes init-time fsyncs; the server still fsyncs at runtime
(`wal_sync_method`-selected `Fdatasync`/`Fsync`, `internal/wal/sync_linux.go:19,27`)
on every commit group, checkpoint, and smgr sync. For write-heavy suite phases
(`pgbench -i`, DML-heavy testport bodies, WAL-churning executor tests) this is
the remaining durability tax.

### B.1 Proposal

Register a boolean **`fsync`** GUC, default **`on`**, semantics matching
upstream (`postgres/src/backend/utils/misc/guc_tables.c`: "Forces
synchronization of updates to disk"): when `off`, every fsync/fdatasync issued
for durability — WAL flush, checkpoint smgr sync, CLOG/SLRU sync — becomes a
no-op; write ordering and content are unchanged.

- **GUC, not a `GOOPG_*` env var**: PG-name parity is project policy
  (AGENT.md "GUC names must match PG's names exactly"), the parity dashboard
  rewards it, and it makes upstream harness recipes (`Cluster.pm` writing
  `fsync = off`) portable to goopg verbatim. `BootVal` must be PG's default
  (`on`) per the standing GUC-defaults rule — the *harness* opts into `off`,
  never the registry.
- **Wiring**: gate scripts pass it at startup once a `-c`/config path exists
  for it (the smoke gate can append `fsync = off` to the generated
  `postgresql.conf`, exactly `Cluster.pm:685`'s move); `testutil/cluster`
  gains a mirrored `SyncRuntime bool` (default false ⇒ `fsync=off`) with the
  same opt-out-for-durability-tests polarity as A.2.
- **Scope discipline**: this touches WAL/smgr production paths ⇒ it is a
  normal feature loop with its own design doc
  (`docs/design/<id>-NNNN-fsync-guc.md`), race gate, pgbench smoke, and the
  sample-config template update (`internal/config/postgresql.conf.sample` +
  `TestSampleConfigCoversRegistry`). This bundle only reserves the design
  decision; do not implement it from here.

### B.2 Quality risk (Medium) and mitigations

`fsync=off` can mask two real bug classes:

1. **fsync-ordering bugs** (WAL-before-data violations that only bite when
   flush boundaries matter). Mitigation: the crash-recovery and WAL-replay
   suites (§4) always run `fsync=on`, and the **nightly batch keeps durability
   on for all stages** until the A/B parity protocol of
   [06 Part B](06-prompt-changes-and-rollout.md) has compared fast/slow
   nightlies with zero pass/fail divergence.
2. **Missing-fsync-call bugs** (a path that should request durability but
   doesn't). These are invisible to *any* test that doesn't hard-kill the
   host, with fsync on or off — no signal is lost that existed.

## §4 The durability allowlist — tests that MUST keep fsync/sync on

Blanket flips are forbidden; the following families stay on the durable path
(`SyncInit: true` / no `fsync=off`), enumerated by grep criteria so the list
is reproducible and auditable in the implementing loop:

| family | how to find it | why |
|--------|----------------|-----|
| init sync behavior tests | `internal/initdb/sync_test.go` | they assert the sync pass itself |
| crash/kill-then-reopen recovery | `grep -rln 'Kill()\|SIGKILL\|kill -9' internal/testport internal/testutil internal/server internal/initdb internal/wal --include='*_test.go'` then keep those that reopen/restart and assert state | replay after an unclean stop is the durability contract under test |
| WAL replay / recovery | content grep, not filename glob (a future test named differently must not escape): `grep -rln 'Open(\|ReplayWAL\|StartupRecovery' internal/initdb internal/wal --include='*_test.go'`, keep the replay-asserting subset | same |
| restart-durability ports (e.g. TOAST-restart, crash-durability testport rows) | `grep -rln 'Restart(' internal/testport --include='*_test.go'` | the restart IS the assertion |
| physical/logical replication ports | `grep -rln 'pg_basebackup\|pgoutput\|subscription\|standby' internal/testport --include='*_test.go'` | replication interacts with flush LSNs and cluster identity |
| LSN-flush relationship assertions | `grep -rln 'synchronous_commit\|pg_stat_wal\|wal_sync' internal/testport --include='*_test.go'` (today: `recovery_port_test.go`, `e2e_replication_test.go`, both failover E2Es, `pgoutput_interop_test.go`) | the failover E2Es (real PG replaying goopg WAL and vice versa) are exactly where a "flushed-LSN advanced past durable data" bug surfaces |

Everything outside the allowlist is throwaway and eligible. The implementing
loop must record the **resolved concrete test-name list** in its design doc —
downstream references (the A.3 sweep's exception list, doc 03's tmpfs
carve-out) point at that resolved list, not at these greps, which exist to
make the list reproducible and auditable.

Note: **process**-kill recovery tests (SIGKILL the goopg process, host keeps
running) remain valid under `--no-sync`/`fsync=off` *on tmpfs or a healthy
host* only if the page cache survives — which it does for a process kill,
**provided** `off` elides only the sync syscall and never the `write()` /
WAL-buffer-flush-to-OS at commit. B.1 states that semantics; to pin it
mechanically, the fast nightly lane of [06 B.2](06-prompt-changes-and-rollout.md)
must include at least one process-kill recovery test running *with* the fast
knobs — its continued pass is the executable proof that "off = skip sync
syscall only". The families are allowlisted anyway: keeping the durable path
exercised somewhere is the point, and their runtime share is small. The
wal-replication practice card gains a warning
([06 A.3](06-prompt-changes-and-rollout.md)) so future recovery tests are
written durable-by-default.

### What is honestly lost, and what is not

Host-crash-only bug classes — WAL-before-data ordering violations, checkpoint
sync omissions, pg_control/CLOG sync gaps, torn pages vs `full_page_writes` —
are **undetectable by every current local gate regardless of these knobs**:
no gate hard-kills the host, so fsync-on runs give no signal on them either.
Turning fsync off therefore loses no existing detection for that class. The
real residual losses are narrower and named: (1) fsync **error-path**
handling (EIO/ENOSPC surfaced at sync time) goes unexercised wherever the
fast path is used — the allowlist keeps it exercised in the recovery
families; (2) a **timing-coverage shift**: real fsync latency (~ms) piles
concurrent committers onto shared flush waits and exercises group-commit
interleavings that near-zero-latency sync makes rare — this is doc 03's
concern too, and is why `make race-gate` stays off tmpfs
([03 §2](03-tmpfs-data-dirs.md)).

## §5 Probe evidence (2026-07-17, this host)

```bash
# init fsync cost — three modes, throwaway dirs, sizes ~8.9 MB each
time ./bin/goopg init -D "$SCRATCH/initprobe-sync"              # 0.77 s
time ./bin/goopg init -D "$SCRATCH/initprobe-nosync" --no-sync  # 0.14 s
time ./bin/goopg init -D /dev/shm/goopg-initprobe               # 0.09 s
```

Single-sample timings on an otherwise-idle WSL2 host; treat as
order-of-magnitude. The `^TestInit` subset probe in
[03 §5](03-tmpfs-data-dirs.md) (17.9 s → 1.6 s) captures the same fsync
elimination end-to-end through real tests.
