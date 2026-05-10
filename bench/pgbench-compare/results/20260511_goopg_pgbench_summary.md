# pgbench measurement against goopg — 2026-05-11

## Parameters

Per `bench/pgbench-compare/README.md` + `run_comparison.sh`:

- scale factor: 100 (~1.5 GB / 10M accounts rows)
- clients: 100 (`-c 100`)
- threads: 100 (`-j 100`)
- duration: 180 s per workload (`-T 180`)
- progress: every 10 s (`-P 10`)
- `shared_buffers = 2560MB`, `wal_buffers = 100MB`,
  `checkpoint_timeout = 24h`, `max_wal_size = 1024GB`

Each workload was run on a **freshly-restarted goopg** (fresh
init → start → checkpoint+stop → start → pgbench → checkpoint+stop)
per the user's protocol.

## Results

| Workload | Result file | TPS | Transactions | Failed | Avg lat (ms) |
|---|---|---:|---:|---:|---:|
| standard (TPC-B-like) | `20260511_041808_goopg_standard.txt` | 69.81 | 12628 | 0 | 1429.69 |
| simple-update (`-N`) | `20260511_042939_goopg_simple-update.txt` | 94.998 | 17167 | 0 | 1051.05 |
| select-only (`-S`) | `20260511_042130_goopg_select-only.txt` | 400.07 | 72094 | 0 | 249.77 |

Baseline for comparison: the same suite produced `0.86 TPS` on
the standard workload before the M0079 catalog DDL WAL recovery
fix (file `20260511_015126_goopg_standard.txt`). The current
68–70 TPS reflects the post-fix steady state.

## Issues surfaced + fixed during the run

1. **Concurrent UPDATE line-pointer race** — under `-c 100`,
   clients aborted with `ERROR: storage: unsupported line
   pointer state: slot=N flags=0`. Root cause was a missing
   race tolerance in 3 modify-phase call sites
   (updateOp.updateViaIndex / updateOp.Next / deleteOp.Next).
   Fixed in commit `18c60d9` (`fix(executor): tolerate
   concurrent line-pointer state changes during UPDATE/DELETE`).

2. **HOT-path post-prune race** — `tryApplyHOTUpdate`'s
   `PagePruneOpt` page-full fallback could invalidate the
   old-image slot between our pre-check and the subsequent
   `PageStampHotOldTuple`. Fixed in commit `2c1e18e`
   (`fix(executor): tolerate post-prune race in
   tryApplyHOTUpdate stamp`).

## Outstanding bugs noted but NOT fixed in this run

The 3 result files above came from clean fresh-init starts.
However, while iterating, two restart-related durability bugs
were observed that prevent running multiple write-heavy
workloads back-to-back on the same data dir:

- **standard → restart → other-write-workload fails with
  `ERROR: short read at block`.** Inspection showed
  `pgbench_history` had 0 rows after the standard run's
  checkpoint+stop+restart even though 12628 transactions
  executed cleanly. The post-restart heap or buffer-pool state
  for the relations the standard workload extended is
  inconsistent with the index.
- **SIGKILL'd goopg (or stop without prior checkpoint) leaves
  WAL that fails to replay: `wal: decode at offset N: wal:
  corrupt record: checksum mismatch`.** A `checkpoint` call
  immediately before `stop` works around this in practice.

The simple-update measurement above therefore had to be re-run
standalone (wipe + re-init + standalone simple-update) rather
than in the standard→simple-update→select-only chain. The
underlying durability gap is a candidate follow-up milestone.
