# perf-optimize3 — why goopg's write path trails PostgreSQL more than its read path

date: 2026-07-13 · goopg `e453e3f2` · PostgreSQL 18.3 · pgbench scale 100, c=50

**Question**: goopg is ~2× behind PG on reads but ~7.4× behind on writes —
what makes the write path disproportionately slow?

**Answer (measured + code-attributed)**: the commit flush (`END`) is 91 % of
goopg's write-transaction latency, and it is slow for three write-specific
reasons stacked on the shared ~2× engine tax:

1. **12–18× WAL volume** — every canonical **heap** WAL record embeds a full
   8 KB page image, and each heap write is logged twice (canonical FPI record
   + native logical record): 33.0 KB WAL/txn vs PG's 1.8–2.9 KB, bypassing the
   existing once-per-checkpoint FPI machinery that the btree leaf-insert path
   already uses correctly.
2. **A synchronous CLOG fsync on the commit path** (~6.3 ms each, ~1 per 11
   commits, 6,734/min) — PostgreSQL sets bits in memory and defers pg_xact
   I/O to checkpoint/eviction.
3. **B-tree dead entries are never reclaimed on access** (no
   LP_DEAD/kill-prior-tuple/simple deletion) — the pgbench pkey **doubles in
   2 minutes** (+166 MB) while PG's grows 0 bytes, compounding WAL volume via
   splits and degrading throughput over time.

| | read `-S` | write `-N` |
|---|---:|---:|
| goopg | 91,783 TPS | 2,141 TPS |
| PostgreSQL | 180,257 TPS | 15,806 TPS |
| gap | 1.96× | **7.38×** |

Caveat: the absolute commit-cycle costs are fsync-latency-bound on this
WSL2/ext4 host (multi-ms floor); mechanism 1's byte/CPU cost is
device-independent while mechanism 2's weight scales with the device's fsync
floor — see 02 for the storage-dependence discussion.

## Documents

- [00-methodology.md](00-methodology.md) — conditions, diagnostics, AUX probes
- [01-results.md](01-results.md) — headline numbers, per-statement latencies,
  wait events, HOT/size data, fsync/WAL-volume attribution, profiles
- [02-write-path-analysis.md](02-write-path-analysis.md) — the four mechanisms
  and how they compose into 7.4×
- [03-code-attribution.md](03-code-attribution.md) — goopg ↔ PG code, file and
  function level
- [04-improvement-candidates.md](04-improvement-candidates.md) — ranked fix
  directions with expected impact and blast radius, plus observability gaps

Artifacts: `runs/20260713_004324/` (pgbench outputs, wait samples, size/WAL
snapshots, pprof profiles, env provenance, `SUMMARY.txt`; AUX in `aux/`,
`aux2/`). Scripts: `scripts/run_rw50.sh`, `scripts/aux2_fsync_probe.sh`
(`aux_fsync_probe.sh` kept for provenance — its goopg LSN query and strace
attach were non-functional; superseded by aux2).
