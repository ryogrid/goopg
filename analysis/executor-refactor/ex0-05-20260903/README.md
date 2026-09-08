# EX0-05 witness record — Q9 batch/width counters, both arms

```
label: EX0-05-q9-batches | date: 2026-09-03 (22:44–22:47 JST)
goopg: 6dd26f85f +dirty (foreign WIP; binary tmp/goopg-ex002 from tree)
suite: TPC-H SF=1 Q9 only | regime: stats=S-cold fresh server per arm,
  parallel=suite-default (4) + serial control (max_parallel_workers_per_gather=0)
GOGC/GOMEMLIMIT: 100/12GiB | cgroup scope ex005 20G/24G/0
work_mem: 64MB (SHOW; cluster default via P0-12 setup_goopg.sh:71),
  effective_cache_size: 2GB | port 65433 tpch@tpch
```

## Batch lines (EXPLAIN ANALYZE, TIMING OFF)

Parallel arm — 4 hash builds, all report:
- width=1096 join: `Buckets: 16384  Batches: 1  Memory Usage: 0kB`
- **witness** width=896 join (orders build): `Buckets: 262144
  Batches: 2  Memory Usage: 91586kB  Build Time: 26460.671 ms`
- width=710 join (lineitem-part): `Buckets: 8192  Batches: 1
  Memory Usage: 1587kB`
- width=720 join (supplier-nation): `Buckets: 1024  Batches: 1
  Memory Usage: 4kB`

Serial arm — same 4 builds, same Batches (1/2/1/1); witness
`Memory Usage: 91586kB  Build Time: 10734.609 ms`. One non-witness
build (partsupp-level, width=896, 800k rows, 9.3 s) renders
`Build Time:` ONLY — lazy-map path publishes no geometry (ledgered
`take3-EX0-lazy-hash-geometry`, not the witness, gate unaffected).

## BEFORE record for the P4-01 exit

Witness: **Batches: 2** (not take2's 8 — re-confirmed shape on HEAD;
the 8 predates current sizing; BEFORE is what HEAD produces).
Widths: 1096/896/896/710/720 — the ≈100 (not 6) narrowing is the
POST-P4-01 expectation, not today's value. No width≈6 degenerate
present.

## Pin

plan-gate 2026-09-03: 22/22 MATCH, changed=0. `git diff --stat` on the
close commit: docs/analysis-only (no `internal/…`).
