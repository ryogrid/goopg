# perf-optimize3 — 00: Methodology

date: 2026-07-13 · goopg commit: `e453e3f2` (branch `wal-system-pgnize`, after the
wal-backend-flush rewrite slices 1–7) · PostgreSQL 18.3 (`postgres/local_install`)

## Question

goopg trails PostgreSQL on **both** pgbench read and write workloads, but the
write-path gap is far larger. This bundle measures both paths under identical
conditions, attributes the asymmetry to specific mechanisms with profiling and
engine statistics, and confirms the attribution against both code bases.

## Workloads

| Tag | pgbench flags | Path exercised |
|-----|---------------|----------------|
| `S` | `-S` (select-only) | read: point SELECT via PK btree, no WAL, no commit flush |
| `N` | `-N` (simple-update) | write: `BEGIN; UPDATE accounts; SELECT; INSERT history; END` — heap update + WAL + commit fsync |

Both engines run both workloads: 4 headline runs total (`goopg_S`, `goopg_N`,
`pg_S`, `pg_N`).

## Conditions (byte-parity with analysis/perf-optimize2 run 20260712_114859)

- scale **100**, clients **c=50 / j=50**, duration **120 s** per run, `-P 30 -r`.
- Both engines **uncapped** (no cgroup/taskset); `GOMEMLIMIT=18GiB` on goopg
  (Go-runtime soft limit only, as in every prior perf-optimize run).
- Identical `postgresql.conf` deltas on both: `max_connections=200`,
  `shared_buffers=2560MB`, `wal_buffers=128MB`, `checkpoint_timeout=24h`,
  `max_wal_size=1024GB`, `min_wal_size=1024MB`,
  `checkpoint_completion_target=0.9`; PG additionally `fsync=on` (explicit).
  `synchronous_commit` is the default **on** for both.
- goopg binary built from **clean HEAD** in a throwaway worktree (the main tree
  carried unrelated uncommitted WIP); sha256 in `runs/<id>/env.txt`.
- Engines run **sequentially** (goopg completely first, then PG) so they never
  compete for CPU/disk. Each workload starts on a **fresh server restart**
  (clears goopg mutex/block profile history; symmetric restart on PG).
- Host: WSL2 (kernel in `env.txt`), data dirs on ext4 under the WSL rootfs.

## Diagnostics captured per run

| Diagnostic | goopg | PG |
|---|---|---|
| pgbench `-r` per-statement latency | ✓ | ✓ |
| `pg_stat_activity` wait-event sampling, 5 Hz | ✓ | ✓ |
| CPU profile (90 s window in-run) + allocs/mutex/block | pprof | — (PG: wait events serve this role) |
| `pg_stat_wal` before/after | (stub — see below) | ✓ |
| `pg_relation_size` of pgbench relations before/after `N` | ✓ | ✓ |
| `n_tup_upd` / `n_tup_hot_upd` after `N` | attempted | ✓ |

**goopg `pg_stat_wal` caveat**: goopg's `pg_stat_wal` view is a zero stub, so
goopg WAL volume and fsync counts come from the AUX probe instead.

## AUX attribution probe (separate, disclosed non-headline run)

`scripts/aux2_fsync_probe.sh` restarts each engine on the same data and runs a
60 s c=50 simple-update to capture:

- **WAL bytes/txn** — PG: `pg_current_wal_lsn()` delta ÷ pgbench xacts.
  goopg: `pg_current_wal_lsn()` is **not wired at runtime** (catalog-only
  stub), so goopg uses the `pg_stat_wal_io`
  `wal_buffers_{flush,overflow}_drain_bytes` delta instead — bytes actually
  drained to segment files, an equivalent measure of emitted WAL.
- **fsync rate / group-commit width** — goopg: the server runs as the child of
  `strace -f -c -e trace=fdatasync,fsync` (`ptrace_scope=1` forbids attaching
  to a running process; filtered `-c` tracing perturbs only the two traced
  syscalls — that perturbation is why this is AUX, not headline); PG:
  `pg_stat_io` (object='wal') `fsyncs` delta (PG 18 moved WAL sync counters
  out of `pg_stat_wal`). Width = xacts ÷ fsyncs.

A first probe iteration (`scripts/aux_fsync_probe.sh`, results in `aux/`) had
a non-functional goopg LSN query and strace attach; it is kept for provenance
and its PG-side WAL measurement (1,801 B/txn at full 15.6 k TPS) remains valid.

## Scripts and artifacts

- `scripts/run_rw50.sh` — the 4-run headline suite; everything lands under
  `runs/<RUN_ID>/` (pgbench outputs, wait samples, walstat/size snapshots,
  `profiles/*.pb.gz`, `env.txt`, `SUMMARY.txt`).
- `scripts/aux_fsync_probe.sh` — the AUX probe above, `runs/<RUN_ID>/aux/`.

## Document map

- `01-results.md` — headline numbers, per-statement latencies, ratios.
- `02-write-path-analysis.md` — profiling + wait-event + AUX attribution of the
  write-path gap.
- `03-code-attribution.md` — goopg and PostgreSQL code analysis tying each
  measured cost to its mechanism.
- `04-improvement-candidates.md` — ranked, design-level fix directions.
