# 0118-0018 — Scan-time ACCESS SHARE so LOCK TABLE conflicts (timeouts table-level half)

Status: accepted
Milestone: M0118-0009 (upstream isolation spec suite — `timeouts`)
Supersedes the deferral recorded in [[0118-0017]] (lock_timeout / row-level half).

## Problem

`postgres/src/test/isolation/specs/timeouts.spec` has eight permutations: four
**row-level** (`wrtbl` … `update`) and four **table-level** (`rdtbl` …
`locktbl`). The row-level half landed in [[0118-0017]] (the `lock_timeout` GUC +
statement-timeout-aware lock-wait cancel messages). The table-level half stayed
deferred and so `timeouts.spec` was not promoted.

The table-level permutations are:

```
session s1: BEGIN; SELECT * FROM accounts;          -- rdtbl
session s2: BEGIN; SET {lock,statement}_timeout='…'; -- sto/lto/lsto/slto
            LOCK TABLE accounts;                      -- locktbl, waits then times out
```

In PostgreSQL `rdtbl`'s plain `SELECT` takes an **ACCESS SHARE** lock on
`accounts` that is held until s1's transaction ends. `locktbl`'s bare
`LOCK TABLE` requests **ACCESS EXCLUSIVE**, which conflicts, so it parks on the
lock and is cancelled when whichever of `lock_timeout` / `statement_timeout`
fires first — emitting the matching cancel message (already correct after
[[0118-0017]]).

goopg deliberately kept ordinary scans **off** the heavyweight lock manager
(`tableLockMgr`): only explicit `LOCK TABLE` registered there (see
[[lockmgr_locks_are_statement_scoped]] and `acquireRelLockTxn`). So a plain
`SELECT` held nothing, `LOCK TABLE` was granted instantly, never waited, and the
table-level permutations diverged.

## Change

Add a narrowly-scoped scan-read lock acquisition. New helper in
`internal/executor/context.go`:

```go
func (c *Context) acquireScanReadLockTxn(rel storage.RelFileNode) error {
    if c.TxnLockBackendID == 0 || rel.RelOid < firstNormalObjectOID {
        return nil
    }
    return c.acquireRelLockTxn(rel, lockmgr.AccessShareLock, false)
}
```

Wired at the three relation-read scan-open sites that already call the (in
production no-op) `acquireRelLock(rel, AccessShareLock)`:

- `operators_storage.go` — sequential scan (`SELECT * FROM accounts` plans to a
  seqscan; this is the path the spec actually exercises),
- `operators_index.go` — index scan,
- `operators_indexonly.go` — index-only scan.

It reuses the existing transaction-scoped LOCK-TABLE machinery
(`acquireRelLockTxn` on `tableLockMgr`, keyed by `TxnLockBackendID`), so the
ACCESS SHARE lock lives until COMMIT/ROLLBACK (released by `ReleaseTableLocks`
from `connTxState.End()`), exactly like a `LOCK TABLE` lock. Because it goes
through the waiting `acquireRelLockTxn`, if a scan ever does meet a conflicting
ACCESS EXCLUSIVE holder it waits under the same `lock_timeout` /
`statement_timeout` budget on the statement context.

## Why the blast radius is bounded (hot-path safety)

`tableLockMgr` is a single-mutex lock manager, so adding it to the read path is
the perf-sensitive part the prior loop flagged. The acquisition is confined:

1. **Autocommit reads skip it.** Outside an explicit transaction block
   `TxnLockBackendID == 0`, so single-statement `SELECT`s never touch
   `tableLockMgr` — and a single statement cannot be on either side of a
   *cross-statement* lock conflict anyway.
2. **System catalogs skip it** (`RelOid < firstNormalObjectOID`, 16384). goopg
   serves catalogs from virtual/in-memory builders and never
   AccessExclusive-locks them, so there is nothing to conflict with.
3. **ACCESS SHARE conflicts only with ACCESS EXCLUSIVE.** In the common case
   (no concurrent `LOCK TABLE` / DDL) the lock is granted instantly with no
   contention beyond the brief mutex.
4. **lockmgr is idempotent.** Re-scanning the same relation within a transaction
   is a cheap held-mask check (`Acquire`/`TryAcquire` return immediately when the
   mode is already held), not a new lock-table entry.

So within an explicit transaction the only added cost is one `tableLockMgr`
acquire per *distinct* relation per transaction (subsequent scans are mask-check
no-ops). pgbench TPC-B (explicit `BEGIN`…`END`, 4 tables) therefore pays a
handful of grant-immediately mutex round-trips per transaction; the smoke run
shows 0-failed with no throughput regression (see Gates).

## Crash / replication safety

No on-disk or WAL change — `tableLockMgr` is in-memory and transaction-scoped,
released at transaction end and on connection teardown. Nothing to replay.

## Tests / Gates

- `TestPort_TimeoutsTableLevel` (new, `internal/testport/timeouts_rowlock_test.go`):
  s1 holds an open txn that has `SELECT`ed `accounts`; s2 `LOCK TABLE accounts`
  blocks and is cancelled by the shorter of `lock_timeout` / `statement_timeout`
  with the matching message, near the ~300ms budget. 4/4
  (statement / lock / lock-wins / statement-wins).
- `TestPort_TimeoutsRowLevel` still 4/4 (no regression to the row-level half).
- Full row-lock / savepoint / merge / multixact / deadlock / skip-locked /
  nowait isolation-port batch: no regression.
- `-race` on executor / mvcc / lockmgr; executor unit suite.
- pgbench CI-parity smoke: 0-failed.

`timeouts.spec` promoted `failed` → `pass` in the target inventory; coverage /
inventory markdown regenerated.

## Deferred

Lock carry to the heavyweight manager for **writes** (`UPDATE`/`DELETE`/`INSERT`
take ROW EXCLUSIVE in upstream) is still not modeled on `tableLockMgr` — goopg's
write/write conflicts ride `xmax`/`WaitForXID`, and no currently-`port` spec
needs the table-level ROW EXCLUSIVE lock. If a future spec needs
`SHARE`/`SHARE ROW EXCLUSIVE` vs DML conflicts, extend the same helper to the DML
open paths under the same narrow gate.
