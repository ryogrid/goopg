# M0103-0007 rung 6 — PG-publisher → goopg-subscriber multi-DML single transaction

## Status

accepted

## Context

Rungs 1–5 of M0103-0007 closed the apply path under a series of DML
shapes — index maintenance, full DML round-trip, sustained batch, REPLICA
IDENTITY FULL, unchanged TOAST. In every case the publisher's DML
statements were issued via `psc.Publisher.Exec(...)`, which shells out to
`psql -c "<stmt>"`. With autocommit on (psql's default) each statement
runs in its own implicit transaction, so pgoutput emits one
`Begin/Change/Commit` triple per statement and the apply worker opens
and closes a fresh `mvcc.Transaction` for each.

The probe shape this rung adds is different: a single explicit
`BEGIN; … COMMIT;` block on the publisher carrying **multiple DML
statements** for the same relation. pgoutput emits one `B` frame, one or
more change frames (`I` / `U` / `D`), and one `C` frame all anchored to
the same backend XID. The apply worker opens **one** TxnMgr transaction
at `B`, replays every change inside it, and commits at `C`.

The interesting correctness property is **same-xact write visibility**:
the `U`/`D` change handlers locate the heap row via sequential scan
(`applyDeleteByKey` / `applyScanFirstMatch`), and that scan must see
tuples that an earlier `I` in the same pgoutput transaction wrote with
`xmin == currentTx.XID`. If the apply worker's snapshot or visibility
predicate dropped own-xact tuples, the `U`/`D` would silently no-op
against an "INSERT-then-mutate" pattern that real-world workloads
(application transactions, pgbench's tpcb-like script) emit constantly.

## Decision

This rung is **pure verification**. No code changes are expected; the
machinery that makes it work was put in place by earlier rungs and the
broader MVCC effort:

  - **One TxnMgr transaction per pgoutput xact** — `applyBegin`
    (`internal/executor/applyworker.go:189-203`) calls `txnMgr.Begin`,
    `applyCommit` (`:630-648`) calls `txnMgr.Commit`. Every `I`/`U`/`D`
    in between threads `w.currentTx` through `applyContext()`.
  - **Own-xact write visibility** —
    `mvcc.TupleVisibleSubxact(h, snap, currentXID, txnMgr)` in
    `internal/mvcc/subxact_visibility.go:131-159` short-circuits on
    `isCurrentTxXID(h.Xmin, currentXID, r)` *before* consulting the
    snapshot. A tuple this xact just wrote is self-visible regardless of
    whether the snapshot was captured before or after the write.
  - **Fresh snapshot per applyContext** — `applyContext`
    (`:365-378`) calls `txnMgr.SnapshotFor(w.currentTx)` each time. This
    is irrelevant for own-xact visibility (the XID equality check fires
    first) but correctly reflects committed concurrent xacts for any
    cross-xact reads the apply path might add later.
  - **PK index maintained per `I`** — `maintainUniqueIndexesForInsert`
    runs at the end of `applyInsert` (rung 1). Subsequent UPDATE/DELETE
    in the same xact don't probe the index (the `applyDeleteByKey` path
    is sequential-scan), but post-commit fresh sessions do hit the index
    for assertion queries, so this stays load-bearing.

The rung's deliverable is one live E2E test that exercises the full
chain end-to-end.

## What this pins

### Live E2E: `TestPort_PgoutputInteropPGToGoopgMultiDMLXact`

Same harness as rungs 1–5 (`pubsubcluster.NewMixed` with a PG
publisher, a goopg subscriber, slot pre-created via
`pg_create_logical_replication_slot`). Schema is the small
`(id int PRIMARY KEY, v text)` shape used by rungs 2–3.

Workload — issued through **one** `psc.Publisher.Exec(...)` call so the
whole `BEGIN;…COMMIT;` arrives as a single libpq simple-query, runs as
one PostgreSQL transaction, and produces one pgoutput xact:

```sql
BEGIN;
INSERT INTO public.t VALUES (1, 'one');
INSERT INTO public.t VALUES (2, 'two');
INSERT INTO public.t VALUES (3, 'three');
UPDATE public.t SET v = 'two-prime' WHERE id = 2;
DELETE FROM public.t WHERE id = 3;
COMMIT;
```

After commit and apply propagation the subscriber must reach the
following state (asserted from **fresh** `database/sql` sessions, so
each WaitForRow query goes through the goopg PK IndexScan path; the
single-session "see own writes" of psql wouldn't tell us anything about
how committed state is exposed):

  - `count(*) WHERE 1=1` → 2 (id=1 and id=2 survive; id=3 was deleted
    inside the same xact that inserted it).
  - `WHERE id = 1 AND v = 'one'` → 1 (untouched INSERT).
  - `WHERE id = 2 AND v = 'two-prime'` → 1 (INSERT-then-UPDATE inside
    same xact; UPDATE saw own write).
  - `WHERE id = 3` → 0 (INSERT-then-DELETE inside same xact; DELETE saw
    own write; the heap re-fetch + MVCC dead-tuple filtering on the PK
    IndexScan correctly drops any orphan PK entry).

Each assertion fail-fasts on a distinct potential regression:

  - `count(*) == 2` catches the "DELETE didn't see own INSERT" failure
    mode — without own-xact visibility, applyDelete would no-op and the
    final count would be 3.
  - `id=2 v='two-prime'` catches the "UPDATE didn't see own INSERT"
    failure mode — without own-xact visibility, applyUpdateByKey's
    `applyDeleteByKey` step would no-op (the new tuple still gets
    written by `writeHeapRowReturning`, but the original `(2,'two')`
    would survive as a stray row).
  - `id=3` count of 0 (rather than 1 with `v='three'`) catches the
    same DELETE-no-op pattern in isolation.

### What is intentionally NOT pinned

This rung does **not** add coverage for:

  - **`SAVEPOINT` / `ROLLBACK TO SAVEPOINT`** — pgoutput emits subxact
    boundaries (`Y` type, plus `B`/`C` with parent_xid linkage) that the
    apply worker does not yet handle. A separate rung will close
    subxact support.
  - **Concurrent transactions on the publisher** — two psql sessions
    interleaving DML inside their own explicit xacts. The publisher
    serialises them in commit order on the slot, so the apply worker
    sees a strictly serial stream of `B…C` blocks; no new code path is
    exercised relative to rungs 1–5.
  - **DDL inside the xact** — DDL statements (`CREATE TABLE`,
    `ALTER TABLE …`) are not currently replicated by pgoutput and are
    out of scope for M0103.
  - **`pgbench` workload, `kill -9` + libpq multi-host reconnect** —
    slated for later rungs.

## Verification

```
go test -count=1 -timeout 120s -run TestPort_PgoutputInteropPGToGoopgMultiDMLXact \
  ./internal/testport/
go test -count=1 -timeout 240s -run TestPort_PgoutputInteropPGToGoopg \
  ./internal/testport/
```

The first command must PASS. The second must PASS for every existing
rung 1–5 test (`...Goopg`, `...GoopgFullDML`, `...GoopgBatchDML`,
`...GoopgReplicaIdentityFull`, `...GoopgUnchangedToast`) so the new
rung does not regress earlier coverage.

Targeted unit regressions:

```
go test -race -count=1 -timeout 180s ./internal/executor/ \
  ./internal/wal/ ./internal/server/ ./internal/catalog/ \
  ./internal/testutil/pubsubcluster/
```

All must remain green.

## Why a separate rung, not a phase inside rung 3?

Rung 3 (batch DML) loops over 50 INSERT / 25 UPDATE / 10 DELETE
statements, each as its own `psc.Publisher.Exec(...)` call. That
deliberately produces 85 separate autocommit xacts on the publisher
(50+25+10), one pgoutput `B…C` per statement, and 85 separate
`txnMgr.Begin/Commit` pairs on the subscriber. It scales rungs 1–2
horizontally but doesn't probe a multi-DML single-xact shape.

This rung scales vertically: one BEGIN, multiple DMLs, one COMMIT. It
is the shape every real-world OLTP application produces.
