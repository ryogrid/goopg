# fix-02 — One commit record per transaction (P1)

## Problem (evidence)

The mvcc xact-marker hook (`internal/initdb/open.go:887-995`) appends **two**
WAL records per commit in PG-wire (`PageHeaders`) mode:

1. `walWriter.Append(EncodeXactCommit(xid))` — goopg's legacy commit marker
   (`open.go:923`);
2. a canonical PG `XLOG_XACT_COMMIT` record (`open.go:942`) for
   `pg_waldump`/standby compatibility;

then `FlushUpTo(endLSN)` (`open.go:967`) and `clog.SetCommitted(xid)`
(`open.go:987`). Each append pays record encoding, CRC, stripe/append
handoff (and, until fix-01 lands, a `runtime.Stack` call), and WAL bytes.
At 1,269 TPS that is ~2,500 commit-record appends/s where PG performs
~1,269.

## PostgreSQL approach (03 §2)

`RecordTransactionCommit()` (xact.c) emits exactly one `XLOG_XACT_COMMIT`
record via a single `XactLogCommitRecord(...)`; commit timestamp, subxacts,
dropped rels, and invalidation messages travel inside that one record, and
clog is updated in memory + SLRU without its own WAL.

## Design

Make the canonical `XLOG_XACT_COMMIT` record the **only** commit record;
retire the legacy append on the write side while keeping the read side
bilingual.

1. **Write side** (`internal/initdb/open.go` xact-marker hook): in
   PageHeaders mode, drop the `EncodeXactCommit` append; keep only the
   canonical record. In legacy (non-PageHeaders) mode — if that mode is
   still reachable in production data dirs — keep the legacy record as the
   sole record (no behavior change there).
2. **Recovery / replay** (`internal/wal/recovery.go` `ApplyRecord`,
   `RmgrXact` dispatch; and the streaming sibling
   `internal/wal/stream_replayer.go` — sibling-paths rule: both must change
   together): treat the canonical `XLOG_XACT_COMMIT` as the authoritative
   commit marker (set clog committed, release recovered locks, etc.). The
   replayer must continue to *accept* legacy `EncodeXactCommit` records so
   WAL written by older binaries replays correctly (upgrade path), but the
   two must be idempotent when both appear (old WAL segments will contain
   pairs).
3. **Downstream consumers audit** (grep for `EncodeXactCommit` /
   `XactCommit` record readers): logical decoding/pgoutput
   (`internal/wal` pgoutput sibling — the PGLZ lesson: decode siblings must
   agree), walsender/standby feed, `pg_waldump` parity harness, and any
   testport tests asserting record sequences.
4. Commit-LSN bookkeeping: `FlushUpTo(endLSN)` must now use the canonical
   record's end LSN (it already flushes to the *later* of the two appends;
   with one append the endLSN is simply that record's end — verify the
   SyncRep wait in `operators_tx.go:205` uses the same LSN).

## Expected lift

Halves commit-record work: encoding+append+bytes. Bounded by the append
share of per-txn cost — estimate ×1.1–1.3 at c=50; also reduces WAL volume
per txn (for reference, PG's *total* WAL is ~254 B/txn on this workload —
`pg_stat_wal` delta; goopg's per-txn WAL volume was not measured this run
and should be captured in the acceptance measurement), which shrinks
drain/flush work.

## Risks (this fix is recovery-critical)

- **Crash recovery**: a commit acknowledged after the canonical-only record
  must be recovered as committed by both replay paths. Mixed-version WAL
  (old pairs + new singles) must replay to the same end state.
- **PG standby attach** (M0106 line): the canonical record was added for
  byte-accurate standby/`pg_waldump` behavior — removing the *legacy* record
  must not disturb the canonical stream layout (it only removes an extra
  record between page boundaries; `pg_waldump` W-001 check must stay green).
- Deferral-ledger note if legacy-mode write support is dropped instead of
  kept.

## Verification plan

1. Unit: wal recovery tests for commit visibility (new-only, old-only,
   mixed WAL); stream-replayer twin test.
2. Kill-9 crash-durability e2e (the root-0022 suite): commit → SIGKILL →
   restart → row visible; abort path unchanged.
3. `pg_waldump` readability spot-check on a fresh bench WAL
   (`postgres/local_install/bin/pg_waldump`).
4. Full regress-port suite (`scripts/pg-regress-runner.sh`) — codec/format
   rule from M0106.
5. `make race-gate`; units + pgbench smoke; `run_su50.sh` perf acceptance
   (expect flush count unchanged, TPS +10–30 %, WAL bytes/txn down).
