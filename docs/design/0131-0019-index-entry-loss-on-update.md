# 0131-0019 — Index entries vanish for updated rows (M0131-S30 re-diagnosis)

Status: **diagnosis — accepted, fix NOT landed**
Filed: 2026-08-11 (M0131-S30 confirmation step)
Supersedes the root-cause claim in the M0131-S30 fix_plan item and the
2026-08-11 deferral-ledger row that opened it.

## Why this document exists

`M0131-S30` was filed as *"crash recovery loses AND duplicates heap rows
(non-atomic non-HOT update)"*, and its first instruction was explicit:

> First step is to CONFIRM that diagnosis (dump the WAL tail with `pg_waldump`
> around a duplicated `aid`) before changing anything.

The confirmation ran and **refuted the diagnosis**. This document records what
was measured, so no later loop rebuilds the WAL-atomicity fix S30 asked for on
evidence that does not support it.

## What S30's evidence actually showed

S30's numbers came from an anti-join:

```sql
SELECT g FROM generate_series(1,500000) g
LEFT JOIN pgbench_accounts a ON a.aid = g WHERE a.aid IS NULL;
```

On goopg that anti-join is **index-driven** (`Index Scan using
pgbench_accounts_pkey`). It therefore measures *index reachability*, not row
presence. S30 read 218 "missing" `aid`s against 64 net missing rows and inferred
"~154 DUPLICATED". That inference is not supported: the gap is fully explained
by rows the index cannot reach.

### The controlling measurement

The S30 repro was re-run and, by accident, **the `kill -9` never fired** — the
probe's `pgrep -f "bin/goopg start -D $DIR"` does not match the
cgroup-wrapped command line, so it killed nothing. With **no crash at all**:

| measurement | value |
|---|---|
| `count(*)` | 500000 |
| `count(distinct aid)` | 500000 |
| anti-join "missing" `aid`s | **672** |
| `SELECT ... WHERE aid=1435` (index) | **0 rows** |
| `SELECT ... WHERE aid+0=1435` (heap) | `(7,191) | 1435 | 1553` |

The heap is intact and duplicate-free; 672 rows are simply unreachable by index.
**A crash is not required to produce S30's signal.** Whatever crash-induced
`count(*)` loss may also exist is unmeasured — S30's own number for it
(500000 → 499949) is not reproduced here and remains open.

## The real defect (reproducible in 20 seconds, no crash, no concurrency)

`/analysis/idxprobe.sh`: fresh cluster, `t(id int primary key, v int)` with
20 000 rows, then a pgbench script doing `UPDATE t SET v=v+1 WHERE id=:id`.

| run | result |
|---|---|
| 4 clients, 20 s | heap 20000 rows / 20000 distinct ids — **5576 ids unreachable by index** |
| **1 client**, 20 s | heap 20000 rows / 20000 distinct ids — **9286 ids unreachable by index** |

Zero failed transactions, no kill, no restart. Single-client reproduces it, so
**concurrency is not a factor**.

Supporting observations:

- Affected rows are always ones that were updated (`v <> 0`); untouched
  neighbours are fine. Not every updated row is affected (`id=15`, `v=3`, is
  still reachable).
- An **ordered range scan skips the keys outright**: `WHERE id>=10 ORDER BY id
  LIMIT 5` returns `14,15,17,18,20`, stepping over 10,11,12,13,16,19. A forward
  leaf traversal that skips a key proves the entry is *absent from the tree*,
  not merely passed over by an equality scan that stopped early.
- `REINDEX INDEX t_pkey` **fails**: `ERROR: could not create unique index
  "t_pkey"`. The index build sees duplicate keys coming out of the heap, i.e.
  superseded row versions are still being handed to the index builder even
  though MVCC scans correctly hide them. This is a **second, distinct defect**
  and is the grain of truth behind S30's "duplicated" wording — the duplicates
  are dead versions visible to the index builder, not live rows.

## Mechanisms ruled out this loop

Each was tested, not argued:

1. **Non-atomic non-HOT update WAL (S30's stated cause)** — refuted: the loss
   occurs with no crash and no recovery.
2. **Swallowed error at `internal/executor/operators_storage.go:7267`**
   (`_ = tree.Insert(key, ptr)` in `maintainUniqueIndexesForInsert`) — the call
   was instrumented to print any error; across a full failing run it printed
   **zero** lines. `Insert` reports success. (The discarded error is still a
   real robustness defect and should be propagated regardless — but it is not
   this bug.)
3. **Unique-key rejection inside the btree** — `BTree.Insert`
   (`internal/access/btree/btree.go:2543`) performs no uniqueness check at all;
   it inserts duplicates freely. There is nothing there to reject the entry.
4. **The cached-rightmost-leaf fast path** (`tryInsertNoSplit` →
   `tryInsertOnCachedRightmost`, `btree.go:2589`) — disabled with a forced
   `false &&` and rebuilt; the probe reproduced unchanged (still ~12k
   unreachable). Not the cause.
5. **Index scan stopping at the first matching entry** — refuted by the range
   scan skipping the keys.

## Where the next loop should look

Two candidates survive, in priority order:

1. **`maintainUniqueIndexesForInsert` is never reached for these updates.**
   The non-HOT update path calls it only under
   `if destPart != nil { ... } else if pu.scanTbl != nil { ... }`
   (`operators_storage.go:5304-5311`). If both are nil the new version gets **no
   index entry at all** — which matches every observation, including the absence
   of instrumentation output (the probe printed nothing because `Insert` was
   never called, not because it succeeded). **Probe: put the print at the call
   site, unconditionally, and count how many updates reach it versus how many
   updates ran.** This is the cheapest decisive next step.
2. **The split path** (`insertIntoBlock`) loses pre-existing entries when a leaf
   splits. Weaker: losses are interleaved (10,11,12,13 gone but 14,15 present),
   which reads more like per-row insertion failure than a dropped page range.

Also open, and separately filable once (1) is settled: the index builder's
liveness predicate, which currently indexes superseded versions and makes
`REINDEX` fail on a heap that MVCC considers duplicate-free.

## Impact

Wrong answers on ordinary traffic — an indexed equality lookup silently returns
zero rows for a row that exists. No crash, no concurrency, no special
configuration required; ~46% of updated rows in the single-client probe. This
outranks the WAL-atomicity work S30 was filed for.

## Repro script

`analysis/idxprobe.sh` (committed with this document). Env: `CLIENTS` (default
4), `SECS` (default 20). Uses port 5534 and the cgroup cap wrapper per the
standing server-lifecycle rule.
