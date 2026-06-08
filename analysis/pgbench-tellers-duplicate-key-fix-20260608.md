# pgbench TPC-B `pgbench_tellers_pkey` duplicate-key fix (2026-06-08)

Base commit: `f21d868c` (branch `align-data-structure-with-pg`)

## Symptom (CI failure)

`.github/workflows/test.yml` runs:

```
pgbench -i -s 1
pgbench -T 30 -c 2 -j 2 -P 5
```

The benchmark aborted with:

```
client 1 script 0 aborted in command 7 query 0:
ERROR:  duplicate key value violates unique constraint "pgbench_tellers_pkey"
DETAIL:  Key (tid)=(6) already exists.
```

`command 7` is `UPDATE pgbench_tellers SET tbalance = tbalance + :delta WHERE tid = :tid`.
After one client aborted, the *other* client hung and TPS dropped to 0 for the
rest of the run (see "Secondary bug" below).

## Reproduction

Local server on the isolation port, upstream pgbench from `postgres/local_install`:

```
GOOPG_CG_UNIT=goopg-pgbench scripts/goopg-test-run.sh \
  ./bin/goopg start -D tmp/perf-optimize/data --listen 127.0.0.1:5533
LD_LIBRARY_PATH=postgres/local_install/lib \
  postgres/local_install/bin/pgbench -T 30 -c 2 -j 2 -P 2 --failures-detailed
```

Reproduced deterministically after ~750 transactions (~5 s) — i.e. it is a
**concurrency race**, not a first-transaction failure.

## Root cause

`pgbench_tellers` has 10 rows (scale 1) and a primary key on `tid`. The TPC-B
`UPDATE pgbench_tellers SET tbalance = …` changes a **non-indexed** column, so it
is HOT-eligible. Under contention two clients update the same `tid`
concurrently; the loser's `tryApplyHOTUpdate` sees the winner's in-flight
`xmax` (`isConcurrentlyUpdated`), waits, and then **falls back to the non-HOT
delete+insert path**.

Commit `2c779d66` had added `checkUniqueIndexesForInsert` to that non-HOT
UPDATE path. That INSERT-time check probes the unique index and calls
`isLiveForUniqueCheck`, which treats a tuple whose `xmax` is **active in another
transaction** as a *live duplicate* (`uniqueCheckWithWait` waits only on a
concurrent `xmin`, never on an in-flight `xmax`). So when the loser's check
scans the PK index and lands on a sibling version of the *same* `tid` that a
concurrent client is mid-updating (in-flight `xmax`), it raises a spurious
23505.

The deeper issue: the UPDATE does **not change the key** (`tid`). A no-key-change
UPDATE can never violate its own uniqueness — the "duplicate" the probe finds is
just another MVCC version of the very row being updated. PostgreSQL never
performs (let alone fails) a uniqueness check here: a HOT update touches no
index, and a non-HOT update's `_bt_check_unique` recognises the prior versions as
the same logical row.

## Fix

`internal/executor/operators_storage.go`:

- New `indexKeyColumnsChanged(idx, cols, oldRow, newRow, cat)` — compares the
  encoded index-key bytes of old vs new row.
- New `checkUniqueIndexesForUpdate(ctx, tbl, cols, oldRow, newRow, forceAll, pos)`
  — same probe as the INSERT-time check, but **skips any unique/primary index
  whose key columns are unchanged**. `forceAll=true` (cross-partition moves)
  bypasses the skip, since a move into a different relation may legitimately
  collide with a pre-existing key there.
- The two UPDATE non-HOT call sites (`updateViaIndex` and the seqscan update
  path) now call `checkUniqueIndexesForUpdate(..., oldRow, newRow, isCrossPartitionMove, …)`
  instead of `checkUniqueIndexesForInsert(..., newRow, …)`.

The INSERT/MERGE paths and the shared `checkUniqueIndexesForInsert` /
`uniqueCheckWithWait` / `isLiveForUniqueCheck` helpers are **unchanged**. A
genuine key-changing UPDATE that collides with another live row still raises
23505 (preserves the `2c779d66` / M0100-0005r behaviour).

## Verification

- `pgbench -T 30 -c 2 -j 2` (CI config) × 3 fresh runs: exit 0, **0 failed
  transactions, 0 errors**, 4989 / 5036 / 6759 tx processed. (Before the fix the
  run aborted within ~5 s.)
- New regression tests in `internal/executor/insert_unique_constraint_test.go`:
  `TestCheckUniqueIndexesForUpdate_NoKeyChangeSkips`,
  `…_KeyChangeStillEnforced`, `…_ForceAllProbesUnchangedKey`.
- `go test ./internal/executor/... ./internal/storage/... ./internal/mvcc/...
  ./internal/access/...` pass; `go build ./...` and `go vet ./internal/executor`
  clean.
- `internal/server` has one PRE-EXISTING unrelated failure
  (`TestPGHeapEncodingPreservesTextLikeInsertCoercions`, also fails at HEAD
  without this change); all other server tests pass.
- The change is write-path-only (the new functions are reachable only from the
  two UPDATE sites), so TPC-H read-only queries (Q12/Q13 silent-regression
  tripwires) are structurally unaffected.

## Secondary bug discovered (separate, out of scope)

Under heavier contention (`pgbench -c 4 -j 4`, scale 1) a backend that returns
`40001 could not serialize access` (EPQ retries exhausted) mid-transaction
leaves the connection wedged: pgbench then reports `current transaction is
aborted, commands ignored`, TPS drops to 0, and the surviving clients hang
indefinitely. Diagnostics of the hung server: the erroring backend's goroutine
is gone (16 goroutines total, no backend frames), the process is fully idle
(0 CPU; all OS threads parked in `futex_wait`), the socket stays
`ESTAB`/`CLOSE-WAIT`, and `pg_stat_activity` still lists the backend as active
on `UPDATE pgbench_tellers`. This is a pre-existing transaction-error-recovery /
protocol-state bug, **not** triggered by the CI config (`-c 2` is robust across
repeated runs) and unrelated to the unique-check change. Tracked as a follow-up.
