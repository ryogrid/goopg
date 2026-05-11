# Milestone 0090 — pgbench scale-100 MVCC + INSERT bugs

**Status:** planned
**Depends on:** M0079, M0080, M0088, M0089 (durability boundaries
all closed first so the scale-100 symptom isolates to the issues
below, not to a checkpoint/stop durability gap).
**Drives:** pgbench at the documented `bench/pgbench-compare/`
parameters (`scale=100, -c 100 -j 100 -T 180`) completes the full
init → standard → simple-update → select-only sequence with 0
failed transactions per workload.

## Context

After M0089 closed the checkpoint/stop/restart durability surface
on 2026-05-11, the original pgbench standard-→-simple-update
failure still reproduces:

```
$ pgbench -h 127.0.0.1 -p 5433 -U postgres \
    -c 100 -j 100 -T 180 postgres   # standard at scale 100
   ... 12,841 transactions, 0 failed.
$ goopg checkpoint && goopg stop
$ goopg start
$ pgbench -h 127.0.0.1 -p 5433 -U postgres \
    -c 100 -j 100 -T 30 -N postgres   # simple-update
   pgbench: error: client … aborted in command 7 query 0:
     ERROR:  short read at block
   number of transactions actually processed: 0
```

Even `-c 1` reproduces the same error on the SAME data dir,
confirming the bug is **state-corruption, not a concurrency
race.**

Two distinct symptoms surface in this state:

### Bug 1 — `pgbench_history` INSERTs lost at scale 100

After the standard run:

```
$ ls -la <DataDir>/base/<dboid>/<history_oid>
-rw-------  0 May 11 09:57 16400
$ psql -c "SELECT count(*) FROM pgbench_history;"
 count
-------
     0
(1 row)
```

The heap file is 0 bytes despite 12,841 reportedly-committed
transactions that each do `INSERT INTO pgbench_history (...)
VALUES (...)`. At smaller scales (5, 10) the same workload
correctly grows the file and persists the rows across restart.

Likely candidates to investigate:
- `writeHeapRow` / `writeHeapRowReturning` routing
  (`internal/executor/operators_storage.go:1153-1316`) for any
  scale-or-concurrency-dependent fast path that bypasses
  `Manager.Extend`.
- `Pool.PinNew` (`internal/storage/bufpool.go:746`) for a path
  where the new block is added in-memory but the underlying
  `relfile.extend` doesn't pwrite.
- Buffer-pool slot tag publishing race (`PinNew` lines 813-832):
  if the post-Extend re-check finds an existing slot, the
  current goroutine's reserved slot is released, but is the
  insertion data subsequently written to the existing slot
  correctly? If two PinNew calls race for the same blk, only
  one's content survives.
- autovacuum/HOT-prune on `pgbench_history` during the run —
  could it be truncating the heap file mid-workload? (history is
  INSERT-only so VACUUM should have no dead tuples to clean).

### Bug 2 — UPDATE leaves duplicate visible rows in `pgbench_branches` / `_tellers`

After the standard run at scale 100:

```
$ psql -c "SELECT count(*) FROM pgbench_branches;"
 count
-------
  1,610
(1 row)
```

Expected: 100 rows (scale 100). Observed: 1,610 visible rows —
roughly proportional to the workload's UPDATE count, suggesting
UPDATE is failing to mark the old tuple's xmax in some fraction
of cases. The same pattern affects `pgbench_tellers` (was 1,011
out of expected 1,000).

pgbench's auto-detect reads `SELECT max(bid) FROM pgbench_branches`
or similar to compute the scaling factor, returns "161" instead of
"100", and seeds clients to sample `aid` from `[1, 100,000 * 161]
= [1, 16,100,000]`. The actual accounts table has only 10M rows —
any aid > 10M references a row that doesn't exist, the pkey
returns a TID into a block past EOF, and the SELECT errors with
`short read at block`. So Bug 2 is what surfaces Bug 1's
symptom-of-a-symptom in the simple-update SELECT.

Likely candidates to investigate:
- `updateOp.Next` and `updateViaIndex` in
  `internal/executor/operators_storage.go` — specifically the
  PageSetHeapTupleXmax path. The 18c60d9 / 2c1e18e race-tolerance
  fixes (which `continue` past `ErrUnsupportedItem`) drop the
  row from the result count when the race fires. Under heavy
  concurrency that's many silent drops. Old versions then stay
  visible to other snapshots because their xmax was never
  stamped — increasing the visible-row count over time.
- The non-HOT UPDATE path's atomicity: when an UPDATE
  encounters a tuple that has been HOT-updated by another
  transaction (and is now a non-leaf in the chain), it may need
  to follow the chain and re-stamp; if it instead writes a
  brand-new tuple without invalidating the chain head, both
  rows stay visible.

## Required design docs

- `docs/design/0090-0001-history-insert-loss-at-scale-100.md`
  — root-cause the 0-byte heap file scenario; identify the path
  by which scale-100 INSERTs evade `Manager.Extend`. Likely
  needs a strace-of-extend or a stamped-block-logger added
  temporarily during diagnosis.
- `docs/design/0090-0002-update-xmax-stamping-correctness.md`
  — investigate how the 18c60d9 race-tolerance "skip the row"
  policy interacts with concurrent UPDATEs to leave xmax
  unstamped. Re-evaluate whether the skip is safe; PG's
  EvalPlanQual would re-fetch and re-evaluate, which goopg
  doesn't have — so the safest goopg behaviour may be to
  hard-error on the race (and abort the transaction) rather
  than silently drop the row.

## Tasks

Tasks will be detailed when this milestone is picked up. See the
fix_plan.md note about the milestone-only convention.

## Definition of Done (sketch)

- pgbench at scale 100 with `-c 100 -j 100 -T 180`:
  - Standard workload: `pgbench_history` grows in line with the
    transaction count; restart preserves the row count.
  - `pgbench_branches` / `pgbench_tellers` row counts stay
    constant at `1 * scale` / `10 * scale` (100 / 1,000)
    throughout the workload — UPDATE does not inflate them.
  - Simple-update post-restart runs cleanly: pgbench's
    auto-detected scale matches the actual init scale; no
    `short read at block` errors.
- Tests: new repro test for each bug (small-scale + concurrent)
  that fails pre-fix and passes post-fix.
