# M0103-0007 rung 8 — PG-publisher → goopg-subscriber multi-table interleaved DML

## Status

accepted

## Context

Rungs 1–7 (designs `0103-0023` through `0103-0030`) all exercised a
single published table (`public.t (id int PRIMARY KEY, v text)`). That
schema kept the test surface tight while each rung scaled a different
orthogonal dimension: REPLICA IDENTITY FULL (rung 4), unchanged-TOAST
(rung 5), multi-DML single transaction (rung 6), SAVEPOINT subxacts
(rung 7). What none of them touched is **multi-relation** state on the
wire.

The apply worker's relation cache (`internal/executor/applyworker.go`)
keys decoded `Relation` (`R`) messages by `relOid` and threads
`*catalog.Table` through every `applyInsert` / `applyUpdateByKey` /
`applyDeleteByKey` call. With one published table the cache only ever
holds one entry, the relOid is constant for the life of the slot, and
the table-resolution map (`applyContext()`'s
`subscriberRelByOID[oid]`) is a one-element lookup. A bug in any of
the dispatch sites — relOid → relation mismatch, stale relation
overwrite when a second `R` arrives, mutex misuse across goroutines,
schema column-index drift between the wire tuple and the subscriber's
`*catalog.Table.Columns` — is silently masked by the single-table
shape.

This rung pins the **multi-relation contract**. Each rung's design doc
calls out a load-bearing property the rung's test fail-fasts. Here the
property is:

> When two published tables flow through the same slot, interleaved
> DML against both inside a single top-level transaction, the
> subscriber's post-commit state for each table matches what a
> serial-equivalent execution on the publisher would have produced —
> **per-table** correctness must hold for both.

## Decision

This rung is **pure verification** at proto_version=1 with the
existing relation-cache machinery. No new code is expected; rungs 1–7
already wired:

  - Decoder dispatch on `R` messages —
    `internal/wal/pgoutput_decoder.go::DecodeMessage`'s `case 'R'`
    populates `Relation.OID`, `Schema`, `Name`, `Columns[]`.
  - Apply-worker relation cache —
    `internal/executor/applyworker.go::applyRelation` records
    `Relation` into `w.relations[m.OID]` and resolves the live
    `*catalog.Table` via `applyContext()` so subsequent
    `applyInsert`/`applyUpdate`/`applyDelete` calls index the
    matching subscriber-side table.
  - Per-statement xact wrap — `applyBegin` opens one TxnMgr
    transaction; every change inside the `B…C` block routes through
    that transaction; `applyCommit` closes it.

If multi-table replication regresses in any of the above sites, this
rung's assertions catch it. The most likely failure modes:

  1. **Cross-relation dispatch leak.** A row destined for `orders`
     gets written to `users` (or vice versa) because the apply worker
     reused a stale relation pointer after the second `R` message
     arrived. Caught by the per-table count assertions:
     `count(users)` and `count(orders)` would diverge from expected.
  2. **Index-cache leak.** `maintainUniqueIndexesForInsert` is keyed
     on `*catalog.Table`. If the apply worker passed `users`'s
     `*catalog.Table` to an `orders` INSERT call, the PK index on
     `orders.id` would not be maintained and the subsequent IndexScan
     would miss the row.
  3. **Column-index drift.** The two tables have different column
     counts and different value types. If the apply worker decoded
     the wire tuple against the wrong relation's `Columns[]`, the
     row would either store transposed values (e.g.
     `(user_id, amount)` written into `(id, name)`) or be rejected
     by `parseDatumText` when the value-shape mismatched the
     subscriber column type.

The rung therefore deliberately uses tables with **different column
counts** (`users` 2 cols, `orders` 3 cols) and **different column
types at the same ordinal** (col 1 is `text` on `users` but `int` on
`orders`). A transposed-dispatch bug would surface as a parse error
on the wire, not a silent value swap.

## What this pins

### Live E2E: `TestPort_PgoutputInteropPGToGoopgMultiTable`

Same harness as rungs 1–7 (`pubsubcluster.NewMixed` with PG publisher,
goopg subscriber, slot pre-created via
`pg_create_logical_replication_slot`, `proto_version=1`).

Schemas:

```sql
CREATE TABLE public.users  (id int PRIMARY KEY, name text);
CREATE TABLE public.orders (id int PRIMARY KEY, user_id int, amount int);
```

Both tables join `CREATE PUBLICATION p FOR TABLE users, orders`. The
publication-name canonicalisation work (rung 11 of M0103-0008, design
`0103-0015`) ensures both relation entries are stored with the
canonical `public.users` / `public.orders` keys so the publisher's
`publicationFilter.byTable` lookup hits for both.

Workload — issued through **one** `psc.Publisher.Exec(...)` call so
the whole block arrives as a single libpq simple-query and runs as
one top-level PostgreSQL transaction. DML interleaves the two tables:

```sql
BEGIN;
INSERT INTO public.users  VALUES (1, 'alice');
INSERT INTO public.orders VALUES (10, 1, 100);
INSERT INTO public.users  VALUES (2, 'bob');
INSERT INTO public.orders VALUES (11, 2, 200);
INSERT INTO public.orders VALUES (12, 1, 50);
UPDATE public.orders SET amount = 99 WHERE id = 10;
UPDATE public.users  SET name = 'alice-updated' WHERE id = 1;
DELETE FROM public.users  WHERE id = 2;
DELETE FROM public.orders WHERE id = 11;
COMMIT;
```

A second autocommit phase confirms post-xact multi-relation routing
still works:

```sql
INSERT INTO public.users  VALUES (3, 'carol');
INSERT INTO public.orders VALUES (13, 3, 75);
```

Expected subscriber state after apply propagation:

  - `public.users`:
    - `id=1 name='alice-updated'` (top-level INSERT + same-xact UPDATE)
    - `id=3 name='carol'` (post-xact autocommit INSERT)
    - `id=2` does NOT exist (deleted in xact)
    - `count(*) == 2`
  - `public.orders`:
    - `id=10 user_id=1 amount=99`  (INSERT then UPDATE in same xact)
    - `id=12 user_id=1 amount=50`  (untouched in xact)
    - `id=13 user_id=3 amount=75`  (autocommit INSERT)
    - `id=11` does NOT exist (deleted in xact)
    - `count(*) == 3`

All assertions go through **fresh** `database/sql` sessions
(`psc.WaitForRow` opens a new conn per call), traversing the goopg PK
IndexScan path against committed state — the same scheme rungs 1–7
use.

### Wire-frame ordering this rung verifies (implicitly)

The publisher's reorder buffer (upstream PG) emits frames in arrival
order. The wire for this rung carries (sketch, OIDs abbreviated):

```
B
R rel=users_oid    (id int, name text)
I rel=users_oid    tuple=(1,'alice')
R rel=orders_oid   (id int, user_id int, amount int)
I rel=orders_oid   tuple=(10,1,100)
I rel=users_oid    tuple=(2,'bob')
I rel=orders_oid   tuple=(11,2,200)
I rel=orders_oid   tuple=(12,1,50)
U rel=orders_oid   tuple=(10,1,99)
U rel=users_oid    tuple=(1,'alice-updated')
D rel=users_oid    key=(2)
D rel=orders_oid   key=(11)
C
```

`R` messages are emitted lazily — the first DML touching a relation
that the publisher hasn't already described in this stream triggers
the corresponding `R`. The apply worker's relation cache must accept
two `R` messages inside one `B…C` block and keep both entries live.

### What is intentionally NOT pinned

  - **Tables with a foreign key from `orders.user_id` to
    `users.id`.** Logical replication on the subscriber does not
    enforce FK relationships among replicated tables, and goopg's
    apply worker does not run constraint triggers. Adding an FK
    would conflate apply-worker dispatch with constraint semantics.
    The relation between the tables is "logical" (same `user_id`
    integer) but unenforced.
  - **Three or more tables.** Two tables is the minimum that proves
    the dispatch path multiplexes; adding a third would not add
    coverage of any new code path.
  - **DDL inside the xact** (`CREATE TABLE ... ; INSERT ...`).
    pgoutput does not replicate DDL at proto_version=1; the test
    creates both tables on both peers before subscription, matching
    rungs 1–7.

## Verification

```
go test -count=1 -timeout 120s -run TestPort_PgoutputInteropPGToGoopgMultiTable \
  ./internal/testport/
go test -count=1 -timeout 300s -run TestPort_PgoutputInteropPGToGoopg \
  ./internal/testport/
```

The first command must PASS. The second must PASS for every existing
rung 1–7 test so the new rung does not regress earlier coverage.

Targeted regressions:

```
go test -race -count=1 -timeout 180s ./internal/executor/ \
  ./internal/wal/ ./internal/server/ ./internal/catalog/ \
  ./internal/testutil/pubsubcluster/
```

All must remain green.

## Why a separate rung, not a phase inside rung 6 or 7?

Rung 6 used the same one-`B…C`-block shape but against a single
relation; the apply worker's relation cache only ever held one entry,
so the per-relation dispatch path was untested at that level. Rung 7
added subxact framing on the same single relation. Multi-relation
dispatch is orthogonal to both: it exercises the relation cache's
multi-entry contract, the schema column-index resolution under
schema-divergent tuples, and the per-table index maintenance in
`maintainUniqueIndexesForInsert`. None of those code paths were
exercised by rungs 1–7. The rung therefore establishes the
multi-relation baseline that subsequent rungs (pgbench, DDL,
proto_version=2) can build on without re-verifying it.
