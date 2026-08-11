# 0131-0026 — Concurrent updates of one hot row were silently dropped (M0131-S32.1)

Status: **partially landed** (index-driven UPDATE arm fixed and exact; the
SeqScan-driven arm still under-applies — see §7).
Predecessor: `0131-0025-single-row-update-stall.md` (M0131-S32, the
single-session half: a 64-version chain-walk cap).

## 1. Symptom

With the S32 chain-walk fix in place, `analysis/atomicity-nocrash-control.sh`
(pgbench TPC-B, scale 5, 16 clients, no crash anywhere) still failed, and failed
in BOTH directions at once:

| table | rows | sum before this change | `sum(history.delta)` |
|---|---|---|---|
| `pgbench_accounts` | 500 000 | −31 482 (exact) | −31 482 |
| `pgbench_tellers` | 50 | −14 938 (under-applied) | −31 482 |
| `pgbench_branches` | 5 | −309 543 (~10× OVER-applied) | −31 482 |

Two opposite signs rule out a single truncation-style cause, and `accounts`
being exact is the clue: at scale 5 two clients essentially never touch the same
account row, whereas 5 branch rows collide constantly. The defect is therefore
*contention*-driven, not table-specific.

## 2. Reducing the workload

TPC-B is far too coarse to localise this — one transaction touches four tables
behind `BEGIN`/`COMMIT`. `analysis/concurrent-hotrow.sh` strips it to the
essence: C sessions each apply N blind `UPDATE t SET v = v + 1 WHERE id = ?`
increments to a table of ROWS rows, so the invariant is arithmetic and exact —
`sum(v)` MUST equal `C*N`. Below `C*N` is a lost update; above it is
over-application. `NOIDX=1` drops the primary key so the same statement is
driven by a SeqScan instead of an IndexScan, splitting the two update operators.

At `ROWS=1 CLIENTS=8 N=200` the indexed arm landed **470 of 1600** increments —
with zero errors, `UPDATE 1` reported every time.

(Harness note now baked into the script: an orphaned server from an earlier
probe still listening on the port silently absorbs the whole run — the fresh
server fails to bind, every `psql` still connects, and the numbers come from the
wrong cluster. The script now preflights the port.)

## 3. Instrumentation

Reading the eight silent-return paths in `tryApplyHOTUpdate` and the
EvalPlanQual retry loop could not say which fired, so
`internal/executor/s321_probe.go` adds an env-gated (`GOOPG_S321_PROBE=1`)
counter per path. One run of the indexed probe:

```
hot_skip_concurrent=1553  hot_applied=1
epq_chain_notfound=1112   epq_chain_followed=834
```

1112 of 1946 write attempts ended at `epq_chain_notfound` — the EPQ loop's
`if !chainFound { epqSkip = true; break }`. That is the whole loss.

## 4. Root cause A — "chain follow found nothing" was read as "row is gone"

The EvalPlanQual retry loop treats a failed chain-follow as a deleted row and
SKIPS it, silently, while the statement still reports `UPDATE 1` (the count comes
from the planned row set — a second, independent defect, ledgered separately).

That conflation is wrong whenever the chain tip was written by a transaction
that has not committed yet. With N sessions on one row: session D waits for A,
refreshes its snapshot, walks to A's new version — and by then B has already
replaced it with a version B has not committed. The tail is invisible under D's
snapshot, `epqFollowHOT`/`epqFollowChain` report not-found, and D's increment is
dropped.

Upstream never skips here. `ExecUpdate` loops on `TM_Updated`, and
`heap_lock_tuple` / `EvalPlanQualFetch` block on the in-progress updater
(`XactLockTableWait`) before re-fetching, so the update lands on whichever
version wins (`postgres/src/backend/executor/nodeModifyTable.c`,
`access/heap/heapam.c`). Under READ COMMITTED that wait is unbounded.

**Fix.** `epqChainPendingWriter` (`internal/executor/operators_storage.go`)
walks the t_ctid chain to its tail and reports the XID of a still-in-flight
transaction that owns it, classified authoritatively via `epqXmaxSettled` rather
than by snapshot membership. Both EPQ loops now wait on that XID
(`epqWait`, which carries the wait-for-graph deadlock detection), refresh the RC
snapshot and re-run the loop instead of skipping. The existing
`maxEPQRetriesRC` backstop bounds a pathological lap. Only an in-progress
**xmin** counts: an aborted xmin, or a committed xmax (a real DELETE), means
there is genuinely nothing to update and the existing skip is correct.

## 5. Root cause B — self-modified tuples were re-updated, leaving two live rows

Making the loop retry immediately exposed a second defect in the SeqScan arm:
`count(*)` on a one-row table reached **1720**. After a chain-follow, two
`pending` entries of the SAME statement can converge on one physical tuple.
`isConcurrentlyUpdated` deliberately ignores our own xmax, so the second entry
re-stamped a tuple this transaction had already killed and inserted a SECOND new
version — two live rows for one logical row. That is precisely the
`pgbench_branches` "~10× over-applied" signature.

Upstream `heap_update` returns `TM_SelfModified` for a tuple the current command
already updated, and `ExecUpdate` skips the row. Both EPQ loops now carry the
same guard at the top of the retry (the index arm's existing
`AI-20260810-011258-006` dedupe only covers duplicates *within one scan*, before
any chain-follow).

## 6. Result

`analysis/concurrent-hotrow.sh` indexed arm, `ROWS=1/5`, `CLIENTS=8`, `N=200`,
with and without `BEGIN`/`COMMIT`: **exactly 1600 of 1600**, `count(*)` correct.

`analysis/atomicity-nocrash-control.sh` (`RUNS=1 LOADSEC=30`):

| table | before | after | `sum(delta)` |
|---|---|---|---|
| `pgbench_accounts` | exact | exact | −68 881 |
| `pgbench_tellers` | under-applied | **exact** | −68 881 |
| `pgbench_branches` | ~10× over | −54 588 (under) | −68 881 |

## 7. M0131-S32.2 — the SeqScan arm (fixed 2026-08-12, loop #146)

The SeqScan-driven arm lost updates for two further reasons, both in the
**scan-phase** EPQ loop (`updateOp.Next`, inside the `scanMatching` callback) —
a loop the S32.1 fixes never touched, because S32.1 only hardened the two
*write-phase* loops.

**Defect C — the same silent chain-follow skip, one loop earlier.** When the
scan-phase EPQ loop's `epqFollowHOT`/`epqFollowChain` produced nothing it did
`return nil // row deleted by concurrent tx`, dropping the row from `pending`
before the write phase ever saw it. That is why the losses looked like a scan
problem from outside: the row never entered `pending`, so the statement reported
`UPDATE 0` with no write-phase counter firing. As in S32.1 the premise is wrong —
a chain tip owned by a transaction still in flight is not a delete. Fix: the same
`epqChainPendingWriter` + `epqWait` + retry, bounded by `epqRetryLimit`.
Measured effect alone: `scan_epq_notfound` 20 708 → 4 852.

**Defect D — the EPQ snapshot leaked into the scan, forking one row into many.**
`epqWait` refreshes `ctx.Snap` (so the following `epqRecheckVisible` sees the
committer). In the write-phase loops that is harmless — the scan is over. In the
scan-phase loop it is not: `scanMatching` decides tuple visibility from the very
same `ctx.Snap`. Refreshing it mid-scan lets the rest of the scan see versions
committed *after* statement start, so the same logical row is handed to the
callback twice — once as the version live at statement start, once as a
successor another session has since committed. Both entries land in `pending`,
both are written, and because they are two *different* physical tuples the
S32.1 `TM_SelfModified` guard (which keys on our own xmax) does not fire: the
row forks into two live versions, then those fork again. At
`NOIDX=1 ROWS=1 CLIENTS=8 N=200` a one-row table ended with 130 live rows and
`sum(v)` 96 435 instead of 1600 — the pgbench_branches "over-applied"
signature. Upstream holds the scan's snapshot fixed for the whole statement and
confines the EPQ re-fetch to the tuple being updated
(`postgres/src/backend/executor/execMain.c`, `EvalPlanQual*`). Fix: save
`ctx.Snap` before the loop and restore it on every path that returns to the
scan.

Both defects are needed: with only C the fork gets worse (166 live rows); with
only D the losses remain.

### Result

`analysis/concurrent-hotrow.sh` is now exact — 1600/1600, `count(*)` correct,
zero client errors — in all four configurations: `NOIDX=1` ROWS=1 and ROWS=5,
the IndexScan arm ROWS=1 and ROWS=5, with and without `BEGIN`/`COMMIT`.

`RUNS=1 LOADSEC=30 analysis/atomicity-nocrash-control.sh` (pgbench TPC-B,
scale 5, 16 clients):

| table | S32.1 | S32.2 | `sum(delta)` |
|---|---|---|---|
| `pgbench_accounts` | exact | exact | −969 135 |
| `pgbench_tellers` | exact | exact | −969 135 |
| `pgbench_branches` | −54 588 vs −68 881 (−21 %) | −966 698 (−0.25 %) | −969 135 |

So theresidual is now ~0.25 % on the single hottest 5-row table under a
4-table `BEGIN`/`COMMIT` workload, tracked as **M0131-S32.3**; the isolated
single-table repro is exact.

## 8. Files

- `internal/executor/operators_storage.go` — `epqChainPendingWriter`, the
  pending-writer wait in both EPQ loops, the `TM_SelfModified` guard in both.
- `internal/executor/s321_probe.go` — env-gated diagnostic counters
  (`GOOPG_S321_PROBE=1`) and the `GOOPG_S321_NOWAIT=1` / `GOOPG_S322_NOWAIT=1`
  A/B kill switches (write-phase and scan-phase wait respectively — the split is
  what attributed the fork to defect D rather than to defect C). Temporary;
  remove with M0131-S32.3.
- `analysis/concurrent-hotrow.sh` — the deterministic minimal repro / gate.
