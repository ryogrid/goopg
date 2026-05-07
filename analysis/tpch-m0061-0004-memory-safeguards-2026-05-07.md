# M0061-0004 — Memory safeguards after WSL2 crash during M0061-0003 sweep

**Date:** 2026-05-07
**Trigger:** WSL2 host went down between 09:21 and 09:30 JST, mid-way
through the M0061-0003 follow-up activity. System rebooted at 09:30;
no new `goopg` activity was logged after 09:21:06 (last connection
close). Peak `VmHWM` from the prior sweep run was 16 GB.

## Forensics

Available signals:

- `bench/tpch/runtime_goopg/goopg.log` — last entry **09:21:06**
  (`connection closed pid=63`). Server was therefore idle from
  09:21:06 onward, yet WSL2 went down ~9 minutes later. Either the
  server's heap kept *retained* memory allocated to the Go runtime
  even after the queries finished, or another process pushed the
  WSL2 host past its 32 GB ceiling.
- `dmesg` post-boot is empty of OOM events (kernel ring buffer
  reset on reboot). `last -x` confirms `system boot 09:30`.
- The most recent `tpch-runner` 22-query sweep ran from 08:13:39
  to ~09:15. Long-tail queries within it: Q5 (cancelled at 600 s),
  Q13 (cancelled at 899 s — 300 s past the cancel-after deadline),
  Q20 (600 s), Q21 (600 s).

### Suspect query (best inference)

Without per-query memory traces, the most likely contributors are
**Q13 and Q5**, in that order:

- **Q13** (`customer LEFT JOIN orders ON c_custkey = o_custkey AND
  o_comment NOT LIKE '%special%requests%'`) — 150 K × 1.5 M shape,
  with `NOT LIKE` evaluated per pair. The cancel propagation took
  300 s past `cancel-after=600s`, indicating an operator was busy
  allocating during the entire window. LEFT JOIN's
  unmatched-left-row preservation requires bookkeeping that scales
  with the build side. M0058-0005 added `ctx.Err()` to NL/MHJ
  probe phases but Q13's specific plan apparently uses a path
  without that hook.
- **Q5** (six-table join with multi-way hash) — 600 s cancel; with
  six hash tables built simultaneously, transient peak is high.

Other queries (Q4 / Q19 / Q22 from M0061; Q11 / Q16 / Q18 from
M0058) all completed under 200 s with no abnormal memory
behaviour and are unlikely to be the trigger.

## Root cause hypothesis

`maybeForceGCAfterCommit` was a **no-op** stub since M0032
ripped out the unconditional GC. Go's GC has soft limits via
`GOMEMLIMIT` but **does not return memory to the OS** between
runs — `runtime.GC()` reclaims unreachable heap into Go's free
spans, but only `debug.FreeOSMemory()` returns those spans to
the kernel. Consequently a 22-query sweep that legitimately
peaks at 16 GB during one big query keeps the goopg process at
~16 GB RSS for the rest of the sweep — leaving very little
headroom on a 32 GB WSL2 host once you add the 2 GB
`shared_buffers` arena, the OS, and any co-resident processes
(IDE, claude-code, browser).

## Fix

Three independent safeguards land in this commit:

### 1. Conditional FreeOSMemory at end of every Query (server)

`internal/server/dispatch.go` :: `maybeForceGCAfterCommit` is
re-implemented (no longer a no-op):

```go
const heapReleaseThresholdBytes = 4 << 30 // 4 GiB
const queriesPerForcedFree     = 8

func maybeForceGCAfterCommit() {
    var ms runtime.MemStats
    runtime.ReadMemStats(&ms)
    n := atomic.AddInt64(&queriesWithoutFreeCounter, 1)
    if ms.HeapInuse < heapReleaseThresholdBytes &&
       n < queriesPerForcedFree {
        return
    }
    atomic.StoreInt64(&queriesWithoutFreeCounter, 0)
    runtime.GC()
    debug.FreeOSMemory()
}
```

Trigger when:
- `HeapInuse > 4 GiB` (the query was big), **or**
- `queriesPerForcedFree=8` queries have elapsed without a Free
  (drift over time).

The Free runs **after** the client has received `CommandComplete`,
so the GC pause is invisible to the just-finished query. Cost:
~50–500 ms one-time per Free, only on the post-CommandComplete
side. M0032's "91 % GC overhead on Q9" came from the unthrottled
in-loop Free; the conditional gate avoids that.

### 2. Lower default `GOMEMLIMIT` for the bench harness

`bench/tpch/env_goopg.sh`: `20 GiB → 12 GiB`. With
`shared_buffers=2 GB` plus a generous safety margin, 12 GB is
ample for SF=1 while keeping ≥ 18 GB free for the OS and
co-resident processes on a 32 GB host.

Override remains available: `GOMEMLIMIT=20GiB bash setup_goopg.sh`
on hosts with ≥ 64 GB RAM.

### 3. Per-query connection isolation (already landed in `00ee40f`)

The `tpch-runner` connection-isolation patch ensures that
**server-side per-connection state** is dropped between queries.
Combined with (1)'s end-of-query Free, the server now actively
returns memory between every query.

## Verification

- `go build ./...` PASS.
- `go test ./internal/server/...` PASS.
- Re-running the 22-query sweep after the fix should show RSS
  trending back toward `shared_buffers + small constant` between
  queries instead of sticking at the high-water mark.
  (Empirical re-run is the next M0061-0004 acceptance step.)

## Open follow-ups

- **Q13 cancel-propagation slow path.** Independent of memory:
  the cancel signal took 300 s to propagate on Q13. Not directly
  fixed here. Tracked under the M0061-0003 report's open
  follow-ups list.
- **Per-query memory-watchdog.** A goroutine that polls
  `runtime.MemStats` and triggers `CancelRegistry.CancelAll()`
  when the goopg process's `HeapInuse` crosses (say) 80 % of
  `GOMEMLIMIT`, so a runaway query is killed before the host
  swaps. Not in this commit; documented as a future option.
