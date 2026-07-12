# 05 — Migration and verification

status: draft · date: 2026-07-12

## 5.1 Rollback story (decided before the slices)

**Slice-by-slice replacement in single revertable commits; no runtime feature
flag.** A runtime toggle would keep two concurrency protocols alive against the
same `files`/`dirty`/ring state — strictly more dangerous than either protocol
alone. Cheap insurance instead: the old `handleGroupFlush` path stays
compilable behind a package-level compile-time `const backendFlush = true`
(test-settable via build tag if needed) from slice 3 until its deletion in
slice 6. Two rules keep the const safe (m10): the `writeMu` wraps on
drain-coupled sites are **unconditional** (never gated by the const — flipping
the const back after slice 4 must not resurrect the unsynchronized
`files`/`dirty` state against the new walwriter), and the const gates **all**
new-path callers (FlushUpTo, BackgroundWrite, overflow drain) or none. Every
slice passes its full gate before the next begins; a red gate reverts exactly
one commit.

## 5.2 The seven slices (foundation-first)

| # | slice | content | verification gate |
|---|---|---|---|
| 1 | `walWriteLock` primitive + `publishTail` CAS-max | new lock file + tri-state unit tests; missed-wakeup stress (N acquirers × M generations, assert no waiter sleeps past a release); close-wakeup test. **Also: convert `walBuffer.publishTail` from Load-then-Store to a CAS-max loop** (adversarial-review M2 — hard precondition of slice 3; the Load-then-Store form corrupts the ring under multi-caller `PublishUpTo`) + a concurrent-publishers regression test | `go test -race ./internal/wal/` |
| 2 | `xlogWrite(writeRqst)` extraction | refactor `flushUpTo` → `xlogWrite` with the write/flush split, finishing-segment fsync, `drainedLSNAtomic` mirror. Still called only by the loop with `write==flush` ⇒ **behavior-identical slice** | wal suite; kill-9 crash-recovery harness; `pg_waldump` compat check |
| 3 | **backend-driven `FlushUpTo`** (the core swap) | new FlushUpTo (03 §3.3) with the panic-safe holder scope (deferred release + re-panic), sticky error epoch, closed-under-lock recheck, and the legacy-mode frontier branch; `flushGroup` retired behind the const; `commit_delay`/`commit_siblings` GUCs (default **0**/5, sample-file sync) replacing the hardcoded consts; `waitInsertionsToFinish`; **flusher takes `writeMu` only (never `appendMu`)**; all loop-side and slow-path drain-coupled sites (`drainBufferBytes`/`writeAt`/`walBuf.reset`/`drainedLSN`/`resetPosition`) wrapped in `writeMu` nested inside their existing `appendMu.Lock` (lock order `appendMu`→`writeMu`); `xlogWrite` target validation switched to the published tail | race suite; **full isolation suite**; kill-9; pgbench c∈{1,8,50} p50/p99/TPS vs HEAD baseline; group-commit tests rewritten (fsyncCount < committer count); `TestFlushUpToPreEnqueueFastExit` unchanged-green; holder-panic test (inject panic in xlogWrite → lock released, next commit proceeds) |
| 4 | walwriter policy | `BackgroundWrite()` (03 §3.4); wire the **existing, currently-inert** `wal_writer_flush_after` GUC (already registered, BootVal 1 MiB, already in the sample file — only the behavioral reader is new); open.go ticker swap; pg_stat_activity walwriter row moves to the ticker goroutine | async-commit durability test (clog barrier under `synchronous_commit=off` + kill-9); pgbench sync-off; standby attach + streaming |
| 5 | slow-path ops off the loop | `Append`/`AppendRaw` slow paths in caller context; `opRecycle` → direct call under `writeMu`; `opWALBufStat` → atomics; delete dead `opFlush` | race suite; walreceiver/standby tests; checkpointer retention tests |
| 6 | delete `state.loop` | direct `Close()`; closed/done semantics; drop `LockOSThread`; delete `flushGroup` remnants + the const; **flip supersession statuses** on 0098-0002 / 0099-0002 / wal_fsync_flow_primary / 0107-0007ai+ah notes | testport subset (`TestPort_`), shutdown/restart tests, goroutine-leak check |
| 7 | drain-safety certification | (re-scoped by adversarial review: the flusher is `writeMu`-only from slice 3, so there is no `appendMu` to drop) exhaustive audit that every `walBuf.reset`/`resetPosition`/head-mutation site holds `writeMu`; supersession text finalized in 0107-0007ai; heavy race-stress harness (concurrent append+flush+recycle+close loops) added as a standing test | heavy race stress; kill-9; pgbench re-measure |

Slice 7 is deliberately last: it certifies, under maximum stress, the
corollary-1 coverage (04 §4.4) that slices 3–6 established site by site.

## 5.3 Gates common to every slice

- `go test -race ./internal/wal/ ./internal/mvcc/` (concurrency-critical
  packages — WAL/MVCC practice card).
- Kill-9 crash-recovery e2e (commit → SIGKILL → restart → visible; abort path
  unchanged) — the recovery suites in `internal/initdb` + `internal/wal`.
- **No new `pg_waldump` diffs** and the PG-standby-attach E2E where relevant —
  on-disk format is frozen (0107-0001).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` + the
  pre-commit pgbench smoke (never `--no-verify`); `scripts/tpch-spotcheck.sh`
  after slices that touch the DML-reachable path (3, 5, 6, 7).

## 5.4 Performance acceptance (slices 3, 4, 7)

Reuse `analysis/perf-optimize2/scripts/bench_goopg_su50.sh` +
`run_su50.sh` conditions (uncapped, GOMEMLIMIT=18GiB, scale 100):

- pgbench `-N` at c∈{1, 8, 50}: TPS **and latency distribution** (`-r` /
  progress stddev; p99 is the headline metric for this redesign).
- fsyncCount/TPS ratio (batch width) must not regress vs the ≈8.9 baseline at
  c=50.
- sync-off run as the CPU-path control.
- Record per-slice results in `analysis/` with commit hashes
  (measure-then-attribute discipline).

## 5.5 Risk register

| risk | mitigation |
|---|---|
| deadlock via lock-order violation (`writeMu` → `appendMu`) | single documented order (04 §4.2: flusher/walwriter `writeMu`-only; slow paths nest `writeMu` inside `appendMu.Lock`) + debug assertion; the drain-under-`appendMu` variant was explicitly rejected (ABBA — 00 D4); race-stress gate |
| `publishTail` Load-then-Store race under multi-caller `PublishUpTo` → ring-space double-grant (corruption) | CAS-max conversion in slice 1 (hard precondition) + concurrent-publishers regression test (04 §4.3) |
| holder panic leaks `writeMu` → permanent commit hang on a live server | panic-safe holder scope (deferred release + re-panic, 04 §4.7) + injected-panic test (slice 3) |
| legacy (non-pageHeaders) mode spins forever in `waitInsertionsToFinish` | legacy frontier branch (04 §4.3) + a legacy-mode commit test |
| missed wakeup in `walWriteLock` | proof in 03 §3.1 + dedicated stress test (slice 1); shutdown arm covers close |
| lost durability: `flushedLSNAtomic` published early | publication rule (04 §4.5) + a test that faults injection-delays `doSync` and asserts the fast exit can't pass |
| walwriter write-only mode leaves a long unsynced tail | finishing-segment unconditional fsync (03 §3.2) + `wal_writer_flush_after`/delay-elapsed policy; async-commit kill-9 gate (slice 4) |
| `files`/`dirty` race during migration (dual paths in slices 3–5) | every I/O site wrapped in `writeMu` in the SAME slice that adds a new caller; race suite per slice |
| starvation of a particular committer under barging | bounded-generations coverage argument (03 §3.1); p99 measurements at c=50 as the empirical check |
| M0072-style hang from a too-big change | the whole point of the 7-slice staging; each slice independently landable and revertable |

## 5.6 Deferral discipline

Anything cut from a slice (e.g. per-stripe `sync.Cond` in
`waitInsertionsToFinish`, FD-close-on-segment-switch, walwriter hibernation,
holder-only fsync-time attribution) gets a `.ralph/deferral_ledger.md` row with
a resume point — never a silent forward reference.
