# 05 — Expected performance

status: design · date: 2026-07-13 · base: `e453e3f2` · slice: S5
(measurement) · gates: G-perf

## 1. Volume model

Measured today (perf-optimize3, run 20260713_004324): **33,004 B WAL/txn** on
pgbench `-N` (scale 100, c=50). Decomposition ≈ four 8.24 KB page images +
small records:

| component | B/txn today | native-only |
|---|---:|---:|
| canonical `XLOG_HEAP_INPLACE` image (accounts) | ~8,240 | **0** |
| canonical `XLOG_HEAP_INSERT` image (history) | ~8,240 | **0** |
| native first-touch FPIs (workload-dependent, see §2) | ~16,400 | ~16,400 |
| native logical records (141+85+5) + framing | ~230 | ~230 |
| canonical commit record | ~50 | 0 |
| **total** | **~33,100 ≈ measured 33,004** | **~16,600** |

**Immediate effect of the switch: ≈ −50 % WAL volume** (33.0 → ~16.5 KB/txn)
at the benchmark's touch rate, with zero record-format changes.

## 2. Why the residual converges to PG's number (amortization math)

The residual is dominated by native first-touch FPIs — the **same mechanism
PG's own 1.8–2.9 KB/txn is made of**. PG's measured numbers ARE mostly
amortized first-touch images:

| run | txns | accounts pages | touches/page | amortized FPI/txn | + records ≈ predicted | measured |
|---|---:|---:|---:|---:|---:|---:|
| PG aux1 | 935 k | ~167 k | 5.6 | 8.2 KB/5.6 ≈ 1.46 KB | ~1.7 KB | **1.80 KB** |
| PG aux2 | 532 k | ~167 k | 3.2 | ≈ 2.6 KB | ~2.8 KB | **2.85 KB** |
| goopg aux2 | 75 k | ~54 k | 1.4 | ≈ 5.9 KB (never amortizes in 60 s) | — | (33 KB incl. canonical) |

goopg's short AUX runs sat in the worst-case regime (almost every touch is a
page's first). Two structural facts favor goopg's native-only steady state:

- **goopg's heap is ~3× denser** (accounts 445 MB / ~54 k pages vs PG's
  1.37 GB / ~167 k pages) → at equal txn counts goopg reaches a given
  touches/page ratio 3× sooner, i.e. **amortizes faster than PG**.
- The steady-state logical residual is **~231 B/txn** (HotUpdate 141 +
  HeapInsert 85 + XactCommit 5) — comparable to PG's per-record bytes.

Predicted native-only bytes/txn at PG-aux1-equivalent touch rates
(5.6 touches/page): 8.2 KB/5.6 + 0.23 ≈ **1.7 KB/txn — at or below PG's
measured 1.8 KB**. The design therefore states: −50 % immediately,
**PG-parity (or better) with run length**, no further mechanism needed —
the same checkpoint-interval amortization PG itself relies on.

## 3. Latency / throughput expectations

- END (commit flush) improves through smaller drains per group-commit cycle
  (~width × 16.5 KB instead of ×33 KB) and faster statements (the 8 KB page
  copy + canonical encode disappear from UPDATE/INSERT inline cost — that
  assembly was ~75 % of `memmove`, 11.3 % of `-N` CPU). The per-call fsync
  floor is untouched (that is C2's territory, unaffected by this bundle).
- Statement latencies: UPDATE 0.797 ms and INSERT 0.899 ms should move
  substantially toward PG's 0.155/0.132 ms; not all the way (M4 engine tax
  remains).
- No read-path change expected (`-S` untouched); assert no regression.

## 4. Measurement protocol (S5)

1. `analysis/perf-optimize3/scripts/run_rw50.sh` — both modes
   (`GOOPG_WAL_CANONICAL=on` vs off), fresh init each, record commit hash;
   compare `-S` (must be neutral) and `-N` (TPS/latency + per-statement `-r`).
2. `aux2_fsync_probe.sh` — WAL bytes/txn via the `pg_stat_wal_io` drain
   deltas (or `pg_current_wal_lsn()` if 06-appendix O2 has landed): expect
   ~16.5 KB off-mode at 60 s scale, and a longer run (`DURATION=600`) to
   demonstrate the §2 convergence curve.
3. Record results in `analysis/` with commit hashes (measure-then-attribute
   discipline); update this doc's model with actuals.

## 5. Observability note

The 06-appendix items from perf-optimize3/05 (notably `pg_stat_wal.wal_bytes`
/ `wal_fpi` wiring) make the §2 curve visible in production; under
native-only the `wal_fpi` double-count caveat recorded there disappears (only
native images remain — the counter becomes PG-comparable immediately).
