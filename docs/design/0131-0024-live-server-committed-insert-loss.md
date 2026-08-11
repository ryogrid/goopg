# 0131-0024 — Concurrent committed INSERTs are lost on a LIVE server (no crash)

Status: **diagnosis** (root cause not yet isolated; mechanism narrowed, two
prime suspects refuted). Filed 2026-08-11 (loop #141) as **M0131-S30.9**.

Supersedes the framing of **S30.8** ("committed `UPDATE pgbench_accounts` new
tuples are still not visible after replay"). S30.8's premise — that this is a
crash-recovery defect — is **refuted here**: the same invariant fails when
nothing ever crashes.

## 1. What was actually measured

`analysis/crashprobe30.sh` asserts pgbench's transaction-atomicity invariant
`sum(pgbench_accounts.abalance) == sum(pgbench_history.delta)`. It has been
failing since S30.1 landed, and every loop since has read that failure as a
replay bug, because the probe only ever evaluates it *after* a SIGKILL and
restart.

The control that had never been run is the obvious one: **run the same load and
never crash**. It fails too.

Fresh cluster, `pgbench -i -s 5`, then `pgbench -c 16 -j 4 -T 30`, clean
shutdown, no kill:

```
processed=60593   hist_rows=60279   hist_sum=-558600
acct=-504506      tellers=-449914   branches=-703406
```

All four balance sums must be equal; none of them are. `hist_rows` is *below*
the number of transactions pgbench reports as committed, so committed `INSERT`s
are missing outright.

Narrowing by client count on the same cluster:

| clients | processed | hist_rows | sum(delta) | Δ sum(abalance) |
|---|---|---|---|---|
| 1 | 7928 | 7928 (exact) | -356787 | -336808 |
| 4 | 27282 | 27125 (**157 lost**) | -100874 | -54878 |

At `-c 1` no `INSERT` is lost, so the lost-insert half is concurrency-only.
The accounts divergence at `-c 1` has a different and fully explained cause —
see §2.

## 2. Why a single client also diverges: index-unreachable rows

At `-c 1` the accounts sum still drifts. Direct probe on that cluster:

```
idx  aid=2 : 0 rows            -- Index Scan using pgbench_accounts_pkey
seq  aid=2 : 1 row, abalance=2 -- WHERE aid+0=2, defeats the index
total (seq): 500000
```

The heap row is present and correct; the primary key no longer reaches it.
Consequently every subsequent `UPDATE pgbench_accounts SET abalance =
abalance + :delta WHERE aid = 2` — the exact shape pgbench issues — matches
**zero rows, reports success, and silently drops the increment**. 2000
sequential single-session updates of one such row left `abalance` at 1.

Extent on that cluster: **69 of a 5000-id sample (1.4% of the whole table)**
were index-unreachable. `REINDEX TABLE pgbench_accounts` repaired all of them,
so the btree is genuinely missing entries rather than the scan misreading
visibility.

This is why `crashprobe30`'s row-identity assertions kept passing while the
atomicity assertion kept failing: `count(*)`, `count(distinct aid)`, `min`,
`max` are all seq-scan aggregates, and the heap really is intact.

## 3. The minimal repro: `analysis/lostrows-concurrent-insert.sh`

Updates, MVCC row contention and crashes are all unnecessary. Eight clients,
each issuing 100 single-statement `INSERT ... generate_series(lo,hi)`
transactions over a **disjoint, deterministic** id range, into one table with a
primary key, on a **fresh** cluster:

```
rows=75922 want=80000  heap_missing=6328  index_unreachable=5837
```

- Every client exits 0; `ON_ERROR_STOP=1`; zero error lines in any client log.
  A lost row is never something the client was told about.
- **6328 committed rows (7.9%) are absent from the heap entirely** — not
  invisible, not unreachable: gone.
- `index_unreachable` (5837) is *lower* than `heap_missing` (6328), so the two
  sets are not nested: ~491 ids are absent from a seq scan yet returned by an
  index scan. Both directions of heap↔index divergence occur.

The loss is **contiguous and page-shaped**. Missing ids from the equivalent
run, in order: `689-700`, `1400-1419`, `1983-2000`, `2058-2067`, … — runs of
12-20 consecutive ids at what are page tails for this row width, not scattered
singletons. Whole *tails of appends* to a page disappear together.

## 4. Ruled out — do not re-test

- **A crash / replay.** Nothing is killed in §3; the count is taken from a
  live server through the buffer pool, with the WAL never read back.
- **Client-side failure.** `ON_ERROR_STOP=1`, exit statuses checked, error
  lines counted: all clean.
- **A pre-corrupted datadir.** §3 runs `goopg init` on a fresh directory.
- **Single-client sequential appends.** 200000-row table, `fillfactor=100`,
  12 sequential updates of one row: exact, index and heap agreeing at every
  step. Small tables (50/70 rows, with and without `fillfactor=100`): exact.
  The defect needs concurrency.
- **The S30.3 duplicate-buffer mechanism.** The whole
  `GOOPG_PAGEIDENT_PROBE=1` instrumentation (`internal/storage/pageident_probe.go`
  — `PAGEIDENT-DUPSLOT` / `-REGRESS` / `-REEXTEND`, plus
  `Pool.probeAssertSlotIsMapped`) was left in place for exactly this and was
  enabled for a full reproducing run: **zero events** while 4985 rows were
  lost. Whatever this is, the mutating slot is the bufmap's slot for its own
  tag throughout, and no extend hands out an occupied block.

## 5. Where to resume

The signature — a *live* server, page-tail-contiguous append loss, heap and
btree independently affected, no buffer-tag aliasing — points at a page's later
appends being discarded while an earlier image of that same page survives, i.e.
a dirty/flush lifecycle defect rather than a page-identity one.

Concrete next steps, cheapest first:

1. **Establish whether eviction is required.** Re-run §3 with
   `shared_buffers` far above the working set (the whole table is ~10 MB;
   `internal/config/defaults.go:318` boots at 128 MB but the probe cluster
   holds much more). If the loss vanishes without eviction, the defect is in
   the victim/flush path; if it survives, it is in the append path itself.
2. **`Pool.flushBatch` (`internal/storage/bufpool.go:2483`).** It takes
   `contentMu.RLock()` on every slot in the batch *in slice order* and clears
   `slotDirtyBit` after the write. Audit the window between the AIO write being
   *issued* (`WriteBlockAIO`, :2539, page handed to the IO layer) and
   `h.Wait()` completing (:2559): the read lock is held across it, so an
   appender is excluded — verify that `Slot.Lock()` really is
   `contentMu.Lock()` and that no append path mutates a page under anything
   weaker.
3. **The dirty-bit clear at :2571.** It re-checks `s.tag == tags[i]` but not
   whether the page changed after the bytes were handed to AIO. If any writer
   can dirty the page between issue and completion, clearing the bit loses
   exactly a page tail.
4. **`batchExtendAndRegisterFSM` + `selectFSMCandidatePage`**
   (`internal/executor/operators_storage.go:8636-8703`): the contended path
   batch-extends and publishes several blocks into the FSM at once. Confirm
   every batch-extended block is initialised and made visible to `NBlocks`
   before any FSM consumer can pin it.

Gate for the fix: `bash analysis/lostrows-concurrent-insert.sh` must print
`OVERALL: PASS`, and only then is `RUNS=2 bash analysis/crashprobe30.sh`
meaningful as a *recovery* gate again.

## 6. Consequence for the S30 ledger

S30.8 stays open but is re-scoped: its evidence (a post-restart atomicity
failure) does not demonstrate a replay defect, and no replay code should be
changed on its strength until §3 passes. S30.7's landed XID-reuse fix is
unaffected — it was validated by its own unit guard, not by the probe.

This also puts a caveat on every earlier S30 conclusion that used
`crashprobe30`'s atomicity line as its signal, including the "unidirectional
divergence" reading recorded for S30.8: with a live-server loss of this size
underneath, the direction of the difference carries no information.
