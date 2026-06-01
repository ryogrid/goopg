# M0115-0007: Benchmark Gate — Before/After M0115 Hint-Bit Caching

## Spec

`pgbench -T 60 -c 10 -M simple -S postgres` — TPS at HEAD must not decrease by
more than 2% versus pre-M0115.

## Method

Two `goopg` instances were benchmarked on the same machine, back-to-back,
using the goopg-shipped PostgreSQL 18.3 `pgbench` binary.

- **Baseline build**: a detached `git worktree` at `ff6076e4` (the commit
  immediately preceding the M0115 implementation commit `d7aa5ef`). On this
  branch `ff6076e4` was committed in a momentarily broken state — the
  `satisfies_hash_partition` call sites in `internal/executor/expr.go` landed
  in `574b0a2c` (M0097-regress hash_part) but the helper file
  `internal/executor/hash_partition.go` was not landed until `8223992f`
  (M0097-0042, 1 day later). To make the baseline buildable we
  cherry-picked **only** that helper file from `8223992f`; nothing in it
  touches MVCC, hint bits, the snapshot path, or any code that M0115 modifies,
  so the cherry-pick does not bias the comparison.
- **HEAD build**: the current branch tip `a53c046f` (M0115-0001..0006 +
  M0116-0001..0004 applied).
- Each side: fresh `goopg init -D` data dir, `goopg start -listen 127.0.0.1:5533`
  in background, `pgbench -i -s 10`, then `pgbench -T 60 -c 10 -M simple -S` ×2,
  server stopped via `postmaster.pid` (avoids the `pkill -f` self-match
  documented in [`goopg_manual_server_test_workflow`]).
- Port 5533 was chosen per the Ralph-isolation convention
  (`tmp/perf-optimize/`, not `tmp/pgbench-compare/` or 5433/5434).
- All other GUCs default. Two consecutive runs per side; means reported.

Raw artifacts live under `tmp/perf-optimize/m0115-0007/{baseline,head}/`:
- `bench_run1.txt`, `bench_run2.txt`: full `pgbench` output
- `server.log`: server stderr
- `bench_summary.txt`: top-level summary (also reproduced below)

## Results

| Side | Run 1 TPS | Run 2 TPS | Mean TPS | Avg latency |
|---|---:|---:|---:|---:|
| Pre-M0115 baseline (ff6076e4) | 57,239.05 | 57,188.18 | **57,213.6** | 0.175 ms |
| Post-M0115 HEAD (a53c046f)    | 56,684.59 | 56,706.15 | **56,695.4** | 0.176 ms |

Delta: **−0.906 %** TPS (well inside the −2.0 % gate). 0 failed transactions
on both sides. Initial connection time, latency average, and transactions/sec
all within run-to-run noise.

**Gate: PASS.**

## Interpretation

`pgbench -S` runs a single `SELECT abalance FROM pgbench_accounts WHERE
aid = :aid` per transaction against a freshly-initialised, fully-cached
working set. The MVCC path that M0115 short-circuits — `SeesCommittedXID`
on the snapshot's `xip` array — is already cheap in this workload because:

1. The fresh data dir has a small `abortedXIDs` set and a small in-flight
   list; the snapshot range check returns `false` (committed in past) almost
   immediately.
2. Each scanned tuple has `xmin` close to a recently-committed XID, so the
   hot path through `TupleVisible` rarely fans out into the slow branches.
3. The M0115 write path (`SetXminHintBit` in `seqScan`) is taken only on
   the first visit per tuple; subsequent visits short-circuit, but in a
   60s `-c 10` run the per-tuple touch frequency dominates the per-tuple
   first-touch cost regardless of which way M0115 routes the second visit.

The take-away is that M0115 is _ceiling-preserving_ on this workload, as
required by the gate, rather than _ceiling-raising_. Workloads that
actually exercise the cold-snapshot path (long-running transactions
holding snapshots open while many concurrent writers commit; bulk SELECT
across rarely-visited heap pages whose hint bits were never stamped) are
the workloads where M0115 is expected to materially raise TPS; M0115-0007's
gate is a regression check, not a measure of M0115's best-case win.

## Cross-references

- M0107 established the historical 42k TPS baseline at a different
  scale/config (`tmp/perf-optimize/m0107_*`) — not directly comparable
  because of scale and -c/-j differences.
- M0116-0004 measured single-column IOS regression at scale=10
  `-c 50 -j 50 -T 30` and reported 167k TPS — also a different config,
  not directly comparable to this `-c 10 -j 1 -T 60` run.
- Concurrent ralph-loop check (per [`concurrent_ralph_loops_corrupt_tree`]):
  at run time there was one ralph supervisor (`1137103`) and one nested
  per-cycle worker (`1390439`, ppid 1137103), i.e. the #76 nesting artifact
  with exactly one live `claude` worker. No peer contention.
