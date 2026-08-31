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

---

# RESOLVED — 2026-08-11 (M0131-S31)

## Root cause

**Opportunistic HOT prune never re-points a redirect it created earlier**, and
the same pass then reclaims the slot that redirect addresses.

`pagePruneCore` (`internal/storage/prune.go`) walked the line pointers and did
`if item.Flags != ItemIDNormal { continue }`. An `ItemIDRedirect` root — the
line pointer a *previous* prune produced for a HOT chain whose root had died —
was therefore invisible to the prune. Meanwhile the redirect's target, by then a
dead HEAP_ONLY tuple further down the same chain, was correctly classified as
HOT-only dead and appended to `result.Unused`. After
`VacuumHeapPageBySlots` the sequence is:

```
LP 102: REDIRECT → 227      (untouched: not ItemIDNormal)
LP 227: UNUSED              (reclaimed in the same pass)
LP 228: NORMAL, live        (unreachable — nothing points here)
```

The btree entry for the key still addresses LP 102, and
`followHOTChainNoCopy` stops dead at a non-NORMAL chain end. The row is gone
from every index scan — an equality probe returns 0 rows, an ordered range scan
appears to *skip* the key — while a seq scan (`WHERE id+0 = k`) still returns
it. Upstream does not have this hole: `heap_prune_chain`
(`postgres/src/backend/access/heap/pruneheap.c`) starts from redirected roots
too (`if (ItemIdIsRedirected(rootlp))`) and re-points the redirect at the new
surviving tip.

**Two HOT updates of one row on a page that prunes are sufficient.** That is why
the loss looked random and load-dependent: it needs the page-full prune (which
`tryApplyHOTUpdate` triggers) plus a second HOT update of an already-redirected
root. A row whose first update happened to go non-HOT got a fresh index entry
pointing past the redirect and stayed reachable — the "interleaved" losses of
the original diagnosis.

Repairing that exposed a second, latent defect in the same helper:
`pruneChainTip` followed `t_ctid.Offset` on *any* dead member, including one
whose update was **non-HOT**. A non-HOT successor lives on a different block, so
its offset read as a slot on *this* page lands on an unrelated tuple; if that
tuple is live it becomes the redirect target and the root's index entry then
resolves to a **foreign row** (`WHERE id=104` returned two rows in the first
build of the fix). Upstream ends the chain at `!HeapTupleHeaderIsHotUpdated`
for exactly this reason.

## Fix

`internal/storage/prune.go`, both arms in `pagePruneCore`/`pruneChainTip` — so
`PagePruneOpt` (opportunistic) and `PageVacuumPrune` (VACUUM) get it alike:

1. An `ItemIDRedirect` line pointer is treated as a chain root: follow it with
   `pruneChainTip` and re-point it (recording a `Redirects` entry, which the
   existing prune WAL record already carries, so replay stays in step).
2. `pruneChainTip` stops at a dead member that is not `HeapHotUpdated`.

## Verification

| probe | before | after |
|---|---|---|
| `analysis/idxprobe.sh` (pgbench, 4 clients, 20 s, 20000 rows) | 5576 unreachable ids | **0** |
| `analysis/idxprobe3.sh` (single session, 20000 serial updates) | 4335 unreachable of 12591 updated | **0** |
| `analysis/idxprobe2.sh` (deterministic 1..5 updates of 5 ids) | ids with 2–4 updates unreachable, one update lost | **all reachable, no lost update** |
| `REINDEX INDEX t_pkey` after the pgbench probe | `could not create unique index` | **OK** |

The `REINDEX` failure was the same defect, not a second one: the index builder
was reading a heap whose chains had been severed. It is dropped from the open
list.

Regression tests: `TestPagePruneOptRepointsExistingRedirect` and
`TestPagePruneOptStopsAtNonHOTChainEnd` (`internal/storage/prune_test.go`) —
both fail on the pre-fix `prune.go`.

## Still deferred

A redirect whose entire chain is dead is left in place rather than converted to
`LP_DEAD` (upstream `heap_prune_record_dead`), because the prune WAL record has
no way to express an LP_DEAD transition and `VacuumHeapPageBySlots` ignores
non-NORMAL slots. Readers treat a non-NORMAL chain end as "no live tuple", so
this costs a line pointer, not correctness. See the deferral ledger.
