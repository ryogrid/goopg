# M0103-0007 rung 7 — PG-publisher → goopg-subscriber SAVEPOINT subxacts

## Status

accepted

## Context

Rung 6 (design `0103-0029`) pinned the multi-DML single-transaction shape:
one explicit `BEGIN; … COMMIT;` block carrying multiple statements arrives
on the wire as one pgoutput `B…C` block. The subscriber opens one TxnMgr
transaction, replays every change inside it, and commits at the close.

The next dimension to scale is **subtransactions** (`SAVEPOINT` /
`RELEASE` / `ROLLBACK TO SAVEPOINT`). On the publisher these introduce
nested xact state: each `SAVEPOINT` opens a new subxact assigned its own
XID, `ROLLBACK TO` aborts every change made since the savepoint, and
`RELEASE` merges the subxact's effects into the parent. The visible-state
contract is unchanged: only changes that survive to top-level `COMMIT`
become visible to other backends.

The wire encoding depends on the pgoutput protocol version:

  - **`proto_version=1`** (default; goopg's
    `LogicalReceiverConfig.ProtoVersion` defaults to 1 — see
    `internal/server/logicalreceiver.go:149-151`). Subxact boundaries are
    **not** streamed. The publisher buffers the whole top-level
    transaction in its slot reorder buffer; on top-level commit it emits
    one `B` / per-row change / `C` block reflecting only the net effect
    of committed (i.e. not-rolled-back) subxacts. Aborted subxact
    contents are dropped at the publisher and never reach the wire.
  - **`proto_version=2+`** (streaming): the publisher emits subxact-
    boundary frames (`Y` for a new subxact tied to a parent XID, plus
    streaming `B`/`C`/`A`-abort frames). The apply worker tracks parent/
    child XID linkage so it can mirror subxact rollback.

This rung pins the proto_version=1 contract: **the apply worker
correctly receives only the committed-subxact net effect**. No
subxact-aware code is exercised because the publisher does the work at
buffer-flush time. The verification is nonetheless load-bearing —
without it, a regression in the publisher's reorder-buffer logic, in
goopg's slot decoder's xact framing, or in the apply worker's apply
order would surface as the rolled-back changes appearing on the
subscriber.

## Decision

This rung is **pure verification** at proto_version=1. No code changes
are expected; the machinery already handles every shape this rung
exercises:

  - The publisher's reorder buffer (upstream PG) filters rolled-back
    subxact rows before emission.
  - goopg's slot decoder consumes the resulting `B…C` block as a single
    xact (the wire frames carry only the top-level XID at
    proto_version=1).
  - The apply worker's `applyBegin` / `applyCommit` pair handles the
    block exactly as rung 6 did. Inside the block, `applyInsert` /
    `applyUpdateByKey` / `applyDeleteByKey` thread `w.currentTx` through
    `applyContext()`, and own-xact write visibility
    (`mvcc.TupleVisibleSubxact`'s `isCurrentTxXID` short-circuit) makes
    later `U`/`D` handlers see rows the earlier `I` handlers wrote in
    the same xact.

The rung's deliverable is one live E2E test that exercises a
SAVEPOINT-heavy workload end-to-end and asserts only the committed-
subxact effects on the subscriber.

A proto_version=2 streaming rung is **out of scope here**. Promoting the
default to `proto_version=2` requires apply-worker subxact tracking
(parent/child XID map, per-subxact tuple buffer, stream-abort handling)
and is a much larger change. It belongs to a later rung once the
proto_version=1 verification baseline is locked in.

## What this pins

### Live E2E: `TestPort_PgoutputInteropPGToGoopgSavepointXact`

Same harness as rungs 1–6 (`pubsubcluster.NewMixed` with a PG publisher,
a goopg subscriber, slot pre-created via
`pg_create_logical_replication_slot`, `proto_version=1`). Schema is the
small `(id int PRIMARY KEY, v text)` shape used by rungs 2–6.

Workload — issued through **one** `psc.Publisher.Exec(...)` call so the
whole block arrives as a single libpq simple-query and runs as one
top-level PostgreSQL transaction. SAVEPOINT operations create subxacts;
ROLLBACK TO discards their effects; RELEASE merges them into the parent
xact:

```sql
BEGIN;
INSERT INTO public.t VALUES (1, 'one');             -- top-level
SAVEPOINT s1;
  INSERT INTO public.t VALUES (2, 'two-rolled');    -- in s1
  UPDATE public.t SET v = 'one-rolled' WHERE id = 1; -- in s1
ROLLBACK TO SAVEPOINT s1;                            -- discards both
SAVEPOINT s2;
  INSERT INTO public.t VALUES (3, 'three');         -- in s2
RELEASE SAVEPOINT s2;                                -- merges into parent
INSERT INTO public.t VALUES (4, 'four');            -- top-level
SAVEPOINT s3;
  DELETE FROM public.t WHERE id = 3;                -- in s3
ROLLBACK TO SAVEPOINT s3;                            -- restores id=3
COMMIT;
```

Expected subscriber state after apply propagation:

  - `id=1 v='one'`  — top-level INSERT survived; the s1 UPDATE that
    rewrote `v` to `'one-rolled'` was rolled back.
  - `id=2` — does not exist (the s1 INSERT was rolled back).
  - `id=3 v='three'` — s2 INSERT survived (RELEASE merged into parent);
    the s3 DELETE that targeted id=3 was rolled back.
  - `id=4 v='four'` — top-level INSERT survived.
  - `count(*) == 3`.

All assertions go through **fresh** `database/sql` sessions
(`psc.WaitForRow` opens a new conn per call), so each query traverses
the goopg PK IndexScan path against committed state — the
single-session "see own writes" of psql couldn't distinguish rolled-
back from committed subxact effects after the publisher's reorder-
buffer flush.

Each assertion fail-fasts on a distinct regression:

  - `count(*) == 3` catches "the publisher leaked rolled-back inserts"
    (would yield 4 or 5) or "the apply worker re-applied a rolled-back
    DELETE" (would yield 2).
  - `id=1 v='one'` catches the s1 UPDATE leaking through — that would
    leave `v='one-rolled'`.
  - `id=2` count of 0 catches the s1 INSERT leaking through.
  - `id=3 v='three'` catches BOTH the s2 RELEASE failing to commit (no
    row at id=3) AND the s3 ROLLBACK TO failing to undo the DELETE
    (count=0 at id=3).
  - `id=4 v='four'` catches the post-RELEASE top-level INSERT being
    accidentally tied to an aborted subxact at the publisher.

### What is intentionally NOT pinned

  - **`proto_version=2+` streaming subxacts.** The publisher would emit
    `Y` frames carrying parent-XID linkage plus per-subxact `B`/`C`/`A`
    frames; the apply worker would need per-subxact tuple buffering and
    a stream-abort handler. That is a separate rung once the v0 baseline
    here is locked.
  - **`PREPARE TRANSACTION` / two-phase commit.** Out of scope for
    M0103-0007; needs the prepared-transactions plumbing.
  - **Concurrent transactions interleaving with subxacts.** Two psql
    sessions with their own SAVEPOINTs commit serially on the publisher
    slot — the apply worker sees a strictly serial stream of top-level
    `B…C` blocks. No new code path is exercised relative to this rung.

## Verification

```
go test -count=1 -timeout 120s -run TestPort_PgoutputInteropPGToGoopgSavepointXact \
  ./internal/testport/
go test -count=1 -timeout 300s -run TestPort_PgoutputInteropPGToGoopg \
  ./internal/testport/
```

The first command must PASS. The second must PASS for every existing
rung 1–6 test (`...Goopg`, `...GoopgFullDML`, `...GoopgBatchDML`,
`...GoopgReplicaIdentityFull`, `...GoopgUnchangedToast`,
`...GoopgMultiDMLXact`) so the new rung does not regress earlier
coverage.

Targeted regressions:

```
go test -race -count=1 -timeout 180s ./internal/executor/ \
  ./internal/wal/ ./internal/server/ ./internal/catalog/ \
  ./internal/testutil/pubsubcluster/
```

All must remain green.

## Why a separate rung, not a phase inside rung 6?

Rung 6's `BEGIN; INSERT/INSERT/INSERT/UPDATE/DELETE; COMMIT;` block has
zero subxact frames — every change belongs to the top-level XID. This
rung's workload introduces subxact state at the publisher and exercises
the rolled-back-content suppression contract that rung 6 cannot reach.
The two together establish the proto_version=1 transaction shape
matrix: single-statement autocommit (rungs 1–5), multi-DML single-xact
(rung 6), subxact rollback (this rung). proto_version=2 streaming is
the natural next dimension.
