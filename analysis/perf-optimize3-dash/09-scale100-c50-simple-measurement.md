# goopg pgbench measurement — scale 100, c=50, `-M simple`

Read-path (`-S`, select-only) and write-path (`-N`, simple-update) throughput and
latency of the current goopg build. goopg only (no PostgreSQL comparison); no
profiling. Raw artifacts:
`analysis/perf-optimize3/runs/scale100_c50_simple_28dad6df/`.

## Provenance

| field | value |
|---|---|
| commit | `28dad6df` (branch `wal-system-pgnize`; net-identical to `635cc590` — the doc-03 S1 change was reverted) |
| date | 2026-07-14 |
| go / kernel | go1.26.3 · 6.18.33.2-microsoft-standard-WSL2 · nproc 16 |
| GOMEMLIMIT | 18GiB |
| pgbench | 18.3 |
| scale factor | 100 (`pgbench -i -s 100`; load 38 s) |
| clients / threads | 50 / 50 |
| duration | 120 s per workload |
| query mode | `-M simple` |
| server restart | fresh restart before each of `-S` and `-N` |

### goopg `postgresql.conf` (same as prior runs)

```
max_connections = 200
shared_buffers = 2560MB
wal_buffers = 134217728
checkpoint_timeout = 24h
max_wal_size = 1024GB
min_wal_size = 1024MB
checkpoint_completion_target = 0.9
```

## Results

| workload | TPS | mean latency | latency stddev | txns processed | failed |
|---|---:|---:|---:|---:|---:|
| `-S` select-only | **138,696.8** | 0.360 ms | 0.475 ms | 16,643,091 | 0 (0.000%) |
| `-N` simple-update | **10,830.9** | 4.616 ms | 6.404 ms | 1,299,695 | 0 (0.000%) |

TPS is "without initial connection time" (pgbench summary line).

### Per-interval progress (`-P 30`)

**`-S`**

| elapsed | TPS | latency | failed |
|---:|---:|---:|---:|
| 30 s | 136,527.3 | 0.365 ms | 0 |
| 60 s | 140,802.9 | 0.354 ms | 0 |
| 90 s | 140,794.9 | 0.354 ms | 0 |

**`-N`**

| elapsed | TPS | latency | failed |
|---:|---:|---:|---:|
| 30 s | 5,756.5 | 8.684 ms | 0 |
| 60 s | 12,430.3 | 4.022 ms | 0 |
| 90 s | 12,377.7 | 4.039 ms | 0 |

(The `-N` first interval reflects warm-up; steady-state is ~12,400 TPS.)

### Relation sizes around the `-N` run (bytes)

| relation | before | after | delta |
|---|---:|---:|---:|
| pgbench_accounts | 442,818,560 | 449,847,296 | +7,028,736 |
| pgbench_accounts_pkey | 166,526,976 | 333,357,056 | +166,830,080 |
| pgbench_history | 0 | 67,813,376 | +67,813,376 |
| pgbench_branches | 8,192 | 8,192 | 0 |
| pgbench_branches_pkey | 16,384 | 16,384 | 0 |
| pgbench_tellers | 49,152 | 49,152 | 0 |
| pgbench_tellers_pkey | 40,960 | 40,960 | 0 |
