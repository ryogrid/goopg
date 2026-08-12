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

## 7. Evidence RETRACTED — 2026-08-12 (loop #142)

**The measurements in §3 are not trustworthy, and the defect they describe does
not reproduce at HEAD.** Five runs of the workload at the *exact* failing scale
(8 clients × 100 statements × 100 rows = 80000 rows) are clean:

| run | harness | result |
|---|---|---|
| `/tmp/lostrows-dump` | `analysis/lostrows-ctiddump.sh` | 80000/80000; shell-diff of the dumped ids: 0 heap-missing, 0 index-missing |
| `/tmp/lostrows-g` | `analysis/lostrows-concurrent-insert.sh` | `count(*)` = 80000 |
| `/tmp/lostrows-r{1,2,3}` | `analysis/lostrows-concurrent-insert.sh` | `rows=80000 heap_missing=0 index_unreachable=0 heap_dupes=0`, `OVERALL: PASS` ×3 |

### 7.1 Root cause of the false positive: the probe could measure mid-load

Every one of these scripts backgrounds the server with `&` and later calls a
bare `wait`. Bash's `wait` with no arguments waits for **all** background jobs
*including the server*, which never exits. So the intended barrier between "all
clients have committed" and "count the rows" was not a barrier at all — its
behaviour depended entirely on whether the cgroup wrapper
(`scripts/goopg-test-run.sh`) stayed in the foreground of its job or detached
into a systemd scope:

* wrapper stays in the job → `wait` blocks forever. Observed twice this loop as
  an apparent multi-hour hang "in the measurement query" while the load had in
  fact finished in **under a minute**.
* wrapper detaches → `wait` returns as soon as the *server* job is reaped,
  i.e. potentially **while clients are still inserting**, and the script counts
  a partially-loaded table. That is a shortfall by construction.

The §3 numbers came from a run of the second kind. Their internal
inconsistency is the fingerprint: `rows=75922` with `heap_missing=6328` implies
73672 distinct ids present, i.e. 2250 *duplicate* rows under a PRIMARY KEY,
which no page-level mechanism explains but two queries sampling a growing table
at different instants explain exactly. The "page-tail contiguous" loss pattern
is likewise just the frontier of an in-progress load.

Fixed in all three harnesses (2026-08-12) by collecting client PIDs and waiting
on **those only**:

```sh
CLIENT_PIDS+=($!)   # per client
wait "${CLIENT_PIDS[@]}"
```

### 7.2 Second measurement defect: the anti-join metric

`heap_missing`/`index_unreachable` were computed server-side with
`NOT EXISTS (SELECT 1 FROM lr b WHERE b.id+0 = g)` over `generate_series`. At
WANT=80000 that anti-join does not finish (a post-mortem run sat in it for
35 min), and its answer was never independently checked — it returned exactly
`6328` on two runs whose `count(*)` shortfalls differed. Both metrics are now
computed by dumping the ids and diffing them with `comm` in the shell: O(n log n),
always terminates, and every number is re-checkable from the files left in `$W`.
A `heap_dupes` assertion was added so "rows lost" can never again be conflated
with "rows lost AND duplicated".

### 7.3 What §5's step 1 turns out to be — already answered

The planned "raise `shared_buffers` above the working set" experiment was moot:
the probe cluster boots at the PG default 128 MB = **16384 slots**
(`shared_buffers_slots=16384` in every `server.log`) against a ~1000-page
working set, so **no eviction ever happened**, and the failing run logged a
single checkpoint — at startup, before the load. Neither the victim/flush path
nor the checkpointer was in play for the reported loss, which is consistent with
there having been no loss.

### 7.4 Status

S30.9 is **not confirmed at HEAD**. It must not be treated as a known live
data-loss defect, and the S30.8 blockade it created is lifted: the §6 caveat
above rested on the refuted control, so `crashprobe30`'s atomicity line is
back to being ordinary (if still unexplained) evidence. The honest residual
claim is narrower and worth keeping: *nothing here proves goopg is free of
concurrent-insert loss* — it proves the probe that said otherwise was broken.
The hardened `analysis/lostrows-concurrent-insert.sh` is now a usable gate and
should be re-run (and, if it is to be trusted as a regression gate, promoted to
a Go test) before any S30 conclusion leans on it.
