# 06 — Observability fixes (appendix)

status: design appendix · date: 2026-07-13 · base: `e453e3f2` · recommend
landing **early**, alongside C1-S1/C2-S1 (README X5) — each item below is the
cheap way to verify a main candidate's effect without strace/pprof gymnastics.

Every item was a measurement gap the perf-optimize3 analysis had to work
around (`../01-results.md` measurement notes).

## O1 — Wire `pg_stat_wal` (serves C1 + C2 verification)

Today: the view is a **zero stub** — `VirtualRows` returns a hardcoded all-zero
row (`internal/catalog/catalog.go:8508-8530`).

Counters that already exist and only need plumbing to the view:

| column | source |
|---|---|
| `wal_bytes` | `pg_stat_wal_io`'s drain counters (`wal_buffers_flush_drain_bytes` + `overflow_drain_bytes`, wired from `walBufferCounters`) — or better, the writer's published append frontier delta |
| `wal_sync` | `walBufferCounters.fsyncCount` (`internal/wal/writer.go` — counted at every `doSync`) |
| `wal_write` | drain (`pwrite`) count — counter exists adjacent to fsyncCount |
| `wal_fpi` | new cheap counter at the FPI emit sites (`Pool.maybeEmitFPI` / `MarkDirty*` first-touch branch, `internal/storage/bufpool.go`; plus canonical image-bearing emits once C1-S2b unifies them). **Caveat**: until C1-S8/D4 resolves the first-touch duplication this counts native + canonical images (≈2× PG per first-touched page) — compare as a trend, not a PG-parity number |
| `wal_records` | increment in `Writer.Append`/stripe append |

Implementation shape: replace the static `VirtualRows` closure with one that
reads the live `wal.Writer` counters via the runtime (the same pattern
`pg_stat_wal_io` uses in `internal/initdb/wal_io_views.go`). PG 18 note:
`wal_write`/`wal_sync` columns were removed from upstream's `pg_stat_wal`
(moved to `pg_stat_io`) — decide whether to mirror PG 18's schema exactly
(preferred for pg_dump/psql parity; put sync counts in `pg_stat_io`
object='wal') or keep the legacy columns. Verify against
`postgres/local_install` psql `\d pg_stat_wal`.

Test: unit test asserting counters advance across an Append+FlushUpTo;
`TestSampleConfigCoversRegistry`-style schema parity check vs PG 18.

## O2 — `pg_current_wal_lsn()` runtime handler (serves C1 verification)

Today: seeded in `pg_proc` (`internal/initdb/pg_proc_seed_data.go:1849`,
OID 2849, HandlerName set) but **no executor handler exists** → runtime
`ERROR: function pg_current_wal_lsn does not exist`. Implement the builtin to
return the writer's published append frontier (`Writer.WrittenLSN()`)
formatted as `pg_lsn` (`pg_wal_lsn_diff`, OID 3165, same treatment if also
unwired). This makes WAL-bytes/txn measurable identically on both engines
(the aux2 probe had to fall back to drain-bytes deltas).

Test: `SELECT pg_current_wal_lsn()` monotonicity across writes; diff via
`pg_wal_lsn_diff` matches drain-byte delta within a page-padding tolerance.

## O3 — Wait events for the backend WAL flush (serves C2/C5 verification)

Today: 28,425 wait-event samples during `-N` were all empty — the
backend-flush wait is invisible to `pg_stat_activity`. Hooks already exist:
`OnWALSync`/`OnWALSyncDone` on the Writer (fired around FlushUpTo) and the
activity registry's `WaitEventStart/End` (used by `OnWALWrite` wiring in
`internal/initdb/open.go:402-406`). Wire:

- `LWLock:WALWrite` around `flushUpToBackend`'s `acquireOrWait` park,
- `IO:WalSync` around `xlogWrite`'s Stage-2 fsync (holder only),
- (with C2) an SLRU wait event around any remaining CLOG write-back.

Backends need their procNum reachable from the flush path — `gls.BackendID`
already provides it allocation-free (perf-optimize2 fix-01).

Test: sample `pg_stat_activity` during a loaded `-N` burst in an e2e test;
assert non-empty WALWrite/WalSync distribution (bounded, not exact).

## O4 — `n_tup_upd` / `n_tup_hot_upd` counters (serves C3 + HOT verification)

Today: `pg_stat_user_tables` rows exist but the counters are always 0 — the
HOT-rate comparison in `../01-results.md` had to be inferred from file sizes.
Increment at the update apply sites: `tryApplyHOTUpdate` success →
`n_tup_hot_upd`+`n_tup_upd`; non-HOT update apply → `n_tup_upd` (also
`n_tup_ins`/`n_tup_del` at their sites while there). Storage: the
per-table stats plumbing that ANALYZE/`pg_stat_user_tables` reads —
`internal/stats/counter.go` + `internal/executor/pgstat_tables.go` back the
view.

Test: N updates on an unindexed column with page space → `n_tup_hot_upd`≈N;
after forcing page-full non-HOT updates → ratio drops. Cross-check against PG
on the same script.

## Sizing

All four are small, independent, behavior-safe (read-only counters + one new
builtin). Estimated one slice total, or fold O1+O2 with C1-S1 and O3 with
C2-S1.
