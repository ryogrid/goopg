# 0131-0020 — Crash recovery loses committed work: M0131-S30 CONFIRMED and re-scoped

**Status:** diagnosis (measurement complete, fix NOT started)
**Task:** M0131-S30
**Date:** 2026-08-11
**Supersedes the refutation in:** `0131-0019` (which correctly voided S30's *original*
evidence; this doc supplies evidence that survives that refutation)

## What S30 asked for

After `0131-0019` refuted S30's original diagnosis, the fix_plan left exactly one
claim open and demanded it be re-measured before any WAL work:

> re-measure it with a kill that actually fires (match on the data-dir path, or read
> the pid from `postmaster.pid`) and with a heap-only anti-join that cannot use an
> index (`WHERE aid+0 = g`), THEN decide whether any WAL-atomicity work is needed.

That measurement has now been run. **Verdict: CONFIRMED. WAL/recovery work is
needed, and the defect is substantially larger than S30 described.**

## The probe

`analysis/crashprobe30.sh` (committed with this doc). It fixes both halves of what
made the original evidence worthless, and adds two assertions S30 never had.

### Proven kill

The original probe located the victim with `pgrep -f "bin/goopg start -D $DIR"`,
which did not match the cgroup-wrapped command line — so the run measured a server
that was never killed. The probe now takes the pid from `$DIR/postmaster.pid`,
verifies `/proc/<pid>/cmdline` really is a goopg server *before* signalling,
verifies `/proc/<pid>` is gone *after*, verifies the port stops answering, and
**aborts the whole run** if any check fails. A probe that silently does not crash is
worse than no probe; this one cannot do that.

### Index-independent assertions

S30's "missing rows" query was an anti-join goopg executes as an `Index Scan using
pgbench_accounts_pkey`, so it measured index reachability, not row presence. The
probe now pins the aid set with four scalar aggregates that no index can satisfy:

```
count(*) = count(distinct aid) = 500000, min(aid) = 1, max(aid) = 500000
```

Those four together mean the set is *exactly* `{1..500000}` — proving absence of
loss **and** of duplication without an anti-join at all. The `aid+0` anti-join is
retained only as an independent cross-check of the same conclusion.

### Cross-table atomicity invariant (new)

pgbench initialises `abalance` to 0, and every TPC-B transaction does
`UPDATE pgbench_accounts SET abalance = abalance + :delta` **and**
`INSERT INTO pgbench_history(delta)` inside one transaction. Therefore

```
sum(pgbench_accounts.abalance) == sum(pgbench_history.delta)
```

must hold after any crash, for any set of committed transactions. This is the
assertion that actually catches a torn transaction — S30 had no such check, which is
why it could only ever report a vague count delta.

## Results — 3 runs, every kill proven, 3 distinct failures

| run | kill | rows | atomicity invariant | outcome |
|---|---|---|---|---|
| A | proven | **495584 / 500000** (4416 lost, 0 duplicated) | broken (−459015 vs −452038) | replay silently truncated |
| B | proven | 500000 / 500000 | **broken** | rows intact, transactions torn |
| C | proven | — | — | **server refuses to start** |

Run A's replay log names its own bug:

```
WARN wal: end of WAL reached during replay reason="invalid record header"
     detail="wal: invalid record header: padding bytes nonzero (0xff 0x07)"
     lsn=134217721 stream_offset=117440504
```

`134217721 = 0x7FFFFF9` — seven bytes short of the 128 MiB mark, i.e. the very tail
of a WAL segment. Replay read those trailing bytes as a record header, concluded
"end of WAL", and **discarded the entire tail of the stream** — silently, as a
`WARN`, with the server then starting up and reporting success. The `0xff` filler is
consistent with a *recycled* segment's stale contents, which is the same family of
bug as the neighbouring `M0131-S19` page-header work.

Run C's replay does not truncate; it hard-errors and the cluster will not open:

```
goopg start: wal replay: replay record 826236 lsn[154662577,154662656]:
  wal: xlog heap-update add new tuple: storage: not enough free space in page
```

Replay's page state has diverged from the state the original execution saw — the
heap-update arm is trying to place the new tuple on a page that no longer has room
for it. This is direct evidence in the **non-HOT update replay arm**, the exact
region S30's retained hypothesis named.

## What is exonerated (do not re-investigate)

Two controls, both with proven kills, both clean:

- **Bulk load is durable.** Crash immediately after `pgbench -i -s 5` with zero
  update traffic: `count = distinct = 500000`. The COPY/bulk-insert WAL path replays
  correctly. This also corroborates the boundary hypothesis — that run's WAL never
  reached the 128 MiB mark.
- **The live update path is correct.** 45 s of 16-client pgbench with **no crash**:
  `count = distinct = 500000` and the atomicity invariant holds *exactly*
  (843253 == 843253). A clean stop + restart is likewise clean. So nothing is lost
  during normal operation, and nothing is lost through a graceful shutdown — the loss
  is crash-recovery-only.

Also checked and **not** the cause: the WAL-before-data rule is enforced —
`Pool.flushSlot` (`internal/storage/bufpool.go:2545`) flushes WAL up to the page's
`pd_lsn` before `WriteBlock`, and the delete half stamps `pd_lsn` correctly
(`markHeapDeleteDirty` → `Pool.MarkDirtyLogicalChange`, which does `SetLSN(lsn)` at
`bufpool.go:2276`).

## Aggravating factor

`checkpoint` appears **zero** times in the pre-crash server log across the entire
load. Every one of these runs therefore had to replay from near the start of the
stream (`redo=16777256`), which is both why the volume is large enough to reach the
segment boundary and why a truncated replay costs so many rows. Whether goopg should
have checkpointed during ~180 MiB of WAL is a separate question worth asking.

## The three defects to fix (successor work)

1. **Replay must not mistake a segment's unusable tail for end-of-WAL.** When the
   space remaining in a page/segment cannot hold a record header, PG continues at the
   next page after its header rather than parsing the leftover bytes. Recycled
   segments make those leftover bytes non-zero, which is precisely what tripped this.
2. **Early end-of-WAL must not be a `WARN` that still starts the server.** Silently
   discarding committed work and reporting a successful start is the worst possible
   behaviour; a truncation that is not at the true stream end must be loud.
3. **The non-HOT heap-update replay arm is wrong** (run C proves it independently of
   any WAL-framing issue). S30's retained hypothesis — that `updateOp.Next()`'s
   fallback emits `HeapDelete` (`operators_storage.go:5270`) and `HeapInsert`
   (`:5300`) as two separate records where PG writes one atomic
   `XLOG_HEAP_UPDATE` — remains the leading candidate and is now backed by a replay
   error inside that arm. Note the two-record split alone cannot lose a *committed*
   row (both records precede the commit record in the stream), so the fix must
   explain run B's torn transactions too; do not stop at making the record atomic.

## Gate for the fix

`RUNS=3 bash analysis/crashprobe30.sh` must print `OVERALL: PASS`. Every assertion is
index-independent, the kill is self-verifying, and the atomicity invariant makes a
torn transaction a hard failure rather than a judgement call.

## CLOSED 2026-08-12 (loop #145) — the gate passes 6/6 at `fb8affdb`

The gate this document defines (`RUNS=3 bash analysis/crashprobe30.sh` printing
`OVERALL: PASS`) now passes, and passes repeatably: **two independent batches of
three runs, 6/6 PASS**, at `fb8affdb` with `bin/goopg` freshly built from that
tree. Logs: `/tmp/cp30_head_fb8affdb.log`, `/tmp/cp30_head_confirm.log`.

Every assertion this document called for is satisfied in every run:

| assertion | result (all 6 runs) |
|---|---|
| row survival | `count(*) = distinct(aid) = 500000`, `min=1`, `max=500000` |
| atomicity invariant | `sum(abalance) == sum(history.delta)` **exactly** |
| index parity | `idx_count=500000`, `heap_anti_missing=0` |
| kill was real | `KILL_PROVEN` (pid + `/proc/<pid>/cmdline` verified before, `/proc` gone after) |

The per-run `sum` values differ widely and change sign across runs (+71742,
+477315, +301490, −747056, −193358, +612803) — that is the expected shape, since
each run kills pgbench at a different point; what matters is that the two sides
agree to the byte every time. The pre-fix signature was a *disagreement* between
the two sums, first bidirectional and then (after S30.7's XID-reuse fix)
unidirectional; neither shape survives.

No code changed in the closing loop. The defects were fixed by the sub-items
S30.1 / S30.1b / S30.2 / S30.3 / S30.4 / S30.5 / S30.6 / S30.7 / S30.8 (see
`0131-0022`, `0131-0023`, `0131-0027`, `0131-0028`, `0131-0029`) plus **M0131-S32**
(`0131-0025`), whose hand-written `const maxChain = 64` truncation in every
HOT/CTID chain walker was recorded at filing time as *"plausibly contributes to
S30's crash-probe divergences"*. That prediction is now confirmed by measurement:
S32 was the last change to land before this gate flipped from failing to passing.

**Residual scope, deliberately not closed here.** `crashprobe30.sh` is a manual
shell probe; nothing runs it automatically. The automated crash-recovery
coverage is the still-open `M0131-S27` (forward crash E2E, incl. stale pidfile
and torn contrecord) and `M0131-S28` (reverse crash E2E). Until those land, a
regression in this area is invisible to every gate the pre-commit hook and the
nightly batch run. See the ledger row dated 2026-08-12 under `M0131-S30`.
