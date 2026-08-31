# 0117-0009 — Batched CLOG→SLRU backfill on mirror enable (startup fsync storm)

**Milestone:** M0117 (CLOG ↔ PostgreSQL subsystem alignment)
**Status:** accepted
**Type:** enabler / startup-performance fix (NOT a parity-behaviour change)

## Problem

`scripts/tpch-spotcheck.sh` — the mandated executor/planner pre-commit gate —
**infra-failed** on the populated 6M-row bench dataset
(`bench/tpch/runtime_goopg/data`, ~1.5M committed/aborted XIDs) on the WSL2
development box: the server never reached the script's 60 s readiness window, so
the gate aborted with `FATAL — goopg did not become ready`. This was NOT a
regression — it blocked the *entire* M0117 tail for multiple Ralph loops
(#7, #8 both reported BLOCKED, deferring every CLOG live-path slice because the
mandatory populated-data Q12/Q13 gate could not run), since the practice card
requires the TPC-H spot-check for any visibility/tuple-format change.

### Root cause

`CLog.EnablePGSLRUMirror` (`internal/mvcc/clog.go`) is called once at startup
(`initdb.Open`) to project the resident in-memory commit-status banks — loaded
from the goopg-native flat file — into the PG-byte-compatible `pg_xact/` SLRU
mirror. Its backfill loop iterated **every committed/aborted XID** and called
`mirrorToSLRUUnlocked(xid, status)` per XID. That function does, per call:

```
open(segfile, O_RDWR|O_CREATE) → Stat → (Truncate) → ReadAt(1B) → OR → WriteAt(1B) → f.Sync() → Close
```

i.e. **one `open`+`fsync`+`close` cycle per XID**. On a well-aged cluster that
is ~`NextXID` fsyncs (≈1.5M here). On WSL2's slow fsync this took *many minutes*
(>6 min observed via `pprof/goroutine`, still stuck on segment `0000`), so
startup looked hung and the readiness loop timed out. The per-XID `f.Sync()` is
the live per-commit durability barrier — necessary on the commit path, but the
backfill does **not** need it: the backfill merely re-projects in-memory state
that is itself reconstructed from the flat file on every start, so a crash
mid-backfill simply re-runs the backfill next start (no per-XID durability
guarantee is owed).

## Fix

Replace the per-XID backfill loop with a single call to the existing batched
range writer `mirrorTerminalRangeBatchedUnlocked(FirstNormalTransactionID, hi)`,
where `hi = nBanks * xidsPerBank`. That primitive — added by M0110-0004 (RW-002)
for the byte-identical pathology on the `pg_resetwal --next-transaction-id`
implicit-abort sweep — builds each segment's page buffer entirely in memory
(reading the existing on-disk bytes first, OR-ing each XID's real lane via
`GetStatus`), writes the whole buffer once, and issues **one `f.Sync()` per
segment file**. With ~1.5M XIDs spanning ≈2 segments (`clogXactsPerSegment` =
1,048,576) that collapses ~1.5M fsyncs to **2**.

The change is confined to `EnablePGSLRUMirror`'s backfill block; the live
per-commit `mirrorToSLRUUnlocked` path (and its per-commit fsync) is untouched,
so commit durability is unchanged.

### Byte-equivalence

`mirrorTerminalRangeBatchedUnlocked` uses the identical lane arithmetic
(`segNo`/`pageInSeg`/`xidInPage`/`bShift`) and the same strict-OR semantics
(`TransactionIdSetStatusBit`) as the per-XID writer, reading each XID's status
through the same `GetStatus` the old loop read from the banks. `GetStatus`
returns `TxnStatusUnknown` for a nil bank or an out-of-range byte index, so
scanning the full `[FirstNormalTransactionID, nBanks*xidsPerBank)` range is safe
and skips every non-terminal slot exactly as the old loop did (it only mirrored
`Committed`/`Aborted`/`SubCommitted`). The floor is `FirstNormalTransactionID`
(segment 0), matching the old loop's `xid != 0` skip — no truncation-floor
change, so XID coverage is identical.

## Blast radius

Nil on the live query/commit path. The only behavioural change is the number of
fsyncs issued during the one-shot startup backfill. Crash-safety of the backfill
is unchanged (re-derivable from the flat file). No WAL/format/visibility surface.

## Validation

- **Empirical (the actual goal):** with the fix, a fresh `tpch-spotcheck.sh`
  start on the 2.2 GB / ~1.5M-XID bench data dir reached *ready* in **~35 s**
  (process start 07:45:28 → listener bound 07:46:04), versus the prior
  >6-minute hang. The gate now proceeds past readiness to the schema probe.
  (It then SKIPs only because that data dir lacks the `tpch` *role* — a separate
  data-provisioning gap, not a CLOG concern; the `postgres` superuser connects
  fine.)
- `go test -race ./internal/mvcc/ ./internal/wal/` — PASS (WAL/MVCC practice card
  race gate).
- New regression `TestCLogEnableMirrorBackfillBatched`
  (`internal/mvcc/clog_dual_store_consistency_test.go`): writes a mixed
  committed/aborted/sub-committed set spanning an SLRU page boundary into a CLog
  with the mirror **disabled** (so the statuses live only in the banks/flat
  file), then `EnablePGSLRUMirror` and asserts the SLRU-derived view matches
  every XID — reaching the drift surface via the *backfill* (the existing
  `TestCLogDualStoreConsistency` only exercises the live per-commit mirror). Also
  asserts an unwritten XID stays Unknown and both pages of segment 0000 are
  materialised.
- `go build ./...` / `go vet ./internal/mvcc/` clean.
- pgbench pre-commit smoke (live guard for non-query-path changes per the
  practice card / `tpch_spotcheck_slru_backfill_startup_hang` memory).

## Follow-ups

The data dir's missing `tpch` role still prevents a real Q12/Q13 row-count run
from *this* dataset; reload via `bench/tpch/setup_goopg.sh` +
`build_schema_goopg.sh` (creates the role) re-enables the full row-count gate —
now that startup no longer hangs, that gate is usable again, unblocking the
M0117 CLOG live-path slices (0117-0006 Part B / 0117-0007 Part B) that were
deferred solely on the unrunnable gate.
