# 05 — Runtime Trace

Source: `profiles/goopg_c<C>_<wl>.trace.out.gz` (gzipped after capture; 30 s windows starting at T+30). Open with:

```bash
gunzip -k profiles/goopg_c10_select-only.trace.out.gz
go tool trace profiles/goopg_c10_select-only.trace.out
```

`go tool trace` is interactive (browser-based) so this chapter records the findings I drew from it rather than reproducing the visualisations. The raw files are preserved for reviewers to re-examine.

## §5.1 Goroutine population

| pattern | total goroutines (trace) | matches §04 goroutine snapshot? |
|---|---:|---|
| c=10 all workloads | ~ 80–110 | yes |
| c=50 all workloads | ~ 200–300 | yes |
| c=100 SO            | ~ 400      | yes |
| c=100 SU (deadlock) | ~ 110 (stalled) | matches the snapshot's 105 + 19 + 4 + 1 |

goopg's goroutine-per-connection model + WAL writer + bgwriter + AIO workers + GC workers + miscellaneous internal goroutines. No goroutine leak across runs (the c=10 → c=50 → c=100 progression shows linear growth then a clean teardown on the restart between client-count blocks).

## §5.2 Threadcreate

`profiles/goopg_*.threadcreate.txt` shows the count of OS threads (Go runtime `M`s) — kept short by Go's M:N scheduler. Under load the M count stayed at ~ 32 across all patterns (= `GOMAXPROCS=16` + spare Ms for blocking syscalls). No thread explosion despite 100 active backend goroutines.

PG, by contrast, has 100 *processes* under c=100 — orders of magnitude more kernel-side state. That trade-off works in PG's favour because each process has its own address space (no shared GC; no shared lock manager critical section through which all snapshots must funnel).

## §5.3 GC stop-the-world windows

For c=10 select-only the trace shows STW pauses of ~ 0.4–1.2 ms every ~ 1.5 s (at GC frequency ≈ 0.7 Hz given `GOGC=200` and ~5 GB heap turnover). At 2 307 TPS × 1.0 ms STW = ~ 2 300 transactions worth of stop per GC cycle, equating to ~ 1.5 % TPS lost to STW alone. The bigger cost is the concurrent mark phase (§2.1 — 60 % of CPU) running on dedicated GC workers.

For c=50/c=100 the STW pauses get longer (1.5–3 ms) because the live heap grows. Not a primary bottleneck — concurrent mark CPU is — but visible in tail latency: §1 reported latency stddev of 47.8 ms on c=50 simple-update; trace shows individual GC cycles can stall the system by ~10 ms when combined with their assist phase.

## §5.4 Scheduler latency

Trace's "scheduler latency" metric shows how long goroutines wait between becoming runnable and actually running. Under c=10 it's negligible (< 100 µs). Under c=100 SO it grows to 0.5–2 ms — the 100 backend goroutines + 4 GC workers + 4 dedicated GC mark workers compete for 16 logical Ps.

This is consistent with §2.3's `runtime.futex` cost: every mutex/condvar wakeup costs a futex syscall, and at c=100 with deep mutex queues these wakeups dominate scheduler activity.

## §5.5 Syscall blocking

Trace shows the WAL writer's `Fdatasync` syscalls as ~ 200–500 µs per call, batched by M0098-0002 group commit. At c=10 simple-update with 410 TPS, that's ~ 410 fdatasyncs/s (one per commit since most transactions commit individually at c=10). At c=50 simple-update with 347 TPS, group commit kicks in and the rate stays similar but the *batch size* per fdatasync grows.

This is consistent with WAL behaviour and PG's group commit. **WAL is not the OLTP bottleneck on this hardware** — the on-host SSD (or WSL2's filesystem-backed virtual disk) absorbs the write load. The mvcc/activity/bufpool contention upstream of WAL is what gates writes.

## §5.6 P (logical processor) utilisation

Across all patterns the trace shows < 4 logical Ps active on average out of `GOMAXPROCS=16`. P utilisation looks like:

```
P0  ████████████████░░░░░░░░░░░░░░░░       backend goroutines (round-robin)
P1  ████████████░░░░░░░░░░░░░░░░░░░░       backend goroutines
P2  ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░       idle
P3  ████████░░░░░░░░░░░░░░░░░░░░░░░░       GC mark worker
P4  ████░░░░░░░░░░░░░░░░░░░░░░░░░░░░       walwriter / bgwriter
P5-P15  mostly idle
```

13 of 16 Ps idle while 100 clients queue is the visible-from-trace consequence of the single-mutex-contention story documented in §04. There is no thread-pool starvation; there is mutex starvation.

## §5.7 What I'd look for next time

- **Per-goroutine spans** for `mvcc.Manager.Commit` would let us see directly how long each transaction spends in the critical section. (Requires adding `trace.WithRegion` annotations around the hot paths — a small instrumentation change.)
- **`/debug/pprof/trace` over the full 180 s window** rather than 30 s would catch GC-amortised tail latency that the 30 s sample under-represents.
- **Stack samples** during the deadlock window — captured as `deadlock_goroutine.txt` for c=100 simple-update and c=100 standard; they're the smoking gun for §4.4.

## §5.8 Trace files are big

| pattern | uncompressed | gzipped | open with |
|---|---:|---:|---|
| c=100 SO     | 78.9 MB | 52.4 MB | `gunzip -k && go tool trace …` |
| c=50  SO     | 64.6 MB | 42.7 MB | same |
| c=10  SO     | 41.8 MB | 26.7 MB | same |
| c=100 SU     | 35.1 MB | 21.0 MB | same |
| c=100 std    | 0.6 MB  | 0.1 MB  | (killed early by watchdog) |
| ... (others) | ~ 40 MB | ~ 25 MB | same |

The 100-client SO trace at 79 MB / 30 s sample is dense — `go tool trace` may take 60–90 s to load.
